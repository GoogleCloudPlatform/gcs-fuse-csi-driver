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
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	"google.golang.org/grpc/codes"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIFailedMountTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIFailedMountTestSuite returns gcsFuseCSIFailedMountTestSuite that implements TestSuite interface.
func InitGcsFuseCSIFailedMountTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIFailedMountTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "failedMount",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
				storageframework.DefaultFsPreprovisionedPV,
				storageframework.DefaultFsDynamicPV,
			},
		},
	}
}

func (t *gcsFuseCSIFailedMountTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIFailedMountTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIFailedMountTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("failed-mount", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource())
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	ginkgo.It("should fail when the specified GCS bucket does not exist", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}

		init(specs.FakeVolumePrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(codes.NotFound.String())
		tPod.WaitForFailedMountError("storage: bucket doesn't exist")
	})

	ginkgo.It("should fail when the specified GCS bucket name is invalid", func() {
		if pattern.VolType == storageframework.DynamicPV {
			e2eskipper.Skipf("skip for volume type %v", storageframework.DynamicPV)
		}

		init(specs.InvalidVolumePrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(codes.NotFound.String())
		tPod.WaitForFailedMountError("storage: bucket doesn't exist")
	})

	ginkgo.It("should fail when the specified service account does not have access to the GCS bucket", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		ginkgo.By("Deploying a Kubernetes service account without access to the GCS bucket")
		saName := "sa-without-access"
		testK8sSA := specs.NewTestKubernetesServiceAccount(f.ClientSet, f.Namespace, saName, "")
		testK8sSA.Create()
		tPod.SetServiceAccount(saName)

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error PermissionDenied")
		tPod.WaitForFailedMountError(codes.PermissionDenied.String())
		tPod.WaitForFailedMountError("does not have storage.objects.list access to the Google Cloud Storage bucket.")

		ginkgo.By("Deleting the Kubernetes service account")
		testK8sSA.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error Unauthenticated")
		tPod.WaitForFailedMountError(codes.Unauthenticated.String())
		tPod.WaitForFailedMountError("storage service manager failed to setup service: timed out waiting for the condition")
	})

	ginkgo.It("should fail when the sidecar container is not injected", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		tPod.SetAnnotations(map[string]string{})

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(codes.FailedPrecondition.String())
		tPod.WaitForFailedMountError("failed to find the sidecar container in Pod spec")
	})

	ginkgo.It("should fail when the gcsfuse processes got killed due to OOM", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)
		tPod.SetCommand("touch /mnt/test/test_file && while true; do echo $(date) >> /mnt/test/test_file; done")

		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/volumes":      "true",
			"gke-gcsfuse/memory-limit": "15Mi",
		})

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(codes.ResourceExhausted.String())
		tPod.WaitForFailedMountError("gcsfuse exited with error: signal: killed")
	})

	ginkgo.It("should fail when invalid mount options are passed", func() {
		init(specs.InvalidMountOptionsVolumePrefix)
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(codes.InvalidArgument.String())
		tPod.WaitForFailedMountError("Incorrect Usage. flag provided but not defined: -invalid-option")
	})

	ginkgo.It("should fail when the sidecar container is specified with high resource usage", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/volumes":      "true",
			"gke-gcsfuse/memory-limit": "1000000000G",
			"gke-gcsfuse/cpu-limit":    "1000000000",
		})

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod is in Unschedulable status")
		tPod.WaitForUnschedulable()
	})
}
