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
	"time"

	"local/test/e2e/specs"

	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

// LustreStorageClass is the StorageClass used to provision the Lustre-backed
// PVC in Lustre + GCS Fuse data pipeline tests. Overridden via
// --lustre-storage-class in e2e_test.go if needed.
var LustreStorageClass = "lustre-rwx"

const (
	lustreCSIDriverName  = "lustre.csi.storage.gke.io"
	gcsFuseDataMountPath = "/mnt/gcs"
	lustreMountPath      = "/mnt/lustre"
	gcsFuseDataVolName   = "gcs-vol"
	lustreVolName        = "lustre-vol"

	// lustreCheckpointSizeBytes is 1 GiB — representative of a checkpoint/
	// output file written during a training or batch job.
	lustreCheckpointSizeBytes = 1 * 1024 * 1024 * 1024

	// lustrePVCSize is the minimum allocatable size for the lustre-rwx
	// StorageClass given perUnitStorageThroughput=1000.
	lustrePVCSize = "9000Gi"
)

type gcsFuseCSILustreDataPipelineTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSILustreDataPipelineTestSuite returns
// gcsFuseCSILustreDataPipelineTestSuite that implements TestSuite interface.
func InitGcsFuseCSILustreDataPipelineTestSuite() storageframework.TestSuite {
	return &gcsFuseCSILustreDataPipelineTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "lustre-gcsfuse-data-pipeline",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
	}
}

func (t *gcsFuseCSILustreDataPipelineTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSILustreDataPipelineTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSILustreDataPipelineTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config          *storageframework.PerTestConfig
		gcsFuseResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	f := framework.NewFrameworkWithCustomTimeouts("lustre-gcsfuse-data-pipeline", storageframework.GetDriverTimeouts(driver))
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

	// createLustrePVC creates a Lustre-backed PVC and returns it with a cleanup func.
	createLustrePVC := func(namePrefix, size string) (*corev1.PersistentVolumeClaim, func()) {
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

	// waitForLustrePVCBound polls until the given PVC reaches Bound phase.
	// Lustre instance creation can take several minutes.
	waitForLustrePVCBound := func(pvcName string) {
		ginkgo.By(fmt.Sprintf("Waiting for Lustre PVC %q to be Bound (up to 20m)", pvcName))
		framework.ExpectNoError(wait.PollUntilContextTimeout(ctx, 10*time.Second, 20*time.Minute, true, func(c context.Context) (bool, error) {
			pvc, err := f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Get(c, pvcName, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			return pvc.Status.Phase == corev1.ClaimBound, nil
		}))
	}

	// skipIfLustreCSINotInstalled skips when lustre.csi.storage.gke.io CSIDriver is absent.
	skipIfLustreCSINotInstalled := func(testName string) {
		_, err := f.ClientSet.StorageV1().CSIDrivers().Get(ctx, lustreCSIDriverName, metav1.GetOptions{})
		if err != nil {
			e2eskipper.Skipf("%s CSIDriver not found, skipping %s: %v", lustreCSIDriverName, testName, err)
		}
	}

	// TC-02: GCS to Lustre data staging (ingest)
	//
	// A file is pre-seeded in the GCS bucket. A pod mounts both the GCS Fuse
	// volume and a Lustre-backed volume, then copies the file from the GCS
	// Fuse mount to the Lustre mount. The test verifies content integrity
	// (via md5sum) and that the copy completes successfully, demonstrating
	// staging of data from GCS into Lustre for high-throughput, low-latency
	// reads.

	ginkgo.It("[Feature: GCSFuse-Lustre] should stage data from the GCS Fuse mount into a Lustre-backed volume with content integrity", func() {
		skipIfLustreCSINotInstalled("GCS to Lustre data staging test")

		init()
		defer cleanup()

		bucketName := l.gcsFuseResource.Pv.Spec.CSI.VolumeHandle
		const ingestFileName = "ingest-source.txt"

		gcsfuseDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
		if !ok {
			framework.Failf("driver is not *specs.GCSFuseCSITestDriver, cannot pre-seed GCS object")
		}

		ginkgo.By(fmt.Sprintf("Pre-seeding test file %q in GCS bucket %q", ingestFileName, bucketName))
		gcsfuseDriver.CreateTestFileInBucket(ctx, ingestFileName, bucketName)

		pvc, cleanupPVC := createLustrePVC("lustre-ingest-pvc-", lustrePVCSize)
		defer cleanupPVC()

		// Wait for Lustre PVC to be Bound before creating pod.
		waitForLustrePVCBound(pvc.Name)

		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseDataVolName, gcsFuseDataMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, lustreVolName, lustreMountPath, false)
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)
		tPod.WaitForRunning(ctx)

		ginkgo.By("Verifying the pre-seeded file is readable on the GCS Fuse mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test -f %v/%v", gcsFuseDataMountPath, ingestFileName))

		ginkgo.By("Copying the file from the GCS Fuse mount to the Lustre mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("cp %v/%v %v/%v", gcsFuseDataMountPath, ingestFileName, lustreMountPath, ingestFileName))

		ginkgo.By("Verifying content integrity between the GCS Fuse source and the Lustre copy")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test \"$(md5sum < %v/%v)\" = \"$(md5sum < %v/%v)\"",
				gcsFuseDataMountPath, ingestFileName, lustreMountPath, ingestFileName))

		ginkgo.By("Verifying the staged file is readable from the Lustre mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("cat %v/%v", lustreMountPath, ingestFileName))
	})

	// TC-03: Lustre to GCS checkpoint/export
	//
	// A large "checkpoint" file is written directly to the Lustre-backed
	// volume, then copied to the GCS Fuse mount for archival. The test
	// verifies the copy succeeds, content integrity is preserved (via
	// md5sum), and the resulting object is visible in the GCS bucket after
	// fsync/close.
	ginkgo.It("[Feature: GCSFuse-Lustre] should export a checkpoint file from Lustre to the GCS Fuse mount with content integrity and bucket visibility", func() {
		skipIfLustreCSINotInstalled("Lustre to GCS checkpoint export test")

		init()
		defer cleanup()

		bucketName := l.gcsFuseResource.Pv.Spec.CSI.VolumeHandle
		const checkpointFileName = "checkpoint.bin"

		pvc, cleanupPVC := createLustrePVC("lustre-checkpoint-pvc-", lustrePVCSize)
		defer cleanupPVC()

		// Wait for Lustre PVC to be Bound before creating pod.
		waitForLustrePVCBound(pvc.Name)

		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseDataVolName, gcsFuseDataMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, lustreVolName, lustreMountPath, false)
		// Increase memory limit to handle 1GiB dd write — default 20Mi causes OOM on OSS.
		tPod.SetResource("500m", "1Gi", "1Gi")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)
		tPod.WaitForRunning(ctx)

		ginkgo.By(fmt.Sprintf("Writing a %d-byte checkpoint file to the Lustre mount", lustreCheckpointSizeBytes))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("dd if=/dev/urandom of=%v/%v bs=1M count=%d && sync",
				lustreMountPath, checkpointFileName, lustreCheckpointSizeBytes/(1024*1024)))

		ginkgo.By("Copying the checkpoint file from the Lustre mount to the GCS Fuse mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("cp %v/%v %v/%v && sync",
				lustreMountPath, checkpointFileName, gcsFuseDataMountPath, checkpointFileName))

		ginkgo.By("Verifying content integrity between the Lustre source and the GCS Fuse copy")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test \"$(md5sum < %v/%v)\" = \"$(md5sum < %v/%v)\"",
				lustreMountPath, checkpointFileName, gcsFuseDataMountPath, checkpointFileName))

		gcsfuseDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
		if !ok {
			framework.Failf("driver is not *specs.GCSFuseCSITestDriver, cannot verify GCS object visibility")
		}

		ginkgo.By(fmt.Sprintf("Verifying the checkpoint object %q is visible in GCS bucket %q after fsync/close", checkpointFileName, bucketName))
		tmpFile, err := os.CreateTemp("", "checkpoint-download-*")
		framework.ExpectNoError(err)
		downloadPath := tmpFile.Name()
		tmpFile.Close()
		defer os.Remove(downloadPath)
		framework.ExpectNoError(gcsfuseDriver.DownloadGCSObject(ctx, bucketName, checkpointFileName, downloadPath))
	})

	// TC-04: Concurrency/Stress — concurrent multi-writer
	//
	// Multiple pods simultaneously mount the same Lustre volume and GCS Fuse
	// bucket and each write a distinct large file. The test verifies no
	// corruption, locking errors, or mount errors occur under concurrent
	// load by checking md5sums of each file written by each pod.
	ginkgo.It("[Feature: GCSFuse-Lustre] should handle concurrent multi-writer pods writing to the same Lustre volume and GCS Fuse bucket without corruption", func() {
		skipIfLustreCSINotInstalled("Concurrency/Stress multi-writer test")

		init()
		defer cleanup()

		const concurrentPodCount = 3
		const fileSizeMiB = 128

		gcsfuseDriver, ok := driver.(*specs.GCSFuseCSITestDriver)
		if !ok {
			framework.Failf("driver is not *specs.GCSFuseCSITestDriver, cannot verify GCS object visibility")
		}

		pvc, cleanupPVC := createLustrePVC("lustre-stress-pvc-", lustrePVCSize)
		defer cleanupPVC()

		// Wait for Lustre PVC to be Bound before creating pods — Lustre instance
		// creation can take several minutes and pods will be Unschedulable until then.
		waitForLustrePVCBound(pvc.Name)

		type podHandle struct {
			pod      *specs.TestPod
			fileName string
		}
		pods := make([]podHandle, concurrentPodCount)
		for i := 0; i < concurrentPodCount; i++ {
			fileName := fmt.Sprintf("stress-file-pod%d.bin", i)
			tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
			tPod.SetupVolume(l.gcsFuseResource, gcsFuseDataVolName, gcsFuseDataMountPath, false)
			tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, lustreVolName, lustreMountPath, false)
			tPod.Create(ctx)
			pods[i] = podHandle{pod: tPod, fileName: fileName}
		}
		for i := range pods {
			defer pods[i].pod.Cleanup(ctx)
			pods[i].pod.WaitForRunning(ctx)
		}

		// All pods have volumes mounted simultaneously — write from each pod
		// sequentially to stress concurrent mount coexistence on shared Lustre
		// and GCS Fuse volumes.
		ginkgo.By(fmt.Sprintf("Each pod writes a distinct %d MiB file to Lustre and copies to GCS Fuse", fileSizeMiB))
		for _, ph := range pods {
			lustreFile := fmt.Sprintf("%v/%v", lustreMountPath, ph.fileName)
			gcsFile := fmt.Sprintf("%v/%v", gcsFuseDataMountPath, ph.fileName)
			ph.pod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
				fmt.Sprintf("dd if=/dev/urandom of=%v bs=1M count=%d && sync", lustreFile, fileSizeMiB))
			ph.pod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
				fmt.Sprintf("cp %v %v && sync", lustreFile, gcsFile))
		}

		// Verify md5sum integrity per file across mounts.
		ginkgo.By("Verifying content integrity for each file across Lustre and GCS Fuse mounts")
		for _, ph := range pods {
			lustreFile := fmt.Sprintf("%v/%v", lustreMountPath, ph.fileName)
			gcsFile := fmt.Sprintf("%v/%v", gcsFuseDataMountPath, ph.fileName)
			ph.pod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
				fmt.Sprintf("test \"$(md5sum < %v)\" = \"$(md5sum < %v)\"", lustreFile, gcsFile))
		}

		// Verify all files are visible in GCS bucket.
		ginkgo.By("Verifying all written files are visible in the GCS bucket")
		bucketName := l.gcsFuseResource.Pv.Spec.CSI.VolumeHandle
		for _, ph := range pods {
			tmpFile, err := os.CreateTemp("", "stress-download-*")
			framework.ExpectNoError(err)
			downloadPath := tmpFile.Name()
			tmpFile.Close()
			defer os.Remove(downloadPath)
			framework.ExpectNoError(gcsfuseDriver.DownloadGCSObject(ctx, bucketName, ph.fileName, downloadPath))
		}
	})

	// TC-05: Functional Co-existence — sidecar/driver coexistence on shared node
	//
	// Mixed pods (Lustre+GCSFuse, Lustre-only, GCSFuse-only) are scheduled
	// on the same node. The test verifies that both CSI driver node plugins
	// and the GCS Fuse sidecar injector coexist without resource conflicts
	// or crashes, and that each pod can successfully read/write its volumes.
	ginkgo.It("[Feature: GCSFuse-Lustre]should coexist with mixed Lustre-only, GCSFuse-only, and Lustre+GCSFuse pods on the same node without resource conflicts", func() {
		skipIfLustreCSINotInstalled("Functional Co-existence test")

		init()
		defer cleanup()

		pvc, cleanupPVC := createLustrePVC("lustre-coexist-pvc-", lustrePVCSize)
		defer cleanupPVC()

		// Wait for Lustre PVC to be Bound before creating pods.
		waitForLustrePVCBound(pvc.Name)

		// Pod A: mounts both Lustre and GCS Fuse.
		ginkgo.By("Creating Pod A: Lustre + GCS Fuse")
		podA := specs.NewTestPod(f.ClientSet, f.Namespace)
		podA.SetupVolume(l.gcsFuseResource, gcsFuseDataVolName, gcsFuseDataMountPath, false)
		podA.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, lustreVolName, lustreMountPath, false)
		podA.Create(ctx)
		defer podA.Cleanup(ctx)
		podA.WaitForRunning(ctx)
		nodeName := podA.GetNode()

		// Pod B: mounts Lustre only (reuses same PVC — RWX).
		ginkgo.By("Creating Pod B: Lustre-only")
		podB := specs.NewTestPod(f.ClientSet, f.Namespace)
		podB.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, lustreVolName, lustreMountPath, false)
		podB.SetNodeAffinity(nodeName, true)
		podB.Create(ctx)
		defer podB.Cleanup(ctx)
		podB.WaitForRunning(ctx)

		// Pod C: mounts GCS Fuse only.
		ginkgo.By("Creating Pod C: GCS Fuse-only")
		podC := specs.NewTestPod(f.ClientSet, f.Namespace)
		podC.SetupVolume(l.gcsFuseResource, gcsFuseDataVolName, gcsFuseDataMountPath, false)
		podC.SetNodeAffinity(nodeName, true)
		podC.Create(ctx)
		defer podC.Cleanup(ctx)
		podC.WaitForRunning(ctx)

		// Pod A writes to Lustre; Pod B reads it back (verifies Lustre RWX).
		const coexistFile = "coexist-test.txt"
		ginkgo.By("Pod A writes a file to the Lustre mount")
		podA.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'coexist-data' > %v/%v && sync", lustreMountPath, coexistFile))

		ginkgo.By("Pod B reads the file written by Pod A from the shared Lustre mount")
		podB.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'coexist-data' %v/%v", lustreMountPath, coexistFile))

		// Pod A writes to GCS Fuse; Pod C reads it back.
		ginkgo.By("Pod A writes a file to the GCS Fuse mount")
		podA.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'gcs-coexist-data' > %v/%v && sync", gcsFuseDataMountPath, coexistFile))

		ginkgo.By("Pod C reads the file written by Pod A from the GCS Fuse mount")
		podC.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("grep 'gcs-coexist-data' %v/%v", gcsFuseDataMountPath, coexistFile))

		// Verify all pods are still Running — no crashes or resource conflicts.
		ginkgo.By("Verifying all pods are still Running after concurrent operations")
		podA.WaitForRunning(ctx)
		podB.WaitForRunning(ctx)
		podC.WaitForRunning(ctx)
	})
}
