/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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
	"strconv"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/utils"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIMountTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseMountTestSuite returns gcsFuseCSIMountTestSuite that implements TestSuite interface.
func InitGcsFuseMountTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIMountTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "mount",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
	}
}

func (t *gcsFuseCSIMountTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIMountTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIMountTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	envVar := os.Getenv(utils.TestWithSAVolumeInjectionEnvVar)
	supportSAVolInjection, err := strconv.ParseBool(envVar)
	if err != nil {
		klog.Fatalf(`env variable "%s" could not be converted to boolean`, envVar)
	}
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("mount", storageframework.GetDriverTimeouts(driver))
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

	testCaseStoreAndRetainData := func(configPrefix ...string) {
		init(configPrefix...)
		defer cleanup()

		ginkgo.By("Configuring the first pod")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying the first pod")
		tPod1.Create(ctx)

		ginkgo.By("Checking that the first pod is running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Checking that the first pod command exits with no error")
		bdi := tPod1.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf(`mountpoint -d "%s"`, mountPath))
		readAheadPath := fmt.Sprintf("/sys/class/bdi/%s/read_ahead_kb", bdi)

		currentReadAhead := tPod1.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, "cat "+readAheadPath)

		gomega.Expect(currentReadAhead).To(gomega.Equal(specs.ReadAheadCustomReadAheadKb))

		ginkgo.By("Deleting the first pod")
		tPod1.Cleanup(ctx)
	}

	testCaseHostNetworkEnabled := func(configPrefix ...string) {
		init(configPrefix...)
		defer cleanup()

		ginkgo.By("Configuring hostnetwork enabled pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.EnableHostNetwork()
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)

		ginkgo.By("Deploying hostnetwork enabled pod")
		tPod.Create(ctx)

		ginkgo.By("Checking pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the pod command exits with no error")
		tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf(`mountpoint -d "%s"`, mountPath))

		ginkgo.By("Checking that the pod can access bucket")
		// Create a new file B using gcsfuse.
		testFile := "testfile"
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("touch %v/%v", mountPath, testFile))

		// Check mounted volumes on pod
		projectedSAVolMounted := false
		for _, vol := range tPod.GetPodVols() {
			if vol.Name == webhook.SidecarContainerSATokenVolumeName {
				projectedSAVolMounted = true

				break
			}
		}
		gomega.Expect(projectedSAVolMounted).To(gomega.BeTrue())

		// Check the volume content.
		volumeContents := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName, fmt.Sprintf("ls %v", mountPath))
		gomega.Expect(volumeContents).To(gomega.Equal(testFile))

		ginkgo.By("Deleting pod")
		tPod.Cleanup(ctx)
	}

	ginkgo.It("[read ahead config] should update read ahead config knobs", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		testCaseStoreAndRetainData(specs.EnableCustomReadAhead)
	})

	ginkgo.It("should successfully mount for hostnetwork enabled pods", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}
		if supportSAVolInjection {
			testCaseHostNetworkEnabled()
		} else {
			ginkgo.By("Skipping the hostnetwork test for cluster version < 1.33.0")
		}
	})
}
