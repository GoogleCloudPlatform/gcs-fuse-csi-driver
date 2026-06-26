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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

// LustreStorageClass is the StorageClass used to dynamically provision
// Lustre-backed PVCs in the GCS Fuse + Lustre performance/resilience tests.
// Overridden via --lustre-storage-class in e2e_test.go. If empty, these tests
// are skipped.
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

type gcsFuseLustrePerfResilienceTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseLustrePerfResilienceTestSuite returns
// gcsFuseLustrePerfResilienceTestSuite that implements TestSuite interface.
func InitGcsFuseLustrePerfResilienceTestSuite() storageframework.TestSuite {
	return &gcsFuseLustrePerfResilienceTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "gcsfuse-lustre-perf-resilience",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
	}
}

func (t *gcsFuseLustrePerfResilienceTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseLustrePerfResilienceTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseLustrePerfResilienceTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config          *storageframework.PerTestConfig
		gcsFuseResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	f := framework.NewFrameworkWithCustomTimeouts("gcsfuse-lustre-perf-resilience", storageframework.GetDriverTimeouts(driver))
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

	// Large file / high-throughput transfer: writes a 1 GB file on Lustre,
	// copies it to GCS Fuse and back, verifies checksums match in both
	// directions, and confirms both mounts remain stable throughout.
	ginkgo.It("should transfer a 1GB file between Lustre and GCS Fuse mounts with matching checksums and no mount instability", func() {
		skipIfLustreNotAvailable("large file high-throughput transfer test")

		init()
		defer cleanup()

		ginkgo.By("Creating a dynamically provisioned Lustre PVC")
		pvc, cleanupPVC := createLustrePVC("lustre-largefile-pvc-")
		defer cleanupPVC()

		ginkgo.By("Configuring the pod with both GCS Fuse and Lustre volumes")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)
		tPod.WaitForRunning(ctx)

		ginkgo.By("Writing a 1 GB file to the Lustre mount")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("dd if=/dev/urandom of=%v/large.bin bs=1M count=1024 conv=fsync", gcsFuseLustreMountPath))

		ginkgo.By("Computing checksum of the source file on Lustre")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("md5sum %v/large.bin > /tmp/lustre.md5", gcsFuseLustreMountPath))

		ginkgo.By("Copying the 1 GB file from Lustre to GCS Fuse")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("cp %v/large.bin %v/large.bin", gcsFuseLustreMountPath, gcsFuseLustreGCSMountPath))

		ginkgo.By("Verifying the checksum of the file on GCS Fuse matches the Lustre source")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf(
				"md5sum %v/large.bin > /tmp/gcs.md5 && "+
					"LUSTRE_SUM=$(awk '{print $1}' /tmp/lustre.md5) && "+
					"GCS_SUM=$(awk '{print $1}' /tmp/gcs.md5) && "+
					"[ \"$LUSTRE_SUM\" = \"$GCS_SUM\" ]",
				gcsFuseLustreGCSMountPath))

		ginkgo.By("Copying the file back from GCS Fuse to Lustre")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("cp %v/large.bin %v/large-from-gcs.bin", gcsFuseLustreGCSMountPath, gcsFuseLustreMountPath))

		ginkgo.By("Verifying the round-trip checksum matches the original")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf(
				"md5sum %v/large-from-gcs.bin > /tmp/back.md5 && "+
					"LUSTRE_SUM=$(awk '{print $1}' /tmp/lustre.md5) && "+
					"BACK_SUM=$(awk '{print $1}' /tmp/back.md5) && "+
					"[ \"$LUSTRE_SUM\" = \"$BACK_SUM\" ]",
				gcsFuseLustreMountPath))

		ginkgo.By("Verifying both mounts remain stable after large transfers")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("mount | grep %v", gcsFuseLustreMountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("mount | grep %v", gcsFuseLustreGCSMountPath))
	})

	// Mixed I/O pattern: many small-file reads/writes on GCS Fuse run
	// concurrently with sequential large-file I/O on Lustre. Verifies no
	// cross-driver resource contention and that both mounts remain healthy.
	ginkgo.It("should handle concurrent small-file I/O on GCS Fuse and sequential large-file I/O on Lustre without contention", func() {
		skipIfLustreNotAvailable("mixed I/O pattern test")

		init()
		defer cleanup()

		ginkgo.By("Creating a dynamically provisioned Lustre PVC")
		pvc, cleanupPVC := createLustrePVC("lustre-mixedio-pvc-")
		defer cleanupPVC()

		ginkgo.By("Configuring the pod with both GCS Fuse and Lustre volumes")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.gcsFuseResource, gcsFuseLustreGCSVolName, gcsFuseLustreGCSMountPath, false)
		tPod.SetupVolume(&storageframework.VolumeResource{Pvc: pvc}, gcsFuseLustreVolName, gcsFuseLustreMountPath, false)
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)
		tPod.WaitForRunning(ctx)

		ginkgo.By("Running concurrent GCS Fuse small-file writes and Lustre sequential large-file I/O")
		// Launches 100 small-file writes to GCS Fuse in the background while
		// simultaneously performing a 512 MB sequential write + read on Lustre,
		// then waits for the GCS workload to finish before asserting exit codes.
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf(
				"sh -c 'set -e; "+
					"for i in $(seq 1 100); do echo \"small-${i}\" > %v/small-${i}.txt; done & GCS_PID=$!; "+
					"dd if=/dev/zero of=%v/seq.bin bs=1M count=512 conv=fsync; "+
					"dd if=%v/seq.bin of=/dev/null bs=1M; "+
					"wait $GCS_PID'",
				gcsFuseLustreGCSMountPath, gcsFuseLustreMountPath, gcsFuseLustreMountPath))

		ginkgo.By("Verifying a sample of small files written to GCS Fuse are readable")
		for _, i := range []int{1, 25, 50, 75, 100} {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
				fmt.Sprintf("grep 'small-%d' %v/small-%d.txt", i, gcsFuseLustreGCSMountPath, i))
		}

		ginkgo.By("Verifying the sequential Lustre file is intact")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("test -f %v/seq.bin && stat -c%%s %v/seq.bin | grep -q 536870912",
				gcsFuseLustreMountPath, gcsFuseLustreMountPath))

		ginkgo.By("Verifying both mounts remain healthy after mixed I/O")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("mount | grep %v", gcsFuseLustreMountPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("mount | grep %v", gcsFuseLustreGCSMountPath))
	})
}
