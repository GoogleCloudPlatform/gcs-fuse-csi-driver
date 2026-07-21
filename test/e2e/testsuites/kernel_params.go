/*
Copyright 2026 Google LLC

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
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"local/test/e2e/specs"
	"local/test/e2e/utils"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type ParamName string

const (
	MaxReadAheadKb                ParamName = "max-read-ahead-kb"
	MaxBackgroundRequests         ParamName = "fuse-max-background-requests"
	CongestionWindowThreshold     ParamName = "fuse-congestion-window-threshold"
	CustomCSIReadAhead3MiB                  = "3072"
	CustomGCSFuseMaxReadAhead6MiB           = "6144"
	CustomGCSFuseMaxReadAhead8MiB           = "8192"
	CustomMaxBackground                     = "999"
	CustomCongestionThreshold               = "666"
	tmpMountPath                            = "/gcsfuse-tmp"

	testFileSizeMiB       = 16
	testFileSize32MiB     = 32
	requestSize32MiBInKiB = 32 * util.KiB
	chunkSize1MiBBytes    = 1 * util.MiB
	expectedChunkCount    = 16
)

type gcsFuseCSIKernelParamsTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseKernelParamsTestSuite returns gcsFuseCSIKernelParamsTestSuite that implements TestSuite interface.
func InitGcsFuseKernelParamsTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIKernelParamsTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "kernelParams",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIKernelParamsTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIKernelParamsTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func setupAndDeployTestPod(ctx context.Context, f *framework.Framework, volumeResource *storageframework.VolumeResource, needsFuseConnections bool, mountOptions ...string) *specs.TestPod {
	tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
	tPod.SetupVolume(volumeResource, volumeName, mountPath, false, mountOptions...)
	tPod.SetupTmpVolumeMount(tmpMountPath)
	if needsFuseConnections {
		// Setup HostPath for FUSE connections
		fuseControlVol := &storageframework.VolumeResource{
			VolSource: &corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/sys/fs/fuse/connections"}},
		}
		tPod.SetupVolume(fuseControlVol, "fuse-control", "/sys/fs/fuse/connections", false)
	}

	ginkgo.By("Deploying the pod")
	tPod.Create(ctx)

	ginkgo.By("Checking that the pod is running")
	tPod.WaitForRunning(ctx)

	ginkgo.By("Checking that the pod command exits with no error")
	tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mountpoint %q", mountPath))

	return tPod
}

func kernelParamsConfigFilePath(volumeName string) string {
	return fmt.Sprintf("%s/.volumes/%s/kernel-params.json", tmpMountPath, volumeName)
}

// kernelParamValueFromConfigFile reads the kernel parameters configuration file,
// which is expected to be in JSON format,It uses a regular expression to find the
// parameter by name and return its value. If the parameter is not found, it returns an empty string.
// parameter. The JSON file is expected to have a structure similar to the following:
//
//	{
//	  "request_id": "10c8148f-b26f-4ba4-9d09-e854311eb5d6",
//	  "timestamp": "2026-02-04T02:26:45.622211445Z",
//	  "parameters": [
//	    {
//	      "name": "max-read-ahead-kb",
//	      "value": "16384"
//	    },
//	    {
//	      "name": "fuse-congestion-window-threshold",
//	      "value": "12"
//	    },
//	    {
//	      "name": "fuse-max-background-requests",
//	      "value": "16"
//	    }
//	  ]
//	}
const kernelParamExtractRegex = `"name"\s*:\s*"%s".*?"value"\s*:\s*"(.*?)"`

func skipIfKernelParamsNotSupported() {
	gcsfuseVersion, branch := specs.GCSFuseVersionAndBranch()

	// Running since we are on master branch.
	if branch == utils.MasterBranchName {
		return
	}
	// Kernel parameters were introduced in v3.7.0-gke.0.
	if !gcsfuseVersion.AtLeast(version.MustParseSemantic(utils.MinGCSFuseKernelParamsVersion)) {
		e2eskipper.Skipf("skip kernel params test for unsupported gcsfuse version %s", gcsfuseVersion.String())
	}
}

func skipIfFuseMaxRequestSizeNotSupported() {
	gcsfuseVersion, branch := specs.GCSFuseVersionAndBranch()

	// Running since we are on master branch.
	if branch == utils.MasterBranchName {
		return
	}
	// fuse-max-request-size-kb was introduced in v3.11.1-gke.0.
	if !gcsfuseVersion.AtLeast(version.MustParseSemantic(utils.MinGCSFuseFuseMaxRequestSizeVersion)) {
		e2eskipper.Skipf("skip fuse max request size test for unsupported gcsfuse version %s", gcsfuseVersion.String())
	}
}

func kernelParamValueFromConfigFile(f *framework.Framework, tPod *specs.TestPod, volumeName string, paramName ParamName) string {
	configFileData := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf("cat %s", kernelParamsConfigFilePath(volumeName)))
	pattern := fmt.Sprintf(kernelParamExtractRegex, regexp.QuoteMeta(string(paramName)))
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(configFileData)

	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func updateKernelParamValueInConfigFileAtomically(f *framework.Framework, tPod *specs.TestPod, paramName ParamName, volumeName, newValue string) {
	configFilePath := kernelParamsConfigFilePath(volumeName)
	originalContent := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf("cat %s", configFilePath))

	// Using a regular expression to replace the value of the specified parameter.
	// This approach is chosen to be resilient to formatting changes in the JSON file.
	re := regexp.MustCompile(fmt.Sprintf(`("name"\s*:\s*"%s".*?"value"\s*:\s*")[^"]*(")`, regexp.QuoteMeta(string(paramName))))
	newContent := re.ReplaceAllString(originalContent, fmt.Sprintf("${1}%s${2}", newValue))

	tmpConfigFile := fmt.Sprintf("%s-tmp", configFilePath)
	// Write the updated content to a temporary file in the pod.
	// Using a heredoc to avoid issues with special characters in the content.
	tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("cat <<'EOF' > %s\n%s\nEOF", tmpConfigFile, newContent))

	// Atomically move the temporary file to the original file path.
	tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mv %s %s", tmpConfigFile, configFilePath))
}

func KernelParamValueFromMount(f *framework.Framework, tPod *specs.TestPod, paramName ParamName) string {
	bdi := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf(`mountpoint -d "%s"`, mountPath))
	parts := strings.Split(strings.TrimSpace(bdi), ":")
	major := parts[0]
	minor := parts[1]
	pathForParam := map[ParamName]string{
		MaxReadAheadKb:            fmt.Sprintf("/sys/class/bdi/%s:%s/read_ahead_kb", major, minor),
		MaxBackgroundRequests:     fmt.Sprintf("/sys/fs/fuse/connections/%s/max_background", minor),
		CongestionWindowThreshold: fmt.Sprintf("/sys/fs/fuse/connections/%s/congestion_threshold", minor),
	}
	sysfsPath, ok := pathForParam[paramName]
	if !ok {
		return ""
	}
	return strings.TrimSpace(tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf(`cat "%s"`, sysfsPath)))
}

func (t *gcsFuseCSIKernelParamsTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("kernel-params", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		skipIfKernelParamsNotSupported()

		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		l.config.Prefix = configPrefix[0]
		l.volumeResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	ginkgo.It("should verify kernel params config file is written for zonal bucket", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && !driver.EnableZB {
			e2eskipper.Skipf("skip for regional bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		defer cleanup()

		ginkgo.By("Configuring and setting up test pod")
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, false /* needsFuseConnections */)

		ginkgo.By("Verifying kernel params config file is present")
		tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, fmt.Sprintf("cat %s", kernelParamsConfigFilePath(volumeName)))

		ginkgo.By("Deleting the pod")
		tPod.Cleanup(ctx)
	})

	ginkgo.It("should verify that CSI read_ahead_kb is overwritten by GCSFuse default setting for zonal bucket", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && !driver.EnableZB {
			e2eskipper.Skipf("skip for regional bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		defer cleanup()

		ginkgo.By("Configuring and setting up test pod")
		// This read_ahead_kb will be overwritten by GCSFuse default readAheadKb in config file.
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, false /* needsFuseConnections */, fmt.Sprintf("read_ahead_kb=%s", CustomCSIReadAhead3MiB))

		ginkgo.By("Verifying read_ahead_kb is eventually overwritten by GCSFuse default value")
		expectedValue := kernelParamValueFromConfigFile(f, tPod, volumeName, MaxReadAheadKb)
		gomega.Eventually(func() string {
			return KernelParamValueFromMount(f, tPod, MaxReadAheadKb)
		}, retryTimeout, retryPolling).Should(gomega.Equal(expectedValue))

		ginkgo.By("Verify actual value of ReadAheadKb is not equal to CSI read_ahead_kb value")
		actualValue := KernelParamValueFromMount(f, tPod, MaxReadAheadKb)
		gomega.Expect(actualValue).ShouldNot(gomega.Equal(CustomCSIReadAhead3MiB))

		ginkgo.By("Deleting the pod")
		tPod.Cleanup(ctx)
	})

	ginkgo.It("should verify that readAheadKb is overwritten by GCSFuse user provided value for zonal bucket", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && !driver.EnableZB {
			e2eskipper.Skipf("skip for regional bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		defer cleanup()

		ginkgo.By("Configuring and setting up test pod")
		// This read_ahead_kb will be overwritten by user provided GCSFuse max-read-ahead-kb in config file.
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, false /* needsFuseConnections */, fmt.Sprintf("read_ahead_kb=%s,file-system:max-read-ahead-kb:%s", CustomCSIReadAhead3MiB, CustomGCSFuseMaxReadAhead6MiB))

		ginkgo.By("Verifying ReadAheadkb is eventually overwritten by user provided value of GCSFuse max-read-ahead-kb")
		expectedValue := kernelParamValueFromConfigFile(f, tPod, volumeName, MaxReadAheadKb)
		gomega.Expect(CustomGCSFuseMaxReadAhead6MiB).Should(gomega.Equal(expectedValue))
		gomega.Eventually(func() string {
			return KernelParamValueFromMount(f, tPod, MaxReadAheadKb)
		}, retryTimeout, retryPolling).Should(gomega.Equal(expectedValue))

		ginkgo.By("Deleting the pod")
		tPod.Cleanup(ctx)
	})

	ginkgo.It("should verify that max-background and congestion-threshold are changed to GCSFuse default setting for zonal bucket", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && !driver.EnableZB {
			e2eskipper.Skipf("skip for regional bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		defer cleanup()

		ginkgo.By("Configuring and setting up test pod")
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, true /* needsFuseConnections */)

		ginkgo.By("Verifying max-background is eventually changed to GCSFuse default value")
		expectedMaxBackground := kernelParamValueFromConfigFile(f, tPod, volumeName, MaxBackgroundRequests)
		gomega.Eventually(func() string {
			return KernelParamValueFromMount(f, tPod, MaxBackgroundRequests)
		}, retryTimeout, retryPolling).Should(gomega.Equal(expectedMaxBackground))

		ginkgo.By("Verifying congestion-threshold is eventually changed to GCSFuse default value")
		expectedCongestionThreshold := kernelParamValueFromConfigFile(f, tPod, volumeName, CongestionWindowThreshold)
		gomega.Eventually(func() string {
			return KernelParamValueFromMount(f, tPod, CongestionWindowThreshold)
		}, retryTimeout, retryPolling).Should(gomega.Equal(expectedCongestionThreshold))

		ginkgo.By("Deleting the pod")
		tPod.Cleanup(ctx)
	})

	ginkgo.It("should verify that max-background and congestion-threshold are changed to user provided value for zonal bucket", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && !driver.EnableZB {
			e2eskipper.Skipf("skip for regional bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		defer cleanup()

		ginkgo.By("Configuring and setting up test pod")
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, true /* needsFuseConnections */, fmt.Sprintf("file-system:max-background:%s,file-system:congestion-threshold:%s", CustomMaxBackground, CustomCongestionThreshold))

		ginkgo.By("Verifying max-background is eventually changed to user provided value")
		expectedMaxBackground := kernelParamValueFromConfigFile(f, tPod, volumeName, MaxBackgroundRequests)
		gomega.Expect(CustomMaxBackground).Should(gomega.Equal(expectedMaxBackground))
		gomega.Eventually(func() string {
			return KernelParamValueFromMount(f, tPod, MaxBackgroundRequests)
		}, retryTimeout, retryPolling).Should(gomega.Equal(expectedMaxBackground))

		ginkgo.By("Verifying congestion-threshold is eventually changed to user provided value")
		expectedCongestionThreshold := kernelParamValueFromConfigFile(f, tPod, volumeName, CongestionWindowThreshold)
		gomega.Expect(CustomCongestionThreshold).Should(gomega.Equal(expectedCongestionThreshold))
		gomega.Eventually(func() string {
			return KernelParamValueFromMount(f, tPod, CongestionWindowThreshold)
		}, retryTimeout, retryPolling).Should(gomega.Equal(expectedCongestionThreshold))

		ginkgo.By("Deleting the pod")
		tPod.Cleanup(ctx)
	})

	ginkgo.DescribeTable("should verify that kernel params are continuously updated if config file is changed for zonal bucket",
		func(paramName ParamName, newValue string, needsFuseConnections bool) {
			if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && !driver.EnableZB {
				e2eskipper.Skipf("skip for regional bucket")
			}
			init(specs.EnableKernelParamsPrefix)
			defer cleanup()

			ginkgo.By("Configuring and setting up test pod")
			tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, true /* needsFuseConnections */)

			ginkgo.By(fmt.Sprintf("Verifying %s is eventually changed to GCSFuse default value", paramName))
			expectedDefaultValue := kernelParamValueFromConfigFile(f, tPod, volumeName, paramName)
			gomega.Eventually(func() string {
				return KernelParamValueFromMount(f, tPod, paramName)
			}, retryTimeout, retryPolling).Should(gomega.Equal(expectedDefaultValue))

			ginkgo.By(fmt.Sprintf("Verifying %s is eventually changed to new value upon config file change", paramName))
			updateKernelParamValueInConfigFileAtomically(f, tPod, paramName, volumeName, newValue)
			gomega.Eventually(func() string {
				return KernelParamValueFromMount(f, tPod, paramName)
			}, retryTimeout, retryPolling).Should(gomega.Equal(newValue))

			ginkgo.By("Deleting the pod")
			tPod.Cleanup(ctx)
		},
		ginkgo.Entry("for read-ahead-kb", MaxReadAheadKb, CustomGCSFuseMaxReadAhead8MiB, false),
		ginkgo.Entry("for max-background", MaxBackgroundRequests, CustomMaxBackground, true),
		ginkgo.Entry("for congestion-threshold", CongestionWindowThreshold, CustomCongestionThreshold, true),
	)

	ginkgo.It("should verify kernel params config file is not written for regional bucket", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && driver.EnableZB {
			e2eskipper.Skipf("skip for zonal bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		defer cleanup()

		ginkgo.By("Configuring and setting up test pod")
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, false /* needsFuseConnections */)

		ginkgo.By("Verifying kernel params config file is not written")
		tPod.VerifyExecInPodFail(f, specs.TesterContainerName, fmt.Sprintf("cat %s", kernelParamsConfigFilePath(volumeName)), 1)

		ginkgo.By("Deleting the pod")
		tPod.Cleanup(ctx)
	})

	ginkgo.It("should verify node_fuse_max_request_limit_kb=0 disables higher file I/O", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && driver.EnableZB {
			e2eskipper.Skipf("skip for zonal bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		skipIfFuseMaxRequestSizeNotSupported()
		defer cleanup()

		ginkgo.By("Configuring and setting up test pod with trace logging")
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, false /* needsFuseConnections */, "enable-kernel-reader=true,node_fuse_max_request_limit_kb=0,logging:severity:trace")
		defer tPod.Cleanup(ctx)

		// We use direct-io=true to bypass the page cache, allowing us to verify file I/O correctly.
		ginkgo.By("Write a 16MB file in single block and read in single block")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=/dev/zero of=%s/testfile bs=%dM count=1 oflag=direct", mountPath, testFileSizeMiB))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=%s/testfile of=/dev/null bs=%dM count=1 iflag=direct", mountPath, testFileSizeMiB))

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		ginkgo.By("Verifying write I/O is present in sidecar logs in 1MiB blocks")
		assertFuseIO(tPod, "write", chunkSize1MiBBytes, expectedChunkCount, bucketName)

		ginkgo.By("Verifying read I/O is present in sidecar logs in 1MiB blocks")
		assertFuseIO(tPod, "read", chunkSize1MiBBytes, expectedChunkCount, bucketName)
	})

	ginkgo.It("should verify enabling kernel reader results in 16MiB readFile I/O", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && driver.EnableZB {
			e2eskipper.Skipf("skip for zonal bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		defer cleanup()

		ginkgo.By("Configuring and setting up test pod with trace logging")
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, false /* needsFuseConnections */, "enable-kernel-reader=true,logging:severity:trace")
		defer tPod.Cleanup(ctx)

		// We use direct-io=true to bypass the page cache, allowing us to verify file I/O correctly.
		ginkgo.By("Write a 16MB file in a single block and read in a single block")
		targetSize := int64(testFileSizeMiB * chunkSize1MiBBytes)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=/dev/zero of=%s/testfile bs=%d count=1 oflag=direct", mountPath, targetSize))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=%s/testfile of=/dev/null bs=%d count=1 iflag=direct", mountPath, targetSize))

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		ginkgo.By("Verifying write I/O is present in sidecar logs in 1MiB blocks")
		assertFuseIO(tPod, "write", chunkSize1MiBBytes, expectedChunkCount, bucketName)

		ginkgo.By("Verifying read I/O is present in sidecar logs in a single 16MiB block")
		assertFuseIO(tPod, "read", targetSize, 1, bucketName)
	})

	ginkgo.It("should verify enabling kernel reader with non-default setting results in 32MiB read and write file I/O", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && driver.EnableZB {
			e2eskipper.Skipf("skip for zonal bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		skipIfFuseMaxRequestSizeNotSupported()
		defer cleanup()

		ginkgo.By("Configuring and deploying test pod with 32MiB limits and trace logging")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false,
			fmt.Sprintf("enable-kernel-reader=true,node_fuse_max_request_limit_kb=%d,file-system:fuse-max-request-size-kb:%d,logging:severity:trace", requestSize32MiBInKiB, requestSize32MiBInKiB))
		tPod.SetupTmpVolumeMount(tmpMountPath)
		tPod.SetResource("100m", "50Mi", "100Mi")
		defer tPod.Cleanup(ctx)

		tPod.Create(ctx)
		tPod.WaitForRunning(ctx)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mountpoint %q", mountPath))

		// We use direct-io=true to bypass the page cache, allowing us to verify file I/O correctly.
		ginkgo.By("Write a 32MB file in a single block and read in a single block")
		targetSize := int64(testFileSize32MiB * chunkSize1MiBBytes)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=/dev/zero of=%s/testfile bs=%d count=1 oflag=direct", mountPath, targetSize))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=%s/testfile of=/dev/null bs=%d count=1 iflag=direct", mountPath, targetSize))

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		ginkgo.By("Verifying write I/O is present in sidecar logs in 1MiB blocks")
		assertFuseIO(tPod, "write", chunkSize1MiBBytes, testFileSize32MiB, bucketName)

		ginkgo.By("Verifying read I/O is present in sidecar logs in a single 32MiB block")
		assertFuseIO(tPod, "read", targetSize, 1, bucketName)
	})

	ginkgo.It("should verify enabling kernel reader on multiple volumes inside a single pod yields independent 16MiB and 32MiB block file I/O", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && driver.EnableZB {
			e2eskipper.Skipf("skip for zonal bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		skipIfFuseMaxRequestSizeNotSupported()
		defer cleanup()

		ginkgo.By("Creating the second volume resource (second bucket)")
		l.config.Prefix = specs.EnableKernelParamsPrefix
		volumeResource2 := storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
		defer func() {
			if volumeResource2 != nil {
				framework.ExpectNoError(volumeResource2.CleanupResource(ctx))
			}
		}()

		ginkgo.By("Configuring and deploying a multi-volume test pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "vol-16mib", "/mnt/test-16mib", false,
			"enable-kernel-reader=true,logging:severity:trace")
		tPod.SetupVolume(volumeResource2, "vol-32mib", "/mnt/test-32mib", false,
			fmt.Sprintf("enable-kernel-reader=true,node_fuse_max_request_limit_kb=%d,file-system:fuse-max-request-size-kb:%d,logging:severity:trace", requestSize32MiBInKiB, requestSize32MiBInKiB))
		tPod.SetupTmpVolumeMount(tmpMountPath)
		tPod.SetResource("100m", "50Mi", "100Mi")
		defer tPod.Cleanup(ctx)

		tPod.Create(ctx)
		tPod.WaitForRunning(ctx)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "mountpoint '/mnt/test-16mib'")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "mountpoint '/mnt/test-32mib'")

		// We use direct-io=true to bypass the page cache, allowing us to verify file I/O correctly.
		ginkgo.By("Write 16MiB to Volume 1 (vol-16mib) and verify write and read logs")
		targetSize16 := int64(testFileSizeMiB * chunkSize1MiBBytes)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=/dev/zero of=/mnt/test-16mib/testfile bs=%d count=1 oflag=direct", targetSize16))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=/mnt/test-16mib/testfile of=/dev/null bs=%d count=1 iflag=direct", targetSize16))

		ginkgo.By("Write 32MiB to Volume 2 (vol-32mib) and verify write and read logs")
		targetSize32 := int64(testFileSize32MiB * chunkSize1MiBBytes)
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=/dev/zero of=/mnt/test-32mib/testfile bs=%d count=1 oflag=direct", targetSize32))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("dd if=/mnt/test-32mib/testfile of=/dev/null bs=%d count=1 iflag=direct", targetSize32))

		bucketName16 := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		bucketName32 := volumeResource2.VolSource.CSI.VolumeAttributes["bucketName"]

		ginkgo.By("Verifying Volume 1 (16MiB) I/O traces in sidecar logs")
		assertFuseIO(tPod, "write", chunkSize1MiBBytes, expectedChunkCount, bucketName16)
		assertFuseIO(tPod, "read", targetSize16, 1, bucketName16)

		ginkgo.By("Verifying Volume 2 (32MiB) I/O traces in sidecar logs")
		assertFuseIO(tPod, "write", chunkSize1MiBBytes, testFileSize32MiB, bucketName32)
		assertFuseIO(tPod, "read", targetSize32, 1, bucketName32)
	})

	ginkgo.It("should verify that once the GCSFuse mount is done the FUSE max_pages_limit on the host is reverted back to its original default value", func() {
		if driver, ok := driver.(*specs.GCSFuseCSITestDriver); ok && driver.EnableZB {
			e2eskipper.Skipf("skip for zonal bucket")
		}
		init(specs.EnableKernelParamsPrefix)
		defer cleanup()

		driverNamespace := "gcs-fuse-csi-driver"
		if os.Getenv(specs.IsOSSEnvVar) != "true" {
			driverNamespace = "kube-system"
		}

		ginkgo.By("Listing CSI driver pods to select a node")
		pods, err := f.ClientSet.CoreV1().Pods(driverNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "k8s-app=gcs-fuse-csi-driver",
		})
		framework.ExpectNoError(err)
		gomega.Expect(pods.Items).ToNot(gomega.BeEmpty())

		// Pick the first driver pod to pin our test pods to its node
		driverPod := pods.Items[0]
		nodeName := driverPod.Spec.NodeName

		ginkgo.By("Configuring a host-reader helper pod pinned to the selected node")
		helperPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		hostFuseVol := &storageframework.VolumeResource{
			VolSource: &corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/proc/sys/fs/fuse",
				},
			},
		}
		helperPod.SetupVolume(hostFuseVol, "host-fuse", "/host-proc-sys-fs-fuse", false)
		helperPod.SetNodeAffinity(nodeName, true)

		ginkgo.By("Deploying the host-reader helper pod")
		helperPod.Create(ctx)
		defer helperPod.Cleanup(ctx)
		helperPod.WaitForRunning(ctx)

		ginkgo.By("Reading the initial FUSE max_pages_limit on the host via the helper pod")
		limitStr := helperPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, "cat /host-proc-sys-fs-fuse/max_pages_limit")
		initialLimit, err := strconv.ParseInt(strings.TrimSpace(limitStr), 10, 64)
		framework.ExpectNoError(err)

		ginkgo.By("Configuring the GCSFuse test pod with GCSFuse volume and node affinity")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false, "enable-kernel-reader=true")
		tPod.SetupTmpVolumeMount(tmpMountPath)
		tPod.SetNodeAffinity(nodeName, true)

		ginkgo.By("Deploying the GCSFuse test pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the GCSFuse mount is active")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mountpoint %q", mountPath))

		ginkgo.By("Reading the FUSE max_pages_limit on the host again via the helper pod")
		limitStr = helperPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, "cat /host-proc-sys-fs-fuse/max_pages_limit")
		currentLimit, err := strconv.ParseInt(strings.TrimSpace(limitStr), 10, 64)
		framework.ExpectNoError(err)

		gomega.Expect(currentLimit).To(gomega.Equal(initialLimit), "host max_pages_limit (%d) should be reverted back to its initial value (%d)", currentLimit, initialLimit)
	})
}

func countWriteSizes(stdout string, targetSize int64, bucketName string) int {
	re := regexp.MustCompile(fmt.Sprintf(`<- WriteFile \([^)]*?, (%d) bytes\).*?"mount-id":"%s-`, targetSize, regexp.QuoteMeta(bucketName)))
	matches := re.FindAllStringSubmatch(stdout, -1)
	return len(matches)
}

func countReadSizes(stdout string, targetSize int64, bucketName string) int {
	re := regexp.MustCompile(fmt.Sprintf(`<- ReadFile \([^)]*?, (%d) bytes\).*?"mount-id":"%s-`, targetSize, regexp.QuoteMeta(bucketName)))
	matches := re.FindAllStringSubmatch(stdout, -1)
	return len(matches)
}

func assertFuseIO(tPod *specs.TestPod, opType string, targetSize int64, expectedCount int, bucketName string) {
	gomega.Eventually(func() int {
		stdout, err := tPod.GetSidecarLogs()
		if err != nil {
			return 0
		}
		if opType == "write" {
			return countWriteSizes(stdout, targetSize, bucketName)
		}
		return countReadSizes(stdout, targetSize, bucketName)
	}, retryTimeout, retryPolling).Should(gomega.Equal(expectedCount))
}
