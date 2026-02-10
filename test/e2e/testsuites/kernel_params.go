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
	"regexp"
	"strings"

	"local/test/e2e/specs"
	"local/test/e2e/utils"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
	MaxReadAheadKb               ParamName = "max-read-ahead-kb"
	MaxBackgroundRequests        ParamName = "fuse-max-background-requests"
	CongestionWindowThreshold    ParamName = "fuse-congestion-window-threshold"
	CustomCSIReadAhead3MB                  = "3072"
	CustomGCSFuseMaxReadAhead6MB           = "6144"
	CustomGCSFuseMaxReadAhead8MB           = "8192"
	CustomMaxBackground                    = "999"
	CustomCongestionThreshold              = "666"
	tmpMountPath                           = "/gcsfuse-tmp"
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

func skipIfKernelParamsNotSupported(ctx context.Context, f *framework.Framework) {
	gcsfuseVersion, branch := specs.GCSFuseVersionAndBranch(ctx, f)

	// Running since we are on master branch.
	if branch == utils.MasterBranchName {
		return
	}
	// Kernel parameters were introduced in v3.7.0-gke.0.
	if !gcsfuseVersion.AtLeast(version.MustParseSemantic(utils.MinGCSFuseKernelParamsVersion)) {
		e2eskipper.Skipf("skip kernel params test for unsupported gcsfuse version %s", gcsfuseVersion.String())
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
		skipIfKernelParamsNotSupported(ctx, f)

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
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, false /* needsFuseConnections */, fmt.Sprintf("read_ahead_kb=%s", CustomCSIReadAhead3MB))

		ginkgo.By("Verifying read_ahead_kb is eventually overwritten by GCSFuse default value")
		expectedValue := kernelParamValueFromConfigFile(f, tPod, volumeName, MaxReadAheadKb)
		gomega.Eventually(func() string {
			return KernelParamValueFromMount(f, tPod, MaxReadAheadKb)
		}, retryTimeout, retryPolling).Should(gomega.Equal(expectedValue))

		ginkgo.By("Verify actual value of ReadAheadKb is not equal to CSI read_ahead_kb value")
		actualValue := KernelParamValueFromMount(f, tPod, MaxReadAheadKb)
		gomega.Expect(actualValue).ShouldNot(gomega.Equal(CustomCSIReadAhead3MB))

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
		tPod := setupAndDeployTestPod(ctx, f, l.volumeResource, false /* needsFuseConnections */, fmt.Sprintf("read_ahead_kb=%s,file-system:max-read-ahead-kb:%s", CustomCSIReadAhead3MB, CustomGCSFuseMaxReadAhead6MB))

		ginkgo.By("Verifying ReadAheadkb is eventually overwritten by user provided value of GCSFuse max-read-ahead-kb")
		expectedValue := kernelParamValueFromConfigFile(f, tPod, volumeName, MaxReadAheadKb)
		gomega.Expect(CustomGCSFuseMaxReadAhead6MB).Should(gomega.Equal(expectedValue))
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
		ginkgo.Entry("for read-ahead-kb", MaxReadAheadKb, CustomGCSFuseMaxReadAhead8MB, false),
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
}
