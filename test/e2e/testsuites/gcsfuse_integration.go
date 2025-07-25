/*
Copyright 2018 The Kubernetes Authors.
Copyright 2022 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package testsuites

import (
	"context"
	"fmt"
	"strings"

	"local/test/e2e/specs"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	gcsfuseIntegrationTestsBasePath = "gcsfuse/tools/integration_tests"

	testNameOperations            = "operations"
	testNameReadonly              = "readonly"
	testNameRenameDirLimit        = "rename_dir_limit"
	testNameImplicitDir           = "implicit_dir"
	testNameExplicitDir           = "explicit_dir"
	testNameReadLargeFiles        = "read_large_files"
	testNameWriteLargeFiles       = "write_large_files"
	testNameGzip                  = "gzip"
	testNameLocalFile             = "local_file"
	testNameListLargeDir          = "list_large_dir"
	testNameManagedFolders        = "managed_folders"
	testNameConcurrentOperations  = "concurrent_operations"
	testNameKernelListCache       = "kernel_list_cache"
	testNameEnableStreamingWrites = "streaming_writes"
	testNameInactiveStreamTimeout = "inactive_stream_timeout"

	testNamePrefixSucceed = "should succeed in "

	masterBranchName = "master"

	defaultSidecarMemoryLimit   = "1Gi"
	defaultSidecarMemoryRequest = "512Mi"

	// In order to pass anything to the sidecar, you need to pass it via the mountPaths that the sidecar injects.
	// So for temporary files, that would be mountPath: /gcsfuse-tmp, via the gke-gcsfuse-tmp volume.
	inactive_stream_timeout_log_file = "/gcsfuse-tmp/log.json"
)

var gcsfuseVersionStr = ""

const gcsfuseGoVersionCommand = `grep -o 'go[0-9]\+\.[0-9]\+\.[0-9]\+' ./gcsfuse/tools/cd_scripts/e2e_test.sh | cut -c3-`

func hnsEnabled(driver storageframework.TestDriver) bool {
	gcsfuseCSITestDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
	gomega.Expect(ok).To(gomega.BeTrue(), "failed to cast storageframework.TestDriver to *specs.GCSFuseCSITestDriver")

	return gcsfuseCSITestDriver.EnableHierarchicalNamespace
}

// zbEnabled checks if the Zonal Buckets feature is enabled for the given driver.
// It is needs to be used in the integration test commands because gcsfuse tests parallelize and disabled tests based on this --zonal flag.
// Gcsfuse team adds this in this commit https://github.com/GoogleCloudPlatform/gcsfuse/commit/c9a273daf6028ac90dd18f35e74014763d5aa03c.
func zbEnabled(driver storageframework.TestDriver) bool {
	gcsfuseCSITestDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
	gomega.Expect(ok).To(gomega.BeTrue(), "failed to cast storageframework.TestDriver to *specs.GCSFuseCSITestDriver")

	return gcsfuseCSITestDriver.EnableZB
}

func getClientProtocol(driver storageframework.TestDriver) string {
	gcsfuseCSITestDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
	gomega.Expect(ok).To(gomega.BeTrue(), "failed to cast storageframework.TestDriver to *specs.GCSFuseCSITestDriver")

	return gcsfuseCSITestDriver.ClientProtocol
}

type gcsFuseCSIGCSFuseIntegrationTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIGCSFuseIntegrationTestSuite returns gcsFuseCSIGCSFuseIntegrationTestSuite that implements TestSuite interface.
func InitGcsFuseCSIGCSFuseIntegrationTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIGCSFuseIntegrationTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "gcsfuseIntegration",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIGCSFuseIntegrationTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIGCSFuseIntegrationTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIGCSFuseIntegrationTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("gcsfuse-integration", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	// skipTestOrProceedWithBranch works by skipping tests for gcsfuse versions that do not support them.
	// These tests run against all non-managed driver versions, and for selected gke managed driver versions. This is because when
	// we build the non-managed driver, we build gcsfuse from master and assign a tag of 999 to that build. This automatically
	// qualifies the non-managed driver to run all the tests.
	skipTestOrProceedWithBranch := func(gcsfuseVersionStr, testName string) string {
		v, err := version.ParseSemantic(gcsfuseVersionStr)
		// When the gcsfuse binary is built using the head commit in the test pipeline,
		// the version format does not obey the syntax and semantics of the "Semantic Versioning".
		// Always use master branch if the gcsfuse binary is built using the head commit.
		if err != nil {
			return masterBranchName
		}

		// If the GCSFuse version is exactly v3.0.0, use the master branch to fetch the tests.
		// This is because GCSFuse made test fixes to read_large_files and local_file tests,
		// after the v3.0.0 release as cut, and we don't want to block the release on them
		// creating a new patch version for this. After v3.1.0 is fully rolled out, and we
		// confirm we are no longer running CI tests on v3.0.0, we should remove this block.
		// v.EqualTo is only supported in k8s.io/apimachinery v0.31.0, and we are still using
		// v0.30.
		if v.AtLeast(version.MustParseSemantic("v3.0.0")) && v.LessThan(version.MustParseSemantic("v3.1.0")) {
			return masterBranchName
		}

		// check if the given gcsfuse version supports the test case
		if !v.AtLeast(version.MustParseSemantic("v2.3.1")) {
			if testName == testNameListLargeDir || testName == testNameConcurrentOperations || testName == testNameKernelListCache {
				e2eskipper.Skipf("skip gcsfuse integration test %v for gcsfuse version %v", testName, v.String())
			}
		}

		// HNS is supported after v2.5.0
		if !v.AtLeast(version.MustParseSemantic("v2.5.0-gke.0")) && hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration HNS tests on gcsfuse version %v", v.String())
		}

		// GCSFuse flag enable-streaming-writes is supported after v2.9.0.
		if !v.AtLeast(version.MustParseSemantic("v2.9.0")) && testName == testNameEnableStreamingWrites {
			e2eskipper.Skipf("skip gcsfuse integration test %v for gcsfuse version %v", testNameEnableStreamingWrites, v.String())
		}

		// tests are added or modified after v2.3.1 release and before v2.4.0 release
		if !v.AtLeast(version.MustParseSemantic("v2.4.0")) && (testName == testNameListLargeDir || testName == testNameConcurrentOperations || testName == testNameKernelListCache || testName == testNameLocalFile) {
			return "v2.4.0"
		}

		// GCSFuse inactive_stream_timeout tests are supported after v3.1.0.
		if !v.AtLeast(version.MustParseSemantic("v3.1.0")) && testName == testNameInactiveStreamTimeout {
			e2eskipper.Skipf("skip gcsfuse integration test %v for gcsfuse version %v", testNameInactiveStreamTimeout, v.String())
		}

		// By default, use the test code in the same release tag branch
		return fmt.Sprintf("v%v.%v.%v", v.Major(), v.Minor(), v.Patch())
	}

	gcsfuseIntegrationTest := func(testName string, readOnly bool, mountOptions ...string) {
		testCase := ""
		if strings.HasPrefix(testName, testNameKernelListCache) || strings.HasPrefix(testName, testNameManagedFolders) || strings.HasPrefix(testName, testNameInactiveStreamTimeout) {
			l := strings.Split(testName, ":")
			testCase = l[1]
			testName = l[0]
		}

		ginkgo.By("Checking GCSFuse version and skip test if needed")
		if gcsfuseVersionStr == "" {
			gcsfuseVersionStr = specs.GetGCSFuseVersion(ctx, f.ClientSet)
		}
		ginkgo.By(fmt.Sprintf("Running integration test %v with GCSFuse version %v", testName, gcsfuseVersionStr))
		gcsfuseTestBranch := skipTestOrProceedWithBranch(gcsfuseVersionStr, testName)
		ginkgo.By(fmt.Sprintf("Running integration test %v with GCSFuse branch %v", testName, gcsfuseTestBranch))

		ginkgo.By("Configuring the test pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetImage(specs.GolangImage)
		tPod.SetResource("1", "5Gi", "5Gi")
		sidecarMemoryLimit := defaultSidecarMemoryLimit

		if testName == testNameWriteLargeFiles || testName == testNameReadLargeFiles {
			tPod.SetResource("1", "6Gi", "5Gi")
			sidecarMemoryLimit = "1Gi"
		}

		mo := l.volumeResource.VolSource.CSI.VolumeAttributes["mountOptions"]
		if testName == testNameExplicitDir && strings.Contains(mo, "only-dir") {
			mo = strings.ReplaceAll(mo, "implicit-dirs,", "")
		}
		mo = strings.ReplaceAll(mo, "logging:severity:info", "logging:severity:trace")
		l.volumeResource.VolSource.CSI.VolumeAttributes["mountOptions"] = mo

		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, readOnly, mountOptions...)
		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/cpu-limit":               "1",
			"gke-gcsfuse/memory-request":          defaultSidecarMemoryRequest,
			"gke-gcsfuse/memory-limit":            sidecarMemoryLimit,
			"gke-gcsfuse/ephemeral-storage-limit": "2Gi",
		})

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		dirPath := ""
		for _, o := range strings.Split(mo, ",") {
			kv := strings.Split(o, "=")
			if len(kv) == 2 && kv[0] == "only-dir" {
				dirPath = kv[1]
			}
		}
		if dirPath != "" {
			if !(testName == testNameRenameDirLimit && hnsEnabled(driver)) {
				bucketName += "/" + dirPath
			}
		}

		if hnsEnabled(driver) {
			tPod.SetupVolumeForHNS(volumeName)
		}

		if testName == testNameInactiveStreamTimeout {
			// This needs to match what gcsfuse sets here:
			// https://github.com/GoogleCloudPlatform/gcsfuse/blob/bb28c11b229d5c2706cbb0abb9eb8634363b3799/tools/integration_tests/inactive_stream_timeout/setup_test.go#L45.
			// The gcsfuse integration tests are run from the main container, which has this hardcoded value. After the tests are setup,
			// the gcsfuse process is started in the sidecar container. Our webhook auto injects some volumeMounts into the sidecar, so we
			// we need to use one of these volume mountPaths below. For this test, we use `/gcsfuse-tmp/<filename>` as the log file to the gcsfuse integration test below.
			inactive_stream_timeout_log_dir := "/tmp/inactive_stream_timeout_logs"
			tPod.SetupTmpVolumeMount(inactive_stream_timeout_log_dir)
		}

		ginkgo.By("Deploying the test pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the test pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the test pod command exits with no error")
		if readOnly {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep ro,", mountPath))
		} else {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		}

		ginkgo.By("Installing dependencies")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("git clone --branch %v https://github.com/GoogleCloudPlatform/gcsfuse.git", gcsfuseTestBranch))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "apt-get install -y apt-transport-https ca-certificates gnupg curl")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "echo 'deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main' | tee -a /etc/apt/sources.list.d/google-cloud-sdk.list")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "ln -s /usr/bin/python3 /usr/bin/python")
		tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, "apt-get update && apt-get install -y google-cloud-cli")

		ginkgo.By("Getting gcsfuse testsuite go version")
		gcsfuseTestSuiteVersion := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, gcsfuseGoVersionCommand)

		ginkgo.By("Checking that the gcsfuse integration tests exits with no error")

		baseTestCommand := fmt.Sprintf("export GOTOOLCHAIN=go%v && export PATH=$PATH:/usr/local/go/bin && cd %v/%v && GODEBUG=asyncpreemptoff=1 go test . -p 1 --integrationTest -v --mountedDirectory=%v", gcsfuseTestSuiteVersion, gcsfuseIntegrationTestsBasePath, testName, mountPath)
		if zbEnabled(driver) {
			baseTestCommand += " --zonal=true"
		}
		baseTestCommandWithTestBucket := baseTestCommand + fmt.Sprintf(" --testbucket=%v", bucketName)

		var finalTestCommand string
		switch testName {
		case testNameReadonly:
			if readOnly {
				finalTestCommand = baseTestCommandWithTestBucket
			} else {
				finalTestCommand = fmt.Sprintf("chmod 777 %v/readonly && useradd -u 6666 -m test-user && su test-user -c '%v'", gcsfuseIntegrationTestsBasePath, baseTestCommandWithTestBucket)
			}
		case testNameExplicitDir, testNameImplicitDir, testNameGzip, testNameLocalFile, testNameOperations, testNameConcurrentOperations, testNameEnableStreamingWrites:
			finalTestCommand = baseTestCommandWithTestBucket
		case testNameRenameDirLimit:
			if gcsfuseTestBranch == masterBranchName || version.MustParseSemantic(gcsfuseTestBranch).AtLeast(version.MustParseSemantic("v2.4.1")) {
				finalTestCommand = baseTestCommandWithTestBucket
			} else {
				finalTestCommand = baseTestCommand
			}
		case testNameKernelListCache, testNameManagedFolders, testNameInactiveStreamTimeout:
			finalTestCommand = baseTestCommandWithTestBucket + " -run " + testCase
		case testNameListLargeDir, testNameWriteLargeFiles:
			finalTestCommand = baseTestCommandWithTestBucket + " -timeout 120m"
		case testNameReadLargeFiles:
			if gcsfuseTestBranch == masterBranchName || version.MustParseSemantic(gcsfuseTestBranch).AtLeast(version.MustParseSemantic("v2.4.1")) {
				finalTestCommand = baseTestCommandWithTestBucket + " -timeout 60m"
			} else {
				finalTestCommand = baseTestCommand + " -timeout 60m"
			}
		default:
			finalTestCommand = baseTestCommand
		}

		tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, finalTestCommand)
	}

	testNameSuffix := func(i int) string {
		return fmt.Sprintf(" test %v", i)
	}

	// The following test cases are derived from https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/tools/integration_tests/run_tests_mounted_directory.sh

	ginkgo.It(testNamePrefixSucceed+testNameOperations+testNameSuffix(1), func() {
		if hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration test %v with flag implicit-dirs when HNS is enabled", testNameOperations)
		}

		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameOperations, false, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameOperations+testNameSuffix(2), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameOperations, false, "implicit-dirs=false")
	})

	ginkgo.It(testNamePrefixSucceed+testNameOperations+testNameSuffix(3), func() {
		if hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration test %v with flag implicit-dirs when HNS is enabled", testNameOperations)
		}

		// passing only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest(testNameOperations, false, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameOperations+testNameSuffix(4), func() {
		// passing only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest(testNameOperations, false, "implicit-dirs=false")
	})

	ginkgo.It(testNamePrefixSucceed+testNameOperations+testNameSuffix(5), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameOperations, false, "write:create-empty-file:true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameReadonly+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameReadonly, true, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameReadonly+testNameSuffix(2), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameReadonly, false, "file-mode=544", "dir-mode=544", "uid=6666", "gid=6666", "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameReadonly+testNameSuffix(3), func() {
		// passing only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest(testNameReadonly, true, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameReadonly+testNameSuffix(4), func() {
		// passing only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest(testNameReadonly, false, "file-mode=544", "dir-mode=544", "uid=6666", "gid=6666", "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameRenameDirLimit+testNameSuffix(1), func() {
		if hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration test %v with flag implicit-dirs when HNS is enabled", testNameRenameDirLimit)
		}

		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameRenameDirLimit, false, "rename-dir-limit=3", "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameRenameDirLimit+testNameSuffix(2), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameRenameDirLimit, false, "rename-dir-limit=3", "implicit-dirs=false")
	})

	ginkgo.It(testNamePrefixSucceed+testNameRenameDirLimit+testNameSuffix(3), func() {
		if hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration test %v with flag implicit-dirs when HNS is enabled", testNameRenameDirLimit)
		}

		// passing only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest(testNameRenameDirLimit, false, "rename-dir-limit=3", "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameRenameDirLimit+testNameSuffix(4), func() {
		// passing only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest(testNameRenameDirLimit, false, "rename-dir-limit=3", "implicit-dirs=false")
	})

	ginkgo.It(testNamePrefixSucceed+testNameImplicitDir+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameImplicitDir, false, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameImplicitDir+testNameSuffix(2), func() {
		// passing only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest(testNameImplicitDir, false, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameExplicitDir+testNameSuffix(1), func() {
		if hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration test %v when HNS is enabled", testNameExplicitDir)
		}

		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameExplicitDir, false, "implicit-dirs=false")
	})

	ginkgo.It(testNamePrefixSucceed+testNameExplicitDir+testNameSuffix(2), func() {
		if hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration test %v when HNS is enabled", testNameExplicitDir)
		}

		// passing only-dir flags
		init(specs.SubfolderInBucketPrefix)
		defer cleanup()

		gcsfuseIntegrationTest(testNameExplicitDir, false, "implicit-dirs=false")
	})

	ginkgo.It(testNamePrefixSucceed+testNameListLargeDir+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameListLargeDir, false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameReadLargeFiles+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameReadLargeFiles, false, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameWriteLargeFiles+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameWriteLargeFiles, false, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameWriteLargeFiles+testNameSuffix(2), func() {
		init()
		defer cleanup()

		v, err := version.ParseSemantic(gcsfuseVersionStr)
		// If error != nil, this means we've autogenerated a tag (meaning we run from HEAD)
		// Otherise, we have a valid tag, and we compare against the supported release.
		if (err != nil || v.AtLeast(version.MustParseSemantic("2.9.0"))) && getClientProtocol(driver) != "grpc" {
			gcsfuseIntegrationTest(testNameWriteLargeFiles, false, "enable-streaming-writes", "implicit-dirs=true")
		} else {
			e2eskipper.Skipf("skip gcsfuse integration test %v with enable-streaming-writes", testNameWriteLargeFiles)
		}
	})

	ginkgo.It(testNamePrefixSucceed+testNameGzip+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameGzip, false, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameLocalFile+testNameSuffix(1), func() {
		init()
		defer cleanup()
		v, err := version.ParseSemantic(gcsfuseVersionStr)

		// If error != nil, this means we've autogenerated a tag (meaning we run from HEAD)
		// Otherise, we have a valid tag, and we compare against the supported release.
		if err != nil || v.AtLeast(version.MustParseSemantic("3.0.0")) {
			ginkgo.By("Running test supported for gcsfuse v3.0.0+")
			// I needed to disable streaming writes the config file method, to
			// avoid gcsfuse exiting with Error: accepts between 2 and 3 arg(s), received 4.
			gcsfuseIntegrationTest(testNameLocalFile, false, "implicit-dirs=true", "rename-dir-limit=3", "write:enable-streaming-writes:false")
		} else {
			ginkgo.By("Running test supported before gcsfuse v3.0.0")
			gcsfuseIntegrationTest(testNameLocalFile, false, "implicit-dirs=true", "rename-dir-limit=3")
		}
	})

	ginkgo.It(testNamePrefixSucceed+testNameConcurrentOperations+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameConcurrentOperations, false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_AlwaysCacheHit", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(2), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_CacheMissOnAdditionOfFile", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(3), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_CacheMissOnDeletionOfFile", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(4), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_CacheMissOnFileRename", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(5), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_EvictCacheEntryOfOnlyDirectParent", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(6), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_CacheMissOnAdditionOfDirectory", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(7), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_CacheMissOnDeletionOfDirectory", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(8), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_CacheMissOnDirectoryRename", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(9), func() {
		init()
		defer cleanup()

		v, err := version.ParseSemantic(gcsfuseVersionStr)
		// If error != nil, this means we've autogenerated a tag (meaning we run from HEAD)
		// Otherise, we have a valid tag, and we compare against the supported release.
		if err != nil || v.AtLeast(version.MustParseSemantic("2.7.0")) {
			ginkgo.By("Running test supported for gcsfuse v2.7.0+")
			gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheDeleteDirTest/TestKernelListCache_ListAndDeleteDirectory", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1", "metadata-cache-ttl-secs=0")
		} else {
			ginkgo.By("Running test supported before gcsfuse v2.7.0")
			gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_ListAndDeleteDirectory", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
		}
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(10), func() {
		init()
		defer cleanup()

		v, err := version.ParseSemantic(gcsfuseVersionStr)
		// If error != nil, this means we've autogenerated a tag (meaning we run from HEAD)
		// Otherise, we have a valid tag, and we compare against the supported release.
		if err != nil || v.AtLeast(version.MustParseSemantic("2.7.0")) {
			ginkgo.By("Running test supported for gcsfuse v2.7.0+")
			gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheDeleteDirTest/TestKernelListCache_DeleteAndListDirectory", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1", "metadata-cache-ttl-secs=0")
		} else {
			ginkgo.By("Running test supported before gcsfuse v2.7.0")
			gcsfuseIntegrationTest(testNameKernelListCache+":TestInfiniteKernelListCacheTest/TestKernelListCache_DeleteAndListDirectory", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=-1")
		}
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(11), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestFiniteKernelListCacheTest/TestKernelListCache_CacheHitWithinLimit_CacheMissAfterLimit", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=5")
	})

	ginkgo.It(testNamePrefixSucceed+testNameKernelListCache+testNameSuffix(12), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameKernelListCache+":TestDisabledKernelListCacheTest/TestKernelListCache_AlwaysCacheMiss", false, "implicit-dirs=true", "kernel-list-cache-ttl-secs=0")
	})

	ginkgo.It(testNamePrefixSucceed+testNameManagedFolders+testNameSuffix(1), func() {
		init()
		defer cleanup()

		gcsfuseIntegrationTest(testNameManagedFolders+":TestEnableEmptyManagedFoldersTrue", false, "implicit-dirs=true")
	})

	ginkgo.It(testNamePrefixSucceed+testNameEnableStreamingWrites+testNameSuffix(1), func() {
		init()
		defer cleanup()
		if getClientProtocol(driver) == "grpc" {
			e2eskipper.Skipf("skip gcsfuse integration grpc test %v with enable-streaming-writes", testNameEnableStreamingWrites)
		} else {
			gcsfuseIntegrationTest(testNameEnableStreamingWrites, false, "rename-dir-limit=3", "implicit-dirs=true", "enable-streaming-writes", "write-block-size-mb=1", "write-max-blocks-per-file=2", "write-global-max-blocks=-1")
		}
	})

	ginkgo.It(testNamePrefixSucceed+testNameInactiveStreamTimeout+testNameSuffix(1), func() {
		init()
		defer cleanup()
		gcsfuseIntegrationTest(testNameInactiveStreamTimeout+":TestTimeoutDisabledSuite", false, "read-inactive-stream-timeout=0s", "logging:format:json", fmt.Sprintf("logging:file-path:%s", inactive_stream_timeout_log_file), "log-severity=trace")
	})

	ginkgo.It(testNamePrefixSucceed+testNameInactiveStreamTimeout+testNameSuffix(2), func() {
		init()
		defer cleanup()
		gcsfuseIntegrationTest(testNameInactiveStreamTimeout+":TestTimeoutEnabledSuite/TestReaderCloses", false, "read-inactive-stream-timeout=1s", "logging:format:json", fmt.Sprintf("logging:file-path:%s", inactive_stream_timeout_log_file), "log-severity=trace")
	})

	ginkgo.It(testNamePrefixSucceed+testNameInactiveStreamTimeout+testNameSuffix(3), func() {
		init()
		defer cleanup()
		gcsfuseIntegrationTest(testNameInactiveStreamTimeout+":TestTimeoutEnabledSuite/TestReaderStaysOpenWithinTimeout", false, "read-inactive-stream-timeout=1s", "logging:format:json", fmt.Sprintf("logging:file-path:%s", inactive_stream_timeout_log_file), "log-severity=trace")
	})
}
