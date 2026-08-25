/*
Copyright 2018 The Kubernetes Authors.
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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
	"local/test/e2e/specs"
)

const (
	sidecarVolName   = "sidecar-gcs-vol"
	sidecarMountPath = "/mnt/sidecar"
	sharedVolName    = "shared-gcs-vol"
	sharedMountPath  = "/mnt/shared"
)

type gcsFuseCSISharedMountTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSISharedMountTestSuite returns gcsFuseCSISharedMountTestSuite that implements TestSuite interface.
func InitGcsFuseCSISharedMountTestSuite() storageframework.TestSuite {
	return &gcsFuseCSISharedMountTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "shared-mount",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
	}
}

func (t *gcsFuseCSISharedMountTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSISharedMountTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSISharedMountTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config             *storageframework.PerTestConfig
		volumeResourceList []*storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	f := framework.NewFrameworkWithCustomTimeouts("shared-mount", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(volumeNumber int, configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}

		l.volumeResourceList = []*storageframework.VolumeResource{}
		for i := range volumeNumber {
			if len(configPrefix) > 0 && configPrefix[0] == specs.SidecarAndSharedMountCoexistencePrefix && i == 0 {
				// Volume 0: Sidecar-mode volume (CSI ephemeral inline)
				l.volumeResourceList = append(l.volumeResourceList, storageframework.CreateVolumeResource(ctx, driver, l.config, storageframework.DefaultFsCSIEphemeralVolume, e2evolume.SizeRange{}))
				continue
			}
			l.volumeResourceList = append(l.volumeResourceList, specs.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{}))
		}
	}

	cleanup := func() {
		var cleanUpErrs []error
		for _, vr := range l.volumeResourceList {
			if vr != nil {
				if err := vr.CleanupResource(ctx); err != nil {
					cleanUpErrs = append(cleanUpErrs, err)
				}
			}
		}
		if len(cleanUpErrs) > 0 {
			err := utilerrors.NewAggregate(cleanUpErrs)
			framework.ExpectNoError(err, "while cleaning up")
		}
	}

	// TC: Sidecar and Shared Mount Coexistence Test
	// Verify that a pod using a GCSFuse sidecar-mode volume and a pod using a shared-mount volume
	// can coexist on the same node without conflicts. Also verify the webhook rejects a pod that mixes
	// sidecar and shared-mount volumes.
	ginkgo.It("[shared-mount] should verify sidecar and shared mount coexistence on the same node and reject pod mixing both volume types", func() {
		init(2, specs.SidecarAndSharedMountCoexistencePrefix)
		defer cleanup()

		gomega.Expect(l.volumeResourceList).To(gomega.HaveLen(2))
		gomega.Expect(l.volumeResourceList[0]).ToNot(gomega.BeNil())
		gomega.Expect(l.volumeResourceList[1]).ToNot(gomega.BeNil())

		sidecarVR := l.volumeResourceList[0]
		sharedVR := l.volumeResourceList[1]

		// 2. Attempt to create a single Pod referencing both volumes, and verify that the mutating webhook rejects the Pod creation attempt.
		ginkgo.By("Attempting to create a single Pod referencing both sidecar and shared-mount volumes")
		mixedPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		mixedPod.SetupVolume(sidecarVR, sidecarVolName, sidecarMountPath, false /* readOnly */)
		mixedPod.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)

		ginkgo.By("Verifying that the mutating webhook rejects the mixed Pod creation attempt")
		mixedPod.CreateExpectErrorContaining(ctx, "mixing shared node mount and non-shared node mount GCSFuse volumes in the same Pod is not allowed")

		// 3. Create sidecarTestPod referencing the sidecar volume and sharedMountTestPod referencing the shared mount PVC on the same node.
		ginkgo.By("Configuring and deploying sidecarTestPod referencing the sidecar volume")
		sidecarTestPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		sidecarTestPod.SetupVolume(sidecarVR, sidecarVolName, sidecarMountPath, false /* readOnly */)
		sidecarTestPod.Create(ctx)
		defer sidecarTestPod.Cleanup(ctx)

		ginkgo.By("Waiting for sidecarTestPod to be running and getting its node")
		sidecarTestPod.WaitForRunning(ctx)
		nodeName := sidecarTestPod.GetNode()

		ginkgo.By(fmt.Sprintf("Configuring and deploying sharedMountTestPod referencing the shared mount PVC on node %s", nodeName))
		sharedMountTestPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		sharedMountTestPod.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)
		sharedMountTestPod.SetNodeAffinity(nodeName, true /* sameNode */)
		sharedMountTestPod.Create(ctx)
		defer sharedMountTestPod.Cleanup(ctx)

		ginkgo.By("Waiting for sharedMountTestPod to be running")
		sharedMountTestPod.WaitForRunning(ctx)
		gomega.Expect(sharedMountTestPod.GetNode()).To(gomega.Equal(nodeName), "expected sharedMountTestPod to run on the same node as sidecarTestPod")

		// 4. Verify sidecarTestPod has a sidecar container injected while sharedMountTestPod does not, and a single Mounter Pod is created for sharedMountTestPod.
		ginkgo.By("Verifying sidecarTestPod has a sidecar container injected")
		sidecarTestPod.VerifySidecarPresence(true /* expectPresent */)

		ginkgo.By("Verifying sharedMountTestPod does NOT have a sidecar container injected")
		sharedMountTestPod.VerifySidecarPresence(false /* expectPresent */)

		ginkgo.By("Verifying a single Mounter Pod is created for sharedMountTestPod on the same node")
		mounterPods, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", webhook.SharedMountLabel, util.TrueStr),
		})
		framework.ExpectNoError(err, "failed to list mounter pods")
		gomega.Expect(mounterPods.Items).To(gomega.HaveLen(1), "expected exactly 1 Mounter Pod for the shared mount volume")
		gomega.Expect(mounterPods.Items[0].Spec.NodeName).To(gomega.Equal(nodeName), "expected Mounter Pod to be scheduled on the same node")

		// 5. Verify that both pods can successfully read and write to their respective volumes without conflicts.
		ginkgo.By("Verifying sidecarTestPod can write and read from its sidecar-mounted volume")
		sidecarTestPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %s | grep rw,", sidecarMountPath))
		sidecarTestPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello from sidecar pod' > %s/data-sidecar && grep 'hello from sidecar pod' %s/data-sidecar", sidecarMountPath, sidecarMountPath))

		ginkgo.By("Verifying sharedMountTestPod can write and read from its shared-mounted volume")
		sharedMountTestPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %s | grep rw,", sharedMountPath))
		sharedMountTestPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello from shared mount pod' > %s/data-shared && grep 'hello from shared mount pod' %s/data-shared", sharedMountPath, sharedMountPath))

		ginkgo.By("Verifying data persistence and isolation on both volumes")
		sidecarTestPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello from sidecar pod' %s/data-sidecar", sidecarMountPath))
		sharedMountTestPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'hello from shared mount pod' %s/data-shared", sharedMountPath))
	})
}
