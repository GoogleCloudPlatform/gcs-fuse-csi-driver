/*
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
	"fmt"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	storageutils "k8s.io/kubernetes/test/e2e/storage/utils"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIFailedMountTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIFailedMountTestSuite returns gcsFuseCSIFailedMountTestSuite that implements TestSuite interface
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

func (t *gcsFuseCSIFailedMountTestSuite) SkipUnsupportedTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
}

func (t *gcsFuseCSIFailedMountTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		driverCleanup  func()
		volumeResource *storageframework.VolumeResource
	}
	var (
		l local
	)

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("volumes", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config, l.driverCleanup = driver.PrepareTest(f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource())
		cleanUpErrs = append(cleanUpErrs, storageutils.TryFunc(l.driverCleanup))
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
		mountPath := "/mnt/test"
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError(fmt.Sprintf("failed to get GCS bucket %q: storage: bucket doesn't exist", specs.FakeVolumePrefix))
	})

	ginkgo.It("should fail when the specified service account does not have access to the GCS bucket", func() {
		init()
		defer cleanup()

		ginkgo.By("Configuring the pod")
		mountPath := "/mnt/test"
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResource, "test-gcsfuse-volume", mountPath, false)

		ginkgo.By("Deploying a Kubernetes service account without access to the GCS bucket")
		saName := "sa-without-access"
		testK8sSA := specs.NewTestKubernetesServiceAccount(f.ClientSet, f.Namespace, saName, "")
		testK8sSA.Create()
		defer testK8sSA.Cleanup()
		tPod.SetServiceAccount(saName)

		ginkgo.By("Deploying the pod")
		tPod.Create()
		defer tPod.Cleanup()

		ginkgo.By("Checking that the pod has failed mount error")
		tPod.WaitForFailedMountError("googleapi: Error 403: Caller does not have storage.buckets.get access to the Google Cloud Storage bucket.")
	})
}
