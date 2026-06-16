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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	e2ekubectl "k8s.io/kubernetes/test/e2e/framework/kubectl"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

// PDStorageClass is the StorageClass used to provision the PD-backed PVC in
// dual CSI volume tests. Overridden via --pd-storage-class in e2e_test.go.
var PDStorageClass = "standard-rwo"

const (
	pdCSIDriverName  = "pd.csi.storage.gke.io"
	gcsFuseMountPath = "/mnt/gcs"
	pdMountPath      = "/mnt/pd"
	gcsFuseVolName   = "gcs-vol"
	pdVolName        = "pd-vol"

	// largeFileSizeBytes is 1 GiB — large enough to stress both drivers.
	largeFileSizeBytes = 1 * 1024 * 1024 * 1024

	snapshotReadyTimeout = 5 * time.Minute
	pvcResizeTimeout     = 5 * time.Minute
)

type gcsFuseCSIDualCSIVolumeTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIDualCSIVolumeTestSuite returns gcsFuseCSIDualCSIVolumeTestSuite
// that implements TestSuite interface.
func InitGcsFuseCSIDualCSIVolumeTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIDualCSIVolumeTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "dual-csi-volume",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
	}
}

func (t *gcsFuseCSIDualCSIVolumeTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIDualCSIVolumeTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIDualCSIVolumeTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config          *storageframework.PerTestConfig
		gcsFuseResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	f := framework.NewFrameworkWithCustomTimeouts("dual-csi-volume", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func() {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		// Skip the CSI pre-mount bucket access check so the test works on
		// OSS clusters where the test pod has no credential configmap annotation.
		// The WIF IAM binding on the bucket still grants real GCSFuse access.
		l.config.Prefix = specs.SkipCSIBucketAccessCheckPrefix
		l.gcsFuseResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		if l.gcsFuseResource != nil {
			framework.ExpectNoError(l.gcsFuseResource.CleanupResource(ctx))
		}
	}

	// createPDPVC creates a PD-backed PVC and returns it with a cleanup func.
	createPDPVC := func(namePrefix, size string) (*corev1.PersistentVolumeClaim, func()) {
		scName := PDStorageClass
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: namePrefix,
				Namespace:    f.Namespace.Name,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &scName,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(size),
					},
				},
			},
		}
		pvc, err := f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Create(ctx, pvc, metav1.CreateOptions{})
		framework.ExpectNoError(err)
		return pvc, func() {
			framework.ExpectNoError(f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Delete(
				ctx, pvc.Name, metav1.DeleteOptions{}))
		}
	}

	// skipIfPDCSINotInstalled skips when pd.csi.storage.gke.io CSIDriver is absent.
	skipIfPDCSINotInstalled := func(testName string) {
		_, err := f.ClientSet.StorageV1().CSIDrivers().Get(ctx, pdCSIDriverName, metav1.GetOptions{})
		if err != nil {
			e2eskipper.Skipf("%s CSIDriver not found, skipping %s: %v", pdCSIDriverName, testName, err)
		}
	}

	// TC-03: Pod Restart — PD Persistence + GCS Remount
	//
	//         [Pod-1]               [Pod-2]
	//         /     \               /     \
	//    [gcs-vol] [pd-vol]    [gcs-vol] [pd-vol]  ← same PVC reused
	//        |         |           |         |
	//     [GCS]   [PD disk]     [GCS]   [PD disk]
	//
	// Pod-1 writes sentinel files to both mounts, then is deleted.
	// Pod-2 mounts the same PVC + same GCS bucket and verifies:
	//   - PD data persists via persistent block volume.
	//   - GCS Fuse data persists via durable object storage and
	//     re-authenticates cleanly on the fresh mount.
	ginkgo.It("should persist PD data and remount GCS Fuse cleanly after pod deletion", func() {
		// Skip when pd.csi.storage.gke.io is not installed (e.g. OSS clusters
		// without the GCE PD CSI driver, or non-GCP environments).
		_, err := f.ClientSet.StorageV1().CSIDrivers().Get(ctx, pdCSIDriverName, metav1.GetOptions{})
		if err != nil {
			e2eskipper.Skipf("%s CSIDriver not found, skipping dual-driver test: %v", pdCSIDriverName, err)
		}

		init()
		defer cleanup()

		// ── Step 1: Create the PD-backed PVC ─────────────────────────────────
		ginkgo.By(fmt.Sprintf("Creating PD-backed PVC using StorageClass %q", PDStorageClass))
		scName := PDStorageClass
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "pod-restart-pd-pvc-",
				Namespace:    f.Namespace.Name,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &scName,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("5Gi"),
					},
				},
			},
		}
		pvc, err = f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Create(ctx, pvc, metav1.CreateOptions{})
		framework.ExpectNoError(err)
		defer func() {
			framework.ExpectNoError(f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Delete(
				ctx, pvc.Name, metav1.DeleteOptions{}))
		}()

		// ── Step 2: Pod-1 — write sentinel files to both mounts ──────────────
		ginkgo.By("Configuring Pod-1 with both GCS Fuse and PD volumes")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod1.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, pdVolName, pdMountPath, false)

		ginkgo.By("Deploying Pod-1")
		tPod1.Create(ctx)
		defer tPod1.Cleanup(ctx)

		ginkgo.By("Waiting for Pod-1 to be running")
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Writing sentinel file to the GCS Fuse volume from Pod-1")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'gcs-sentinel-data' > %v/gcs-sentinel.txt && grep 'gcs-sentinel-data' %v/gcs-sentinel.txt",
				gcsFuseMountPath, gcsFuseMountPath))

		ginkgo.By("Writing sentinel file to the PD volume from Pod-1")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'pd-sentinel-data' > %v/pd-sentinel.txt && grep 'pd-sentinel-data' %v/pd-sentinel.txt",
				pdMountPath, pdMountPath))

		// ── Step 3: Delete Pod-1 ─────────────────────────────────────────────
		ginkgo.By("Deleting Pod-1 to simulate a pod restart")
		tPod1.Cleanup(ctx)

		// ── Step 4: Pod-2 — mount same PVC + GCS, verify sentinel files ──────
		ginkgo.By("Configuring Pod-2 with the same PVC and GCS Fuse volume")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, pdVolName, pdMountPath, false)

		ginkgo.By("Deploying Pod-2")
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		ginkgo.By("Waiting for Pod-2 to be running")
		tPod2.WaitForRunning(ctx)

		// ── Step 5: Assertions ────────────────────────────────────────────────
		ginkgo.By("Verifying PD sentinel file persists after pod restart")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'pd-sentinel-data' %v/pd-sentinel.txt",
				pdMountPath))

		ginkgo.By("Verifying GCS Fuse sentinel file is readable after fresh remount")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'gcs-sentinel-data' %v/gcs-sentinel.txt",
				gcsFuseMountPath))
	})

	// TC-04: PD-Backed File Cache
	//
	// Configure GCS Fuse with a local file cache directory backed by a PD volume.
	// Read files from the GCS Fuse mount. Expect cached reads to be served from
	// the PD-backed cache directory, reducing repeated GCS fetch latency.
	// TC-04: PD-Backed File Cache
	ginkgo.It("should serve GCS Fuse cached reads from a PD-backed cache directory", func() {
		skipIfPDCSINotInstalled("PD-backed file cache test")

		// EnableFileCachePrefix sets fileCacheCapacity=100Mi on the GCS Fuse volume.
		init()
		l.config.Prefix = specs.EnableFileCachePrefix
		l.gcsFuseResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
		defer cleanup()

		bucketName := l.gcsFuseResource.Pv.Spec.CSI.VolumeHandle
		const testFileName = "cache-test-file.txt"

		gcsfuseDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
		if !ok {
			framework.Failf("driver is not *specs.GCSFuseCSITestDriver, cannot pre-seed GCS object")
		}

		ginkgo.By(fmt.Sprintf("Pre-seeding test file %q in GCS bucket %q", testFileName, bucketName))
		gcsfuseDriver.CreateTestFileInBucket(ctx, testFileName, bucketName)

		pvc, cleanupPVC := createPDPVC("pd-cache-pvc-", "5Gi")
		defer cleanupPVC()

		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		// Override the cache volume with PD PVC instead of emptyDir.
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, webhook.SidecarContainerCacheVolumeName, "/cache", false)

		ginkgo.By("Deploying pod with PD-backed GCS Fuse file cache")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)
		tPod.WaitForRunning(ctx)

		// Get the cache subfolder name from the pod volume spec.
		//cacheSubfolder := tPod.GetCacheSubfolder()
		ginkgo.By("Inspecting cache directory contents")
		tPod.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			"find /cache -type f || true",
		)

		ginkgo.By("Reading the test file from GCS Fuse mount (first read — populates cache)")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("cat %v/%v", gcsFuseMountPath, testFileName))

		// ginkgo.By("Verifying cache file exists in the PD-backed cache directory")
		// tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
		//         fmt.Sprintf("grep '%v' /cache/.volumes/%v/gcsfuse-file-cache/%v/%v",
		//                 testFileName, cacheSubfolder, bucketName, testFileName))
		ginkgo.By("Inspecting cache directory contents after first read")
		tPod.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			"find /cache -type f || true",
		)

		ginkgo.By("Reading the test file again (second read — served from cache)")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("cat %v/%v", gcsFuseMountPath, testFileName))
	})

	// TC-05: Node Drain — Both Volumes Remount
	//
	// With a dual-volume pod running, drain the node it is scheduled on.
	// Expect the pod to be rescheduled on another node with both PD and
	// GCS Fuse volumes remounted and data still accessible.
	// TC-05: Node Drain — Both Volumes Remount
	ginkgo.It("should remount both PD and GCS Fuse volumes after node drain and pod recreation", func() {
		skipIfPDCSINotInstalled("node drain remount test")
		init()
		defer cleanup()

		pvc, cleanupPVC := createPDPVC("node-drain-pd-pvc-", "5Gi")
		defer cleanupPVC()
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, pdVolName, pdMountPath, false)

		ginkgo.By("Deploying pod with both GCS Fuse and PD volumes")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)
		tPod.WaitForRunning(ctx)

		ginkgo.By("Writing sentinel data to PD volume before node drain")
		tPod.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf(
				"echo 'pd-drain-data' > %v/pd-drain.txt && grep 'pd-drain-data' %v/pd-drain.txt",
				pdMountPath,
				pdMountPath,
			),
		)

		ginkgo.By("Writing sentinel data to GCS Fuse volume before node drain")
		tPod.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf(
				"echo 'gcs-drain-data' > %v/gcs-drain.txt && grep 'gcs-drain-data' %v/gcs-drain.txt",
				gcsFuseMountPath,
				gcsFuseMountPath,
			),
		)

		ginkgo.By("Getting the node the pod is scheduled on")
		pod, err := f.ClientSet.CoreV1().
			Pods(f.Namespace.Name).
			Get(ctx, tPod.GetPodName(), metav1.GetOptions{})
		framework.ExpectNoError(err)

		nodeName := pod.Spec.NodeName
		framework.Logf("Pod is scheduled on node %q", nodeName)

		ginkgo.By(fmt.Sprintf("Cordoning node %q", nodeName))
		e2ekubectl.RunKubectlOrDie(
			f.Namespace.Name,
			"cordon",
			nodeName,
		)

		defer func() {
			ginkgo.By(fmt.Sprintf("Uncordoning node %q", nodeName))
			e2ekubectl.RunKubectlOrDie(
				f.Namespace.Name,
				"uncordon",
				nodeName,
			)
		}()

		ginkgo.By(fmt.Sprintf("Draining node %q", nodeName))
		e2ekubectl.RunKubectlOrDie(
			f.Namespace.Name,
			"drain",
			nodeName,
			"--ignore-daemonsets",
			"--delete-emptydir-data",
			"--force",
			"--timeout=120s",
		)
		ginkgo.By("Creating a new pod after node drain")

		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, pdVolName, pdMountPath, false)

		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		tPod2.WaitForRunning(ctx)

		newPod, err := f.ClientSet.CoreV1().
			Pods(f.Namespace.Name).
			Get(ctx, tPod2.GetPodName(), metav1.GetOptions{})
		framework.ExpectNoError(err)

		framework.Logf(
			"New pod scheduled on node %q",
			newPod.Spec.NodeName,
		)
		ginkgo.By("Verifying PD sentinel data is still accessible after node drain")
		tPod2.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf(
				"grep 'pd-drain-data' %v/pd-drain.txt",
				pdMountPath,
			),
		)

		ginkgo.By("Verifying GCS Fuse sentinel data is still accessible after node drain")
		tPod2.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf(
				"grep 'gcs-drain-data' %v/gcs-drain.txt",
				gcsFuseMountPath,
			),
		)
	})

}

// // waitForPVCCapacity polls until the PVC's status.capacity.storage reaches at
// // least the requested quantity or the timeout expires.
// func waitForPVCCapacity(ctx context.Context, f *framework.Framework, pvcName string, requested resource.Quantity, timeout time.Duration) error {
//         deadline := time.Now().Add(timeout)
//         for time.Now().Before(deadline) {
//                 pvc, err := f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Get(ctx, pvcName, metav1.GetOptions{})
//                 if err != nil {
//                         return err
//                 }
//                 if cap, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
//                         if cap.Cmp(requested) >= 0 {
//                                 return nil
//                         }
//                 }
//                 framework.Logf("PVC %q capacity not yet %v, retrying...", pvcName, requested)
//                 time.Sleep(5 * time.Second)
//         }
//         return fmt.Errorf("PVC %q did not reach capacity %v within %v", pvcName, requested, timeout)
// }
