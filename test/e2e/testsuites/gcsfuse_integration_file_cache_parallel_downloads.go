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
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIGCSFuseIntegrationFileCacheParallelDownloadsTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIGCSFuseIntegrationFileCacheParallelDownloadsTestSuite returns gcsFuseCSIGCSFuseIntegrationFileCacheParallelDownloadsTestSuite that implements TestSuite interface.
func InitGcsFuseCSIGCSFuseIntegrationFileCacheParallelDownloadsTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIGCSFuseIntegrationFileCacheParallelDownloadsTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "gcsfuseIntegrationFileCacheParallelDownloads",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheParallelDownloadsTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheParallelDownloadsTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheParallelDownloadsTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("gcsfuse-integration-file-cache-parallel", storageframework.GetDriverTimeouts(driver))
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

		// check if the given gcsfuse version supports the test case
		if !v.AtLeast(version.MustParseSemantic("v2.4.0-gke.0")) {
			e2eskipper.Skipf("skip gcsfuse integration test %v for gcsfuse version %v", testName, v.String())
		}

		// HNS is supported after v2.5.0
		if !v.AtLeast(version.MustParseSemantic("v2.5.0-gke.0")) && hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration HNS tests on gcsfuse version %v", v.String())
		}

		return fmt.Sprintf(gcsfuseReleaseBranchFormat, v.Major(), v.Minor(), v.Patch())
	}

	gcsfuseIntegrationFileCacheTest := func(testName string, readOnly bool, fileCacheCapacity, fileCacheForRangeRead, metadataCacheTTLSeconds string, mountOptions ...string) {
		ginkgo.By("Checking GCSFuse version and skip test if needed")
		if gcsfuseVersionStr == "" {
			gcsfuseVersionStr = specs.GetGCSFuseVersion(ctx, f)
		}
		ginkgo.By(fmt.Sprintf("Running integration test %v with GCSFuse version %v", testName, gcsfuseVersionStr))
		gcsfuseTestBranch := skipTestOrProceedWithBranch(gcsfuseVersionStr, testName)
		ginkgo.By(fmt.Sprintf("Running integration test %v with GCSFuse branch %v", testName, gcsfuseTestBranch))

		ginkgo.By("Configuring the test pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetImage(specs.GolangImage)
		tPod.SetCommand("tail -F /tmp/gcsfuse_read_cache_test_logs/log.json")
		tPod.SetResource("1", "1Gi", "5Gi")
		if strings.HasPrefix(testName, "TestRangeReadTest") {
			tPod.SetResource("1", "2Gi", "5Gi")
		}

		l.volumeResource.VolSource.CSI.VolumeAttributes["fileCacheCapacity"] = fileCacheCapacity
		l.volumeResource.VolSource.CSI.VolumeAttributes["fileCacheForRangeRead"] = fileCacheForRangeRead
		l.volumeResource.VolSource.CSI.VolumeAttributes["metadataCacheTTLSeconds"] = metadataCacheTTLSeconds

		tPod.SetupTmpVolumeMount("/tmp/gcsfuse_read_cache_test_logs")
		cacheDir := "cache-dir"
		gcsfuseVersion := version.MustParseSemantic(gcsfuseVersionStr)
		if gcsfuseTestBranch == masterBranchName || gcsfuseVersion.AtLeast(version.MustParseSemantic("v2.4.1-gke.0")) {
			if hnsEnabled(driver) {
				cacheDir = "cache-dir-read-cache-hns-true"
			} else {
				cacheDir = "cache-dir-read-cache-hns-false"
			}
		}
		tPod.SetupCacheVolumeMount("/tmp/"+cacheDir, ".volumes/"+volumeName)
		mountOptions = append(mountOptions, "logging:file-path:/gcsfuse-tmp/log.json", "logging:format:json", "logging:severity:trace")
		mountOptions = append(mountOptions,
			"file-cache:enable-parallel-downloads:true",
			"file-cache:parallel-downloads-per-file:4",
			"file-cache:max-parallel-downloads:-1",
			"file-cache:download-chunk-size-mb:3",
			"file-cache:enable-crc:true")

		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, readOnly, mountOptions...)
		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/cpu-limit":               "1",
			"gke-gcsfuse/memory-request":          defaultSidecarMemoryRequest,
			"gke-gcsfuse/memory-limit":            defaultSidecarMemoryLimit,
			"gke-gcsfuse/ephemeral-storage-limit": "2Gi",
		})

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]

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

		ginkgo.By("Checking that the gcsfuse integration tests exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("git clone --branch %v https://github.com/GoogleCloudPlatform/gcsfuse.git", gcsfuseTestBranch))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "ln -s /usr/bin/python3 /usr/bin/python")

		baseTestCommand := fmt.Sprintf("export GO_VERSION=$(%v) && export GOTOOLCHAIN=go$GO_VERSION && export PATH=$PATH:/usr/local/go/bin && cd %v/read_cache && GODEBUG=asyncpreemptoff=1 go test . -p 1 --integrationTest -v --mountedDirectory=%v --testbucket=%v -run %v", gcsfuseGoVersionCommand, gcsfuseIntegrationTestsBasePath, mountPath, bucketName, testName)
		if zbEnabled(driver) {
			baseTestCommand += " --zonal=true"
		}
		tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, baseTestCommand)
	}

	// The following test cases are derived from https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/tools/integration_tests/run_tests_mounted_directory.sh

	ginkgo.It("should succeed in TestCacheFileForRangeReadFalseTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestCacheFileForRangeReadFalseTest/TestRangeReadsWithCacheMiss", false, "50Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestCacheFileForRangeReadFalseTest 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestCacheFileForRangeReadFalseTest/TestConcurrentReads_ReadIsTreatedNonSequentialAfterFileIsRemovedFromCache", false, "50Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestCacheFileForRangeReadTrueTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestCacheFileForRangeReadTrueTest/TestRangeReadsWithCacheHit", false, "50Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestDisabledCacheTTLTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestDisabledCacheTTLTest/TestReadAfterObjectUpdateIsCacheMiss", false, "9Mi", "false", "0")
	})

	ginkgo.It("should succeed in TestLocalModificationTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestLocalModificationTest/TestReadAfterLocalGCSFuseWriteIsCacheMiss", false, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestRangeReadTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsBeyondReadChunkSizeWithChunkDownloaded", false, "500Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestRangeReadTest 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsBeyondReadChunkSizeWithChunkDownloaded", false, "500Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestSecondSequentialReadIsCacheHit", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadFileSequentiallyLargerThanCacheCapacity", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 3", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadFileRandomlyLargerThanCacheCapacity", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 4", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadMultipleFilesMoreThanCacheLimit", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 5", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadMultipleFilesWithinCacheLimit", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 6", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestSecondSequentialReadIsCacheHit", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 7", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadFileSequentiallyLargerThanCacheCapacity", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 8", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadFileRandomlyLargerThanCacheCapacity", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 9", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadMultipleFilesMoreThanCacheLimit", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 10", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadMultipleFilesWithinCacheLimit", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestSmallCacheTTLTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestSmallCacheTTLTest/TestReadAfterUpdateAndCacheExpiryIsCacheMiss", false, "9Mi", "false", "10")
	})

	ginkgo.It("should succeed in TestSmallCacheTTLTest 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestSmallCacheTTLTest/TestReadForLowMetaDataCacheTTLIsCacheHit", false, "9Mi", "false", "10")
	})
}
