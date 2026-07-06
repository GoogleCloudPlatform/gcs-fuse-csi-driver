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
	"sync"
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

	// init prepares the per-test config and GCS Fuse volume resource.
	// An optional prefix controls volume attributes (e.g. EnableFileCachePrefix).
	// Defaults to SkipCSIBucketAccessCheckPrefix so tests work on OSS clusters
	// where the test pod has no credential configmap annotation.
	init := func(prefixes ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		prefix := specs.SkipCSIBucketAccessCheckPrefix
		if len(prefixes) > 0 && prefixes[0] != "" {
			prefix = prefixes[0]
		}
		l.config.Prefix = prefix
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
	ginkgo.It("[Feature: GCSFuse-PDCSI] should persist PD data and remount GCS Fuse cleanly after pod deletion", func() {
		skipIfPDCSINotInstalled("dual-driver test")

		init()
		defer cleanup()

		// ── Step 1: Create the PD-backed PVC ─────────────────────────────────
		ginkgo.By(fmt.Sprintf("Creating PD-backed PVC using StorageClass %q", PDStorageClass))
		pvc, cleanupPVC := createPDPVC("pod-restart-pd-pvc-", "5Gi")
		defer cleanupPVC()

		// ── Step 2: Pod-1 — write sentinel files to both mounts ──────────────
		ginkgo.By("Configuring Pod-1 with both GCS Fuse and PD volumes")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod1.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, pdVolName, pdMountPath, false)

		ginkgo.By("Deploying Pod-1")
		tPod1.Create(ctx)

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
			fmt.Sprintf("grep 'pd-sentinel-data' %v/pd-sentinel.txt", pdMountPath))

		ginkgo.By("Verifying GCS Fuse sentinel file is readable after fresh remount")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'gcs-sentinel-data' %v/gcs-sentinel.txt", gcsFuseMountPath))
	})

	// TC-04: PD-Backed File Cache
	//
	// Configure GCS Fuse with a local file cache directory backed by a PD volume.
	// Read files from the GCS Fuse mount. Expect cached reads to be served from
	// the PD-backed cache directory, reducing repeated GCS fetch latency.
	ginkgo.It("[Feature: GCSFuse-PDCSI] should serve GCS Fuse cached reads from a PD-backed cache directory", func() {
		skipIfPDCSINotInstalled("PD-backed file cache test")

		init(specs.EnableFileCachePrefix)
		defer cleanup()

		bucketName := l.gcsFuseResource.Pv.Spec.CSI.VolumeHandle
		const testFileName = "cache-test-file.txt"

		gcsfuseDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
		if !ok {
			framework.Failf("driver is not *specs.GCSFuseCSITestDriver, cannot pre-seed GCS object")
		}

		ginkgo.By(fmt.Sprintf(
			"Pre-seeding test file %q in GCS bucket %q",
			testFileName,
			bucketName,
		))
		gcsfuseDriver.CreateTestFileInBucket(
			ctx,
			testFileName,
			bucketName,
		)

		pvc, cleanupPVC := createPDPVC("pd-cache-pvc-", "5Gi")
		defer cleanupPVC()

		// ── Pod-1: populate the cache ─────────────────────────────────────────
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, webhook.SidecarContainerCacheVolumeName, "", false)
		// FSGroup 1000 matches the sidecar's GID so it can write to the PD-backed cache volume.
		tPod.SetNonRootSecurityContext(0, 0, 1000)

		ginkgo.By("Deploying pod with PD-backed GCS Fuse cache")
		tPod.Create(ctx)

		ginkgo.By("Waiting for pod to become Running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Reading test file from GCS Fuse mount to populate PD-backed cache")
		tPod.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf("cat %v/%v", gcsFuseMountPath, testFileName),
		)

		ginkgo.By("Deleting Pod-1 to verify PD-backed cache survives pod recreation")
		tPod.Cleanup(ctx)

		// ── Pod-2: verify cache persisted on the PD volume ───────────────────
		ginkgo.By("Recreating pod using the same PD-backed cache PVC")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, webhook.SidecarContainerCacheVolumeName, "", false)
		tPod2.SetNonRootSecurityContext(0, 0, 1000)

		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		ginkgo.By("Waiting for recreated pod to become Running")
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Reading file again after pod recreation — should be served from PD-backed cache")
		tPod2.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf("cat %v/%v", gcsFuseMountPath, testFileName),
		)
	})
	// TC-05: Node Drain — Both Volumes Remount
	//
	// With a dual-volume pod running, drain the node it is scheduled on.
	// A new pod is created and expected to be scheduled on another node with
	// both PD and GCS Fuse volumes remounted and data still accessible.
	ginkgo.It("[Feature: GCSFuse-PDCSI] should allow a newly created pod to remount both PD and GCS Fuse volumes after node drain", func() {
		skipIfPDCSINotInstalled("node drain remount test")

		init()
		defer cleanup()

		pvc, cleanupPVC := createPDPVC("node-drain-pd-pvc-", "5Gi")
		defer cleanupPVC()

		// Create initial pod.
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, pdVolName, pdMountPath, false)

		ginkgo.By("Deploying pod with both GCS Fuse and PD volumes")
		tPod.Create(ctx)

		tPod.WaitForRunning(ctx)

		ginkgo.By("Writing sentinel data to PD volume before node drain")
		tPod.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf(
				"echo 'pd-drain-data' > %v/pd-drain.txt && grep 'pd-drain-data' %v/pd-drain.txt",
				pdMountPath, pdMountPath,
			),
		)

		ginkgo.By("Writing sentinel data to GCS Fuse volume before node drain")
		tPod.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf(
				"echo 'gcs-drain-data' > %v/gcs-drain.txt && grep 'gcs-drain-data' %v/gcs-drain.txt",
				gcsFuseMountPath, gcsFuseMountPath,
			),
		)

		nodeName := tPod.GetNode()
		framework.Logf("Initial pod scheduled on node %q", nodeName)

		ginkgo.By(fmt.Sprintf("Cordoning node %q", nodeName))
		e2ekubectl.RunKubectlOrDie(f.Namespace.Name, "cordon", nodeName)

		defer func() {
			ginkgo.By(fmt.Sprintf("Uncordoning node %q", nodeName))
			e2ekubectl.RunKubectlOrDie(f.Namespace.Name, "uncordon", nodeName)
		}()

		ginkgo.By(fmt.Sprintf("Draining node %q", nodeName))
		e2ekubectl.RunKubectlOrDie(
			f.Namespace.Name,
			"drain", nodeName,
			"--ignore-daemonsets",
			"--delete-emptydir-data",
			"--force",
			"--timeout=120s",
		)

		// Eagerly cleanup Pod-1 so the RWO PVC detaches before Pod-2 tries to bind.
		// drain evicts the pod but explicit cleanup ensures the PVC is released.
		ginkgo.By("Cleaning up Pod-1 to release the RWO PVC before Pod-2 mounts it")
		tPod.Cleanup(ctx)

		ginkgo.By("Creating a new pod using the same PVC after node drain")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, pdVolName, pdMountPath, false)

		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		tPod2.WaitForRunning(ctx)

		framework.Logf("New pod scheduled on node %q", tPod2.GetNode())

		ginkgo.By("Verifying PD data remains accessible after remount")
		tPod2.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf("grep 'pd-drain-data' %v/pd-drain.txt", pdMountPath),
		)

		ginkgo.By("Verifying GCS Fuse data remains accessible after remount")
		tPod2.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf("grep 'gcs-drain-data' %v/gcs-drain.txt", gcsFuseMountPath),
		)

		ginkgo.By("Verifying the new pod is healthy after both volume remounts")
		tPod2.VerifyExecInPodSucceed(
			f,
			specs.TesterContainerName,
			fmt.Sprintf(
				"test -f %v/pd-drain.txt && test -f %v/gcs-drain.txt",
				pdMountPath, gcsFuseMountPath,
			),
		)
	})

	// ── Test: Multi-pod sharing of a GCS Fuse volume with isolated PD volumes ──
	//
	// Two pods run simultaneously, both mounting the same GCS Fuse volume (RWX)
	// while each also has its own PD-backed PVC (RWO). The test verifies a
	// write from one pod to the shared GCS volume becomes visible from the
	// other, while each pod's PD volume stays isolated and invisible to the
	// other pod.
	ginkgo.It("[Feature: GCSFuse-PDCSI] should share GCS Fuse volume writes across two pods while keeping their individual PD volumes isolated", func() {
		skipIfPDCSINotInstalled("multi-pod sharing test")

		init()
		defer cleanup()

		pvc1, cleanupPVC1 := createPDPVC("sharing-pd-pvc-1-", "5Gi")
		defer cleanupPVC1()
		pvc2, cleanupPVC2 := createPDPVC("sharing-pd-pvc-2-", "5Gi")
		defer cleanupPVC2()

		ginkgo.By("Deploying the first pod with the shared GCS Fuse volume and its own PD volume")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod1.SetupVolume(&storageframework.VolumeResource{Pvc: pvc1}, pdVolName, pdMountPath, false)
		tPod1.Create(ctx)
		defer tPod1.Cleanup(ctx)
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Deploying the second pod with the same shared GCS Fuse volume and its own PD volume")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc2}, pdVolName, pdMountPath, false)
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Writing a file to the shared GCS Fuse volume from the first pod")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'shared-from-pod1' > %v/shared.txt", gcsFuseMountPath))

		ginkgo.By("Verifying the second pod can see the file written by the first pod on the shared GCS volume")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'shared-from-pod1' %v/shared.txt", gcsFuseMountPath))

		ginkgo.By("Writing a file to the shared GCS Fuse volume from the second pod")
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'shared-from-pod2' > %v/shared-back.txt", gcsFuseMountPath))

		ginkgo.By("Verifying the first pod can see the file written by the second pod on the shared GCS volume")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'shared-from-pod2' %v/shared-back.txt", gcsFuseMountPath))

		ginkgo.By("Writing a file to each pod's own isolated PD volume")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'pod1-private-pd-data' > %v/private.txt", pdMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'pod2-private-pd-data' > %v/private.txt", pdMountPath))

		ginkgo.By("Verifying each pod's PD volume is isolated and not visible to the other pod")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'pod1-private-pd-data' %v/private.txt && ! grep -q 'pod2-private-pd-data' %v/private.txt",
				pdMountPath, pdMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'pod2-private-pd-data' %v/private.txt && ! grep -q 'pod1-private-pd-data' %v/private.txt",
				pdMountPath, pdMountPath))
	})

	// ── Test: Concurrent writes from two pods to a shared GCS Fuse bucket ──────
	// and their respective isolated PD volumes
	//
	// Two pods run simultaneously, each writing different files at the same
	// time to the shared GCS Fuse bucket mount and to its own PD-backed volume.
	// The test verifies neither driver corrupts data or surfaces mount errors
	// under concurrent load, and that every file lands intact on both the
	// shared GCS bucket and the writer's own PD volume.
	ginkgo.It("[Feature: GCSFuse-PDCSI] should support concurrent writes from two pods to a shared GCS Fuse bucket and their respective PD volumes without data corruption or mount errors", func() {
		skipIfPDCSINotInstalled("concurrent writes test")

		init()
		defer cleanup()

		pvc1, cleanupPVC1 := createPDPVC("concurrent-pd-pvc-1-", "5Gi")
		defer cleanupPVC1()
		pvc2, cleanupPVC2 := createPDPVC("concurrent-pd-pvc-2-", "5Gi")
		defer cleanupPVC2()

		ginkgo.By("Deploying the first pod with the shared GCS Fuse volume and its own PD volume")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod1.SetupVolume(&storageframework.VolumeResource{Pvc: pvc1}, pdVolName, pdMountPath, false)
		tPod1.Create(ctx)
		defer tPod1.Cleanup(ctx)
		tPod1.WaitForRunning(ctx)

		ginkgo.By("Deploying the second pod with the shared GCS Fuse volume and its own PD volume")
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod2.SetupVolume(&storageframework.VolumeResource{Pvc: pvc2}, pdVolName, pdMountPath, false)
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)
		tPod2.WaitForRunning(ctx)

		ginkgo.By("Concurrently writing different files from both pods to the shared GCS Fuse mount and their own PD volumes")
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()
			tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
				fmt.Sprintf("echo 'pod1-gcs-data' > %v/concurrent-pod1.txt && echo 'pod1-pd-data' > %v/pod1-data.txt",
					gcsFuseMountPath, pdMountPath))
		}()

		go func() {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()
			tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName,
				fmt.Sprintf("echo 'pod2-gcs-data' > %v/concurrent-pod2.txt && echo 'pod2-pd-data' > %v/pod2-data.txt",
					gcsFuseMountPath, pdMountPath))
		}()

		wg.Wait()

		ginkgo.By("Verifying both pods' GCS Fuse mounts remain healthy after concurrent writes")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", gcsFuseMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", gcsFuseMountPath))

		ginkgo.By("Verifying each pod can read its own file back from the shared GCS bucket without corruption")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'pod1-gcs-data' %v/concurrent-pod1.txt", gcsFuseMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'pod2-gcs-data' %v/concurrent-pod2.txt", gcsFuseMountPath))

		ginkgo.By("Verifying each pod can see the other pod's file in the shared GCS bucket with uncorrupted content")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'pod2-gcs-data' %v/concurrent-pod2.txt", gcsFuseMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'pod1-gcs-data' %v/concurrent-pod1.txt", gcsFuseMountPath))

		ginkgo.By("Verifying each pod's own PD volume holds its uncorrupted data")
		tPod1.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'pod1-pd-data' %v/pod1-data.txt", pdMountPath))
		tPod2.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("grep 'pod2-pd-data' %v/pod2-data.txt", pdMountPath))
	})

	// Large file transfer: a 1 GiB file is pre-seeded in GCS via the GCS API. The
	// pod copies it from the GCS Fuse mount to the PD volume. The test verifies the
	// transfer completes without timeout or mount disruption on either driver, and
	// that the sizes match.
	ginkgo.It("[Feature: GCSFuse-PDCSI] should transfer a 1 GiB file from the GCS Fuse mount to a PD-backed volume without timeout or mount disruption", func() {
		skipIfPDCSINotInstalled("large file transfer test")

		init()
		defer cleanup()

		bucketName := l.gcsFuseResource.Pv.Spec.CSI.VolumeHandle
		const largeFileName = "large-transfer-1g.bin"

		gcsfuseDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
		if !ok {
			framework.Failf("driver is not *specs.GCSFuseCSITestDriver, cannot pre-seed GCS object")
		}

		ginkgo.By(fmt.Sprintf("Pre-seeding 1 GiB file %q in GCS bucket %q", largeFileName, bucketName))
		gcsfuseDriver.CreateTestFileWithSizeInBucket(ctx, largeFileName, bucketName, largeFileSizeBytes)

		pvc, cleanupPVC := createPDPVC("large-transfer-pd-pvc-", "5Gi")
		defer cleanupPVC()

		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseVolName, gcsFuseMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, pdVolName, pdMountPath, false)

		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)
		tPod.WaitForRunning(ctx)

		ginkgo.By("Verifying the 1 GiB file is readable on the GCS Fuse mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test -f %v/%v", gcsFuseMountPath, largeFileName))

		ginkgo.By("Copying the 1 GiB file from the GCS Fuse mount to the PD mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("cp %v/%v %v/%v", gcsFuseMountPath, largeFileName, pdMountPath, largeFileName))

		ginkgo.By("Verifying the transferred file size matches the original on GCS")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test $(stat -c%%s %v/%v) -eq $(stat -c%%s %v/%v)",
				pdMountPath, largeFileName, gcsFuseMountPath, largeFileName))

		ginkgo.By("Verifying the GCS Fuse mount is still accessible after the large transfer")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test -f %v/%v", gcsFuseMountPath, largeFileName))
	})
}
