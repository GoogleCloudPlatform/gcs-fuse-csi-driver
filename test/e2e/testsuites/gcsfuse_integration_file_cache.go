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
	"local/test/e2e/utils"

	"github.com/onsi/ginkgo/v2"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIGCSFuseIntegrationFileCacheTestSuite returns gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite that implements TestSuite interface.
func InitGcsFuseCSIGCSFuseIntegrationFileCacheTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "gcsfuseIntegrationFileCache",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("gcsfuse-integration-file-cache", storageframework.GetDriverTimeouts(driver))
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
		v, branch := utils.GCSFuseBranch(gcsfuseVersionStr)
		if branch == utils.MasterBranchName {
			return branch
		}

		// check if the given gcsfuse version supports the test case
		if !v.AtLeast(version.MustParseSemantic("v2.0.0-gke.0")) {
			e2eskipper.Skipf("skip gcsfuse integration test %v for gcsfuse version %v", testName, v.String())
		}

		// HNS is supported after v2.5.0
		if !v.AtLeast(version.MustParseSemantic("v2.5.0-gke.0")) && hnsEnabled(driver) {
			e2eskipper.Skipf("skip gcsfuse integration HNS tests on gcsfuse version %v", v.String())
		}

		return branch
	}

	gcsfuseIntegrationFileCacheTest := func(testName string, readOnly bool, fileCacheCapacity, fileCacheForRangeRead, metadataCacheTTLSeconds string, mountOptions ...string) {
		ginkgo.By("Checking GCSFuse version and skip test if needed")
		if GCSFuseVersionStr == "" {
			GCSFuseVersionStr = specs.GetGCSFuseVersion()
		}
		ginkgo.By(fmt.Sprintf("Running integration test %v with GCSFuse version %v", testName, GCSFuseVersionStr))
		gcsfuseTestBranch := skipTestOrProceedWithBranch(GCSFuseVersionStr, testName)
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
		gcsfuseVersion := version.MustParseSemantic(GCSFuseVersionStr)
		if gcsfuseTestBranch == utils.MasterBranchName || gcsfuseVersion.AtLeast(version.MustParseSemantic("v2.4.1-gke.0")) {
			if hnsEnabled(driver) {
				cacheDir = "cache-dir-read-cache-hns-true"
			} else {
				cacheDir = "cache-dir-read-cache-hns-false"
			}
		}
		tPod.SetupCacheVolumeMount("/tmp/"+cacheDir, ".volumes/"+volumeName)
		mountOptions = append(mountOptions, "logging:file-path:/gcsfuse-tmp/log.json", "logging:format:json", "logging:severity:trace")

		kernelParamsSupported := gcsfuseTestBranch == utils.MasterBranchName || gcsfuseVersion.AtLeast(version.MustParseSemantic(utils.MinGCSFuseKernelParamsVersion))
		if kernelParamsSupported {
			mountOptions = append(mountOptions, "file-system:enable-kernel-reader:false")
		}

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
		installGcsfuseDependencies(tPod, f, gcsfuseTestBranch, false)

		gcsfuseGoVersionCommand := getGoParsingCommand(*gcsfuseVersion, gcsfuseTestBranch)

		baseTestCommand := fmt.Sprintf(gcsfuseGoEnvSetupFormat+" && cd %v/read_cache && GODEBUG=asyncpreemptoff=1 go test . -p 1 --integrationTest -v --mountedDirectory=%v --testbucket=%v -run %v", gcsfuseGoVersionCommand, gcsfuseIntegrationTestsBasePath, mountPath, bucketName, testName)
		if zbEnabled(driver) {
			baseTestCommand += " --zonal=true"
		}
		tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, baseTestCommand)
	}

	gcsfuseIntegrationFileCacheTestNew := func(testPkg string, testName string, config utils.ParsedConfig, secondaryConfig *utils.ParsedConfig) {
		fullTestName := testPkg
		if testName != "" {
			fullTestName = fmt.Sprintf("%s/%s", testPkg, testName)
		}

		ginkgo.By("Checking GCSFuse version and skip test if needed")
		ginkgo.By(fmt.Sprintf("Running integration test %v with GCSFuse version %v", fullTestName, GCSFuseVersionStr))
		gcsfuseTestBranch := skipTestOrProceedWithBranch(GCSFuseVersionStr, fullTestName)
		ginkgo.By(fmt.Sprintf("Running integration test %v with GCSFuse branch %v", fullTestName, gcsfuseTestBranch))

		ginkgo.By("Configuring the test pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetImage(specs.GolangImage)

		if config.LogFilePath != "" {
			framework.Logf("Log file path: %v", config.LogFilePath)
			tPod.SetCommand(fmt.Sprintf("tail -F %v", config.LogFilePath))
		}

		tPod.SetResource("1", "1Gi", "5Gi")
		if strings.HasPrefix(testName, "TestRangeReadTest") {
			tPod.SetResource("1", "2Gi", "5Gi")
		}

		sidecarMemoryRequest, sidecarMemoryLimit := configureLargeFileResources(tPod, testPkg, driver)

		// By setting up the cache volume mount here,the sidecar-mounter will automatically populate
		// the "cache-dir" in its config file map when file cache is enabled.
		l.volumeResource.VolSource.CSI.VolumeAttributes["fileCacheCapacity"] = config.FileCacheCapacity
		tPod.SetupTmpVolumeMount(gkeTempDir)
		framework.Logf("Cache file path: %v", config.CacheDir)
		tPod.SetupCacheVolumeMount(config.CacheDir, ".volumes/"+volumeName)

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]

		if config.LogSeverity != "" {
			// Replaced hardcoded logging:severity:info from testdriver set up with parsed log severity
			mo := l.volumeResource.VolSource.CSI.VolumeAttributes["mountOptions"]
			mo = strings.ReplaceAll(mo, "logging:severity:info", fmt.Sprintf("logging:severity:%v", config.LogSeverity))
			l.volumeResource.VolSource.CSI.VolumeAttributes["mountOptions"] = mo
		}

		// Expand variables in mount options so that flags like --file-cache-exclude-regex=^${BUCKET_NAME}/
		// can be dynamically referenced.
		vars := map[string]string{"BUCKET_NAME": bucketName}
		for i, opt := range config.MountOptions {
			config.MountOptions[i] = utils.ExpandFlagVariables(opt, vars)
		}
		framework.Logf("Final parsed arguments: %v", config.MountOptions)

		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, config.ReadOnly, config.MountOptions...)

		if secondaryConfig != nil {
			framework.Logf("Setting up secondary volume mount")
			for i, opt := range secondaryConfig.MountOptions {
				secondaryConfig.MountOptions[i] = utils.ExpandFlagVariables(opt, vars)
			}
			framework.Logf("Secondary parsed arguments: %v", secondaryConfig.MountOptions)

			// Deep copy the volume resource for dual mounting. We cannot pass l.volumeResource directly
			// because modifying its underlying dictionary (VolumeAttributes) with secondary configuration
			// would inadvertently overwrite the mount options of the primary volume.
			secondaryVolumeResource := *l.volumeResource
			if l.volumeResource.VolSource != nil {
				secondaryVolumeResource.VolSource = l.volumeResource.VolSource.DeepCopy()
			}
			tPod.SetupVolume(&secondaryVolumeResource, volumeName2, mountPath2, secondaryConfig.ReadOnly, secondaryConfig.MountOptions...)
		}

		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/cpu-limit":               "1",
			"gke-gcsfuse/memory-request":          sidecarMemoryRequest,
			"gke-gcsfuse/memory-limit":            sidecarMemoryLimit,
			"gke-gcsfuse/ephemeral-storage-limit": "2Gi",
		})

		onlyDir := utils.ExtractOnlyDirFromMountOptions(l.volumeResource.VolSource.CSI.VolumeAttributes["mountOptions"])

		ginkgo.By("Deploying the test pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the test pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the test pod command exits with no error")
		if config.ReadOnly {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep ro,", mountPath))
		} else {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		}

		if secondaryConfig != nil {
			if secondaryConfig.ReadOnly {
				tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep ro,", mountPath2))
			} else {
				tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath2))
			}
		}

		ginkgo.By("Checking that the gcsfuse integration tests exits with no error")
		installGcsfuseDependencies(tPod, f, gcsfuseTestBranch, false)

		gcsfuseVersion := version.MustParseSemantic(GCSFuseVersionStr)
		gcsfuseGoVersionCommand := getGoParsingCommand(*gcsfuseVersion, gcsfuseTestBranch)

		cmdOpts := TestCommandConfig{
			TestPkg:       testPkg,
			TestName:      testName,
			GoEnvSetupCmd: gcsfuseGoVersionCommand,
			MountPath:     mountPath,
			BucketName:    bucketName,
			OnlyDir:       onlyDir,
		}
		if secondaryConfig != nil {
			cmdOpts.SecondaryMountPath = mountPath2
		}

		baseTestCommand := generateTestCommand(cmdOpts)
		framework.Logf("Executing tests with command:\n%s", baseTestCommand)
		tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, baseTestCommand)
	}

	generateDynamicTests := func(configVersion string) {
		framework.Logf("Generating dynamic tests for file cache with config version: %s", configVersion)
		if utils.IsReadFromTestConfig(configVersion) && len(utils.LoadedTestPackages) == 0 {
			framework.Logf("Loading test config for GCSFuse version %v", configVersion)
			err := utils.LoadTestConfig(configVersion)
			if err != nil {
				framework.Failf("Failed to load test config: %v", err)
			}
		}
		// Dynamically generate tests from test_config.yaml in GCSFuse
		for pkgName, pkgList := range utils.LoadedTestPackages {
			// The YAML parser treats test package as a list because of the '-' syntax.
			// But there is only one configuration item under each package, so we take the first element.
			pkg := pkgList[0]
			for _, config := range pkg.Configs {
				if !isConfigCompatible(config, driver) {
					continue
				}

				for i, flagStr := range config.Flags {
					// TODO: Remove this once b/494403195 got fixed
					if strings.Contains(flagStr, "prometheus-port") {
						continue
					}
					if !utils.IsFileCacheEnabled(flagStr) {
						continue
					}
					// We only generate non-parallel download tests here.
					if utils.IsParallelDownloadsEnabled(flagStr) {
						continue
					}

					testName := config.Run
					fullTestName := pkgName
					if testName != "" {
						fullTestName = fmt.Sprintf("%s/%s", pkgName, testName)
					}

					ginkgo.It(fmt.Sprintf("should succeed in %s with flags %s", fullTestName, flagStr), func() {
						ginkgo.By(fmt.Sprintf("Starting file cache test: %s", fullTestName))
						framework.Logf("Original flag string from config: %s", flagStr)

						if utils.IsOnlyDirEnabled(pkg) {
							ginkgo.By("Configuring test with only_dir enabled")
							init(specs.SubfolderInBucketPrefix)
						} else {
							init()
						}
						defer cleanup()

						// TODO: Remove this once b/494320769 got fixed.
						parsedFlags := utils.ParseConfigFlags(flagStr)

						var secondaryParsedFlags *utils.ParsedConfig
						if i < len(config.SecondaryFlags) {
							secFlags := utils.ParseConfigFlags(config.SecondaryFlags[i])
							secondaryParsedFlags = &secFlags
						}

						if parsedFlags.CacheDir == "${CACHE_DIR_PATH}" {
							dirName := pkgName
							if testName != "" {
								dirName = testName
							}
							parsedFlags.CacheDir = "/gcsfuse-tmp/" + dirName
						}

						//TODO: Remove this once b/494350392 got fixed.
						if pkgName == "monitoring" && testName != "" {
							logFileName := "/gcsfuse-tmp/" + testName + ".log"
							parsedFlags.LogFilePath = logFileName
							for j, opt := range parsedFlags.MountOptions {
								if strings.HasPrefix(opt, "logging:file-path:") {
									parsedFlags.MountOptions[j] = "logging:file-path:" + logFileName
									break
								}
							}
						}

						framework.Logf("Parsed arguments: %+v", parsedFlags)

						gcsfuseIntegrationFileCacheTestNew(pkgName, testName, parsedFlags, secondaryParsedFlags)
					})
				}
			}
		}
	}

	generateStaticTests := func() {
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

			gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsWithinReadChunkSize", false, "500Mi", "false", "3600")
		})

		ginkgo.It("should succeed in TestRangeReadTest 2", func() {
			init()
			defer cleanup()

			gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsBeyondReadChunkSizeWithChunkDownloaded", false, "500Mi", "false", "3600")
		})

		ginkgo.It("should succeed in TestRangeReadTest 3", func() {
			init()
			defer cleanup()

			gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsWithinReadChunkSize", false, "500Mi", "true", "3600")
		})

		ginkgo.It("should succeed in TestRangeReadTest 4", func() {
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

	framework.Logf("Generating tests based on test config")

	// The gcsfuse test_config.yaml is introduced from the gcsfuse v3.7+.
	// If the gcsfuse version is less than v3.7+, we will use the static tests.
	// We will remove the static tests in the future.
	if utils.IsReadFromTestConfig(GCSFuseVersionStr) {
		klog.Info("Generating tests based on test config")
		generateDynamicTests(GCSFuseVersionStr)
	} else {
		klog.Info("Generating static tests")
		generateStaticTests()
	}
}
