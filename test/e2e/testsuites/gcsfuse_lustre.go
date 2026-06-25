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
	"time"

	"local/test/e2e/specs"

	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

// LustreStorageClass is the StorageClass used to dynamically provision
// Lustre-backed PVCs in the GCS Fuse + Lustre combination tests. Overridden
// via --lustre-storage-class in e2e_test.go. If empty, these tests are skipped.
var LustreStorageClass = "lustre-rwx"

const (
	gcsFuseLustreCSIDriverName = "lustre.csi.storage.gke.io"
	gcsFuseLustreMountPath     = "/mnt/lustre"
	gcsFuseLustreVolName       = "lustre-vol"
	gcsFuseLustreGCSMountPath  = "/mnt/gcs"
	gcsFuseLustreGCSVolName    = "gcs-vol"
	gcsFuseLustrePVCSize       = "9000Gi"
	// gcsFuseLustrePVCBindTimeout accounts for Managed Lustre instance
	// provisioning, which can take 10+ minutes for large capacities like
	// gcsFuseLustrePVCSize.
	gcsFuseLustrePVCBindTimeout = 20 * time.Minute
)

type gcsFuseLustreCombinationTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseLustreCombinationTestSuite returns gcsFuseLustreCombinationTestSuite
// that implements TestSuite interface.
func InitGcsFuseLustreCombinationTestSuite() storageframework.TestSuite {
	return &gcsFuseLustreCombinationTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "gcsfuse-lustre",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
	}
}

func (t *gcsFuseLustreCombinationTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseLustreCombinationTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseLustreCombinationTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config          *storageframework.PerTestConfig
		gcsFuseResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	f := framework.NewFrameworkWithCustomTimeouts("gcsfuse-lustre", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func() {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		l.config.Prefix = specs.SkipCSIBucketAccessCheckPrefix
		l.gcsFuseResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		if l.gcsFuseResource != nil {
			framework.ExpectNoError(l.gcsFuseResource.CleanupResource(ctx))
		}
	}

	// skipIfLustreNotAvailable skips the test when the Lustre CSIDriver is not
	// installed on the cluster, no StorageClass is configured, or the named
	// StorageClass doesn't exist on the cluster. The lustre-rwx StorageClass is
	// not created automatically by the Lustre CSI driver/addon — its `network`
	// parameter must match the cluster's VPC, so it has to be created per
	// cluster (see docs/lustre-gcsfuse-dual-mount-cluster-setup.md). Checking
	// for it here avoids createLustrePVC hanging for its full bind timeout on a
	// PVC that can never be provisioned.
	skipIfLustreNotAvailable := func(testName string) {
		if LustreStorageClass == "" {
			e2eskipper.Skipf("--lustre-storage-class not set, skipping %s", testName)
		}
		if _, err := f.ClientSet.StorageV1().CSIDrivers().Get(ctx, gcsFuseLustreCSIDriverName, metav1.GetOptions{}); err != nil {
			e2eskipper.Skipf("%s CSIDriver not found, skipping %s: %v", gcsFuseLustreCSIDriverName, testName, err)
		}
		if _, err := f.ClientSet.StorageV1().StorageClasses().Get(ctx, LustreStorageClass, metav1.GetOptions{}); err != nil {
			e2eskipper.Skipf("StorageClass %q not found, skipping %s: %v. Create it first (see docs/lustre-gcsfuse-dual-mount-cluster-setup.md).", LustreStorageClass, testName, err)
		}
	}

	// createLustrePVC dynamically provisions a Lustre-backed PVC via
	// LustreStorageClass and waits for it to reach Bound before returning, so
	// callers can safely attach it to a pod immediately. Managed Lustre
	// instance provisioning can take several minutes for large capacities, so
	// this uses a generous timeout.
	createLustrePVC := func(namePrefix string) (*corev1.PersistentVolumeClaim, func()) {
		scName := LustreStorageClass
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: namePrefix,
				Namespace:    f.Namespace.Name,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				StorageClassName: &scName,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(gcsFuseLustrePVCSize),
					},
				},
			},
		}
		pvc, err := f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Create(ctx, pvc, metav1.CreateOptions{})
		framework.ExpectNoError(err)
		ginkgo.By(fmt.Sprintf("Waiting for Lustre PVC %s to be bound (up to %v, silently polling)", pvc.Name, gcsFuseLustrePVCBindTimeout))
		start := time.Now()
		framework.ExpectNoError(wait.PollUntilContextTimeout(ctx, framework.Poll, gcsFuseLustrePVCBindTimeout, true, func(ctx context.Context) (bool, error) {
			current, err := f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Get(ctx, pvc.Name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			return current.Status.Phase == corev1.ClaimBound, nil
		}))
		framework.Logf("Lustre PVC %s bound after %v", pvc.Name, time.Since(start))
		return pvc, func() {
			framework.ExpectNoError(f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Delete(
				ctx, pvc.Name, metav1.DeleteOptions{}))
		}
	}

	// Same-pod dual mount + R/W: a Lustre PVC and a GCS Fuse volume are mounted
	// in a single pod. The test writes to and reads back from each mount
	// independently, verifying both volumes are accessible and writable
	// without conflict.
	ginkgo.It("should mount both a Lustre PVC and a GCS Fuse volume in a single pod and read/write independently on both", func() {
		skipIfLustreNotAvailable("same-pod dual mount + R/W test")

		init()
		defer cleanup()

		ginkgo.By("Creating a dynamically provisioned Lustre PVC")
		pvc, cleanupPVC := createLustrePVC("gcsfuse-lustre-dual-pvc-")
		defer cleanupPVC()

		ginkgo.By("Configuring the pod with both GCS Fuse and Lustre volumes")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)

		ginkgo.By("Deploying the pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Waiting for the pod to be running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Writing a file to the GCS Fuse mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'gcs-fuse-data' > %v/gcs-data.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Writing a file to the Lustre mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'lustre-data' > %v/lustre-data.txt", gcsFuseLustreMountPath))

		ginkgo.By("Verifying the file written to the GCS Fuse mount is readable back")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'gcs-fuse-data' %v/gcs-data.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Verifying the file written to the Lustre mount is readable back")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'lustre-data' %v/lustre-data.txt", gcsFuseLustreMountPath))

		ginkgo.By("Verifying the GCS Fuse mount does not contain the Lustre-only file")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test ! -f %v/lustre-data.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Verifying the Lustre mount does not contain the GCS Fuse-only file")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test ! -f %v/gcs-data.txt", gcsFuseLustreMountPath))

		ginkgo.By("Verifying both mounts remain healthy and writable")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v", gcsFuseLustreGCSMountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v", gcsFuseLustreMountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'gcs-fuse-data-2' >> %v/gcs-data.txt && echo 'lustre-data-2' >> %v/lustre-data.txt",
				gcsFuseLustreGCSMountPath, gcsFuseLustreMountPath))
	})

	// Multi-pod shared Lustre + shared GCS bucket (RWX/RWX): two pods mount the
	// same Lustre PVC (RWX) and the same GCS Fuse bucket (RWX) concurrently. A
	// write from either pod to either volume must be immediately visible from
	// the other pod.
	ginkgo.It("should allow two pods to share the same Lustre PVC (RWX) and GCS Fuse bucket (RWX) and see each other's writes", func() {
		skipIfLustreNotAvailable("multi-pod shared Lustre + GCS bucket test")

		init()
		defer cleanup()

		ginkgo.By("Creating a dynamically provisioned Lustre PVC")
		pvc, cleanupPVC := createLustrePVC("gcsfuse-lustre-rwx-pvc-")
		defer cleanupPVC()

		ginkgo.By("Creating Pod-1 with both shared volumes")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod1.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)
		tPod1.Create(ctx)
		defer tPod1.Cleanup(ctx)
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Creating Pod-2 with the same shared volumes")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Pod-1 writes to the shared Lustre volume and the shared GCS bucket")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'pod1-lustre' > %v/pod1-lustre.txt", gcsFuseLustreMountPath))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'pod1-gcs' > %v/pod1-gcs.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Pod-2 sees both files written by Pod-1")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'pod1-lustre' %v/pod1-lustre.txt", gcsFuseLustreMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'pod1-gcs' %v/pod1-gcs.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Pod-2 writes back to the shared Lustre volume and the shared GCS bucket")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'pod2-lustre' > %v/pod2-lustre.txt", gcsFuseLustreMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'pod2-gcs' > %v/pod2-gcs.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Pod-1 sees both files written by Pod-2")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'pod2-lustre' %v/pod2-lustre.txt", gcsFuseLustreMountPath))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'pod2-gcs' %v/pod2-gcs.txt", gcsFuseLustreGCSMountPath))
	})

	// Pod restart + persistence across both: data written to both the Lustre
	// PVC and the GCS Fuse volume in Pod-1 survives a pod deletion, and is
	// fully readable from a fresh Pod-2 that binds the same Lustre PVC and GCS
	// bucket afterward.
	ginkgo.It("should persist data on both the Lustre PVC and the GCS Fuse volume across a pod restart", func() {
		skipIfLustreNotAvailable("pod restart and persistence test")

		init()
		defer cleanup()

		ginkgo.By("Creating a dynamically provisioned Lustre PVC")
		pvc, cleanupPVC := createLustrePVC("gcsfuse-lustre-persist-pvc-")
		defer cleanupPVC()

		ginkgo.By("Creating Pod-1 and writing data to both volumes")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod1.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)
		tPod1.Create(ctx)
		tPod1.WaitForRunning(ctx)

		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'persistent-lustre' > %v/persist.txt", gcsFuseLustreMountPath))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'persistent-gcs' > %v/persist.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Deleting Pod-1 to simulate a pod restart")
		tPod1.Cleanup(ctx)

		ginkgo.By("Creating Pod-2 mounting the same Lustre PVC and GCS bucket")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Verifying Lustre data persisted in Pod-2")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'persistent-lustre' %v/persist.txt", gcsFuseLustreMountPath))

		ginkgo.By("Verifying GCS Fuse data persisted in Pod-2")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'persistent-gcs' %v/persist.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Verifying Pod-2 can extend the persisted data on both volumes")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'persistent-lustre-2' >> %v/persist.txt && echo 'persistent-gcs-2' >> %v/persist.txt",
				gcsFuseLustreMountPath, gcsFuseLustreGCSMountPath))
	})

	// Node drain / reschedule remount: after draining the node running the
	// dual-mount pod, a replacement pod on a different node must remount both
	// volumes and find the data intact.
	ginkgo.It("should remount both the Lustre PVC and the GCS Fuse volume with data intact after the node is drained", func() {
		skipIfLustreNotAvailable("node drain remount test")

		init()
		defer cleanup()

		ginkgo.By("Creating a dynamically provisioned Lustre PVC")
		pvc, cleanupPVC := createLustrePVC("gcsfuse-lustre-drain-pvc-")
		defer cleanupPVC()

		ginkgo.By("Creating the dual-mount pod and waiting for it to run")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod1.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)
		tPod1.Create(ctx)
		tPod1.WaitForRunning(ctx)

		nodeName := tPod1.GetNode()
		framework.Logf("Pod-1 scheduled on node %s", nodeName)

		ginkgo.By("Writing data to both volumes from Pod-1")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'drain-lustre' > %v/drain.txt", gcsFuseLustreMountPath))
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'drain-gcs' > %v/drain.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By(fmt.Sprintf("Cordoning node %s", nodeName))
		cordonPatch := []byte(`{"spec":{"unschedulable":true}}`)
		_, err := f.ClientSet.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, cordonPatch, metav1.PatchOptions{})
		framework.ExpectNoError(err)
		defer func() {
			ginkgo.By(fmt.Sprintf("Uncordoning node %s", nodeName))
			uncordonPatch := []byte(`{"spec":{"unschedulable":false}}`)
			_, patchErr := f.ClientSet.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, uncordonPatch, metav1.PatchOptions{})
			framework.ExpectNoError(patchErr)
		}()

		ginkgo.By(fmt.Sprintf("Evicting Pod-1 from node %s", nodeName))
		eviction := &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tPod1.GetPodName(),
				Namespace: tPod1.GetPodNamespace(),
			},
		}
		err = f.ClientSet.PolicyV1().Evictions(tPod1.GetPodNamespace()).Evict(ctx, eviction)
		framework.ExpectNoError(err)

		ginkgo.By("Waiting for Pod-1 to be fully removed")
		tPod1.WaitForPodNotFoundInNamespace(ctx)

		ginkgo.By("Creating Pod-2 with anti-affinity for the drained node")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)
		tPod2.SetNodeAffinity(nodeName, false)
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)
		tPod2.WaitForRunning(ctx)

		framework.Logf("Pod-2 rescheduled on node %s", tPod2.GetNode())

		ginkgo.By("Verifying Lustre data is intact on the new node")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'drain-lustre' %v/drain.txt", gcsFuseLustreMountPath))

		ginkgo.By("Verifying GCS Fuse data is intact on the new node")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'drain-gcs' %v/drain.txt", gcsFuseLustreGCSMountPath))

		ginkgo.By("Verifying both mounts are functional on the new node")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v", gcsFuseLustreMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v", gcsFuseLustreGCSMountPath))
	})
}
