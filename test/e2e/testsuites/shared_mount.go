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
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/util/retry"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
	"k8s.io/utils/ptr"
	"local/test/e2e/specs"
	"local/test/e2e/utils"
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

		// Attempt to create a single Pod referencing both volumes, and verify that the mutating webhook rejects the Pod creation attempt.
		ginkgo.By("Attempting to create a single Pod referencing both sidecar and shared-mount volumes")
		mixedPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		mixedPod.SetupVolume(sidecarVR, sidecarVolName, sidecarMountPath, false /* readOnly */)
		mixedPod.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)

		ginkgo.By("Verifying that the mutating webhook rejects the mixed Pod creation attempt")
		mixedPod.CreateExpectErrorContaining(ctx, "mixing shared node mount and non-shared node mount GCSFuse volumes in the same Pod is not allowed")

		// Create sidecarTestPod referencing the sidecar volume and sharedMountTestPod referencing the shared mount PVC on the same node.
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

		// Verify sidecarTestPod has a sidecar container injected while sharedMountTestPod does not, and a single Mounter Pod is created for sharedMountTestPod.
		ginkgo.By("Verifying sidecarTestPod has a sidecar container injected")
		sidecarTestPod.VerifySidecarPresence(true /* expectPresent */)

		ginkgo.By("Verifying sharedMountTestPod does NOT have a sidecar container injected")
		sharedMountTestPod.VerifySidecarPresence(false /* expectPresent */)

		ginkgo.By("Verifying a single Mounter Pod is created for sharedMountTestPod on the same node")
		specs.VerifyMounterPods(ctx, f.ClientSet, f.Namespace.Name, 1, nodeName)

		// Verify that both pods can successfully read and write to their respective volumes without conflicts.
		ginkgo.By("Verifying sidecarTestPod can write and read from its sidecar-mounted volume")
		sidecarTestPod.VerifyRWMount(f, sidecarMountPath)
		sidecarTestPod.VerifyWriteAndReadFile(f, fmt.Sprintf("%s/data-sidecar", sidecarMountPath), "hello from sidecar pod")

		ginkgo.By("Verifying sharedMountTestPod can write and read from its shared-mounted volume")
		sharedMountTestPod.VerifyRWMount(f, sharedMountPath)
		sharedMountTestPod.VerifyWriteAndReadFile(f, fmt.Sprintf("%s/data-shared", sharedMountPath), "hello from shared mount pod")

		ginkgo.By("Verifying data persistence and isolation on both volumes")
		sidecarTestPod.VerifyReadFile(f, fmt.Sprintf("%s/data-sidecar", sidecarMountPath), "hello from sidecar pod")
		sharedMountTestPod.VerifyReadFile(f, fmt.Sprintf("%s/data-shared", sharedMountPath), "hello from shared mount pod")
	})

	// TC: Dynamic Mounting Test
	// Verify that dynamic mounting works with the shared node mount architecture.
	// Create a PV with volumeHandle: _ and sharedMount: true.
	// Create multiple Pods referencing the PVC.
	// Verify the Mounter Pod is created.
	// Verify all pods can successfully read from and write to any buckets their KSA is authorized to access.
	ginkgo.It("[shared-mount] should verify dynamic mounting across multiple pods with volumeHandle _", func() {
		init(1, specs.SharedDynamicMountPrefix)
		defer cleanup()

		gomega.Expect(l.volumeResourceList).To(gomega.HaveLen(1))
		gomega.Expect(l.volumeResourceList[0]).ToNot(gomega.BeNil())

		sharedVR := l.volumeResourceList[0]
		buckets := strings.Split(l.config.Prefix, ",")
		gomega.Expect(buckets).To(gomega.HaveLen(2), "expected 2 buckets created for dynamic mounting")

		// Configure and deploy Pod 1 referencing the dynamic shared-mount PVC.
		ginkgo.By("Configuring and deploying the first pod referencing the dynamic shared-mount PVC")
		tPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod1.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)
		tPod1.Create(ctx)
		defer tPod1.Cleanup(ctx)

		ginkgo.By("Waiting for the first pod to be running and getting its node")
		tPod1.WaitForRunning(ctx)
		nodeName := tPod1.GetNode()

		// Configure and deploy Pod 2 on the same node referencing the same PVC.
		ginkgo.By(fmt.Sprintf("Configuring and deploying the second pod on node %s referencing the same PVC", nodeName))
		tPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod2.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)
		tPod2.SetNodeAffinity(nodeName, true /* sameNode */)
		tPod2.Create(ctx)
		defer tPod2.Cleanup(ctx)

		ginkgo.By("Waiting for the second pod to be running")
		tPod2.WaitForRunning(ctx)
		gomega.Expect(tPod2.GetNode()).To(gomega.Equal(nodeName), "expected second pod to run on the same node as first pod")

		// Verify sidecar absence on client pods and exactly 1 Mounter Pod created on the node.
		ginkgo.By("Verifying client pods do NOT have sidecar containers injected")
		tPod1.VerifySidecarPresence(false /* expectPresent */)
		tPod2.VerifySidecarPresence(false /* expectPresent */)

		ginkgo.By("Verifying exactly 1 Mounter Pod is created for the dynamic shared-mount volume on the node")
		specs.VerifyMounterPods(ctx, f.ClientSet, f.Namespace.Name, 1, nodeName)

		// Verify RW mount point in both pods.
		ginkgo.By("Verifying RW mount point in both pods")
		tPod1.VerifyRWMount(f, sharedMountPath)
		tPod2.VerifyRWMount(f, sharedMountPath)

		// Verify dynamic multi-bucket read and write operations across both pods.
		ginkgo.By("Verifying both pods can read and write across all authorized buckets")
		for _, bucket := range buckets {
			pod1File := fmt.Sprintf("%s/%s/pod1-data.txt", sharedMountPath, bucket)
			pod2File := fmt.Sprintf("%s/%s/pod2-data.txt", sharedMountPath, bucket)
			pod1Content := fmt.Sprintf("hello from pod1 in bucket %s", bucket)
			pod2Content := fmt.Sprintf("hello from pod2 in bucket %s", bucket)

			// Pod 1 writes and reads its own file in this bucket
			tPod1.VerifyWriteAndReadFile(f, pod1File, pod1Content)

			// Pod 2 writes and reads its own file in this bucket
			tPod2.VerifyWriteAndReadFile(f, pod2File, pod2Content)

			// Cross-pod read: Pod 1 reads Pod 2's file, Pod 2 reads Pod 1's file
			tPod1.VerifyReadFile(f, pod2File, pod2Content)
			tPod2.VerifyReadFile(f, pod1File, pod1Content)
		}
	})

	// TC: PodTemplate Overrides and Webhook Enforcement Test
	// Verify that custom PodTemplate settings (Container Image, CPU/Memory/EphemeralStorage Requests & Limits,
	// custom Buffer EmptyDir volume, custom Cache EmptyDir volume and user-created cache label, DNSPolicy,
	// fsGroup, ServiceAccountName) are correctly applied to the created Mounter Pod, that the valid client pod
	// can perform read and write operations on the shared mount, and that the mutating webhook rejects client pods
	// with a mismatched KSA (both before and after Mounter Pod creation) or a mismatched fsGroup.
	ginkgo.It("[shared-mount] should verify PodTemplate overrides are applied to Mounter Pod and webhook rejects mismatched KSA and fsGroup", func() {
		init(1)
		defer cleanup()

		gomega.Expect(l.volumeResourceList).To(gomega.HaveLen(1))
		gomega.Expect(l.volumeResourceList[0]).ToNot(gomega.BeNil())
		sharedVR := l.volumeResourceList[0]
		gomega.Expect(sharedVR.Pvc).ToNot(gomega.BeNil())

		templateName := "custom-mounter-template-" + rand.String(6)
		customImage := specs.LastPublishedMounterPodSidecarContainerImage
		customFSGroup := int64(3000)
		mismatchedFSGroup := int64(4000)
		customDNSPolicy := corev1.DNSClusterFirst
		customCPUReq := resource.MustParse("250m")
		customMemReq := resource.MustParse("256Mi")
		customEphemeralStorageReq := resource.MustParse("10Gi")
		customCPULim := resource.MustParse("500m")
		customMemLim := resource.MustParse("512Mi")
		customEphemeralStorageLim := resource.MustParse("20Gi")
		customBufferSize := resource.MustParse("128Mi")
		customCacheSize := resource.MustParse("256Mi")

		customResources := &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:              customCPUReq,
				corev1.ResourceMemory:           customMemReq,
				corev1.ResourceEphemeralStorage: customEphemeralStorageReq,
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:              customCPULim,
				corev1.ResourceMemory:           customMemLim,
				corev1.ResourceEphemeralStorage: customEphemeralStorageLim,
			},
		}

		customBufferVolume := corev1.Volume{
			Name: webhook.SidecarContainerBufferVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: ptr.To(customBufferSize),
				},
			},
		}

		customCacheVolume := corev1.Volume{
			Name: webhook.SidecarContainerCacheVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: ptr.To(customCacheSize),
				},
			},
		}

		// Create the custom PodTemplate.
		ginkgo.By("Creating a custom PodTemplate with resource overrides, custom buffer/cache volumes, image, fsGroup, DNSPolicy, and KSA")
		_, err := specs.CreateMounterPodTemplate(ctx, f.ClientSet, f.Namespace.Name, specs.MounterPodTemplateOptions{
			Name:               templateName,
			ServiceAccountName: specs.K8sServiceAccountName,
			FSGroup:            ptr.To(customFSGroup),
			Resources:          customResources,
			Volumes:            []corev1.Volume{customBufferVolume, customCacheVolume},
			DNSPolicy:          customDNSPolicy,
			Image:              customImage,
		})
		framework.ExpectNoError(err, "failed to create custom mounter pod template")

		// Annotate the PVC with the custom PodTemplate.
		// Use RetryOnConflict to fetch the latest PVC resourceVersion (updated when transitioned to Bound
		// by pv-controller or other background controllers) to prevent 409 Conflict errors.
		ginkgo.By(fmt.Sprintf("Annotating the PVC %s with the custom PodTemplate %s", sharedVR.Pvc.Name, templateName))
		var updatedPVC *corev1.PersistentVolumeClaim
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			currentPVC, getErr := f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Get(ctx, sharedVR.Pvc.Name, metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			if currentPVC.Annotations == nil {
				currentPVC.Annotations = make(map[string]string)
			}
			currentPVC.Annotations[webhook.MounterPodTemplateAnnotation] = templateName
			var updateErr error
			updatedPVC, updateErr = f.ClientSet.CoreV1().PersistentVolumeClaims(f.Namespace.Name).Update(ctx, currentPVC, metav1.UpdateOptions{})
			return updateErr
		})
		framework.ExpectNoError(err, "failed to update PVC with custom pod template annotation")
		sharedVR.Pvc = updatedPVC

		// Create a valid mismatched KSA in the namespace to verify webhook enforcement.
		mismatchedKSAName := "mismatched-ksa-" + rand.String(6)
		mismatchedKSA := utils.NewTestKubernetesServiceAccount(f.ClientSet, f.Namespace, mismatchedKSAName, "")
		mismatchedKSA.Create(ctx)
		defer mismatchedKSA.Cleanup(ctx)

		// Negative validation: Verify webhook rejects client pods with invalid configurations before Mounter Pod exists.
		ginkgo.By("Verifying mutating webhook rejects a client pod with mismatched KSA before Mounter Pod exists")
		mismatchedKSAPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		mismatchedKSAPod1.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)
		mismatchedKSAPod1.SetServiceAccount(mismatchedKSAName)
		mismatchedKSAPod1.SetNonRootSecurityContext(0, 0, int(customFSGroup))
		mismatchedKSAPod1.CreateExpectErrorContaining(ctx, "does not match the one specified in volume's PodTemplate")

		ginkgo.By("Verifying mutating webhook rejects a client pod with mismatched fsGroup before Mounter Pod exists")
		mismatchedFSGroupPod1 := specs.NewTestPod(f.ClientSet, f.Namespace)
		mismatchedFSGroupPod1.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)
		mismatchedFSGroupPod1.SetServiceAccount(specs.K8sServiceAccountName)
		mismatchedFSGroupPod1.SetNonRootSecurityContext(0, 0, int(mismatchedFSGroup))
		mismatchedFSGroupPod1.CreateExpectErrorContaining(ctx, "does not match the one specified in volume's PodTemplate")

		// Deploy valid client pod referencing the custom-annotated PVC.
		ginkgo.By("Configuring and deploying a valid client pod referencing the custom-annotated PVC")
		validPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		validPod.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)
		validPod.SetServiceAccount(specs.K8sServiceAccountName)
		validPod.SetNonRootSecurityContext(0, 0, int(customFSGroup))
		validPod.Create(ctx)
		defer validPod.Cleanup(ctx)

		ginkgo.By("Waiting for the valid client pod to be running")
		validPod.WaitForRunning(ctx)
		nodeName := validPod.GetNode()

		// Verify the created Mounter Pod spec accurately reflects the injected configurations.
		ginkgo.By("Verifying the created Mounter Pod reflects the custom PodTemplate overrides")
		mounterPods := specs.VerifyMounterPods(ctx, f.ClientSet, f.Namespace.Name, 1, nodeName)
		mounterPod := &mounterPods.Items[0]

		// Assert Container Resources
		gomega.Expect(mounterPod.Spec.Containers).ToNot(gomega.BeEmpty())
		mounterContainer := mounterPod.Spec.Containers[0]

		cpuReq := mounterContainer.Resources.Requests.Cpu()
		gomega.Expect(cpuReq).ToNot(gomega.BeNil())
		gomega.Expect(cpuReq.Cmp(customCPUReq)).To(gomega.BeZero())

		memReq := mounterContainer.Resources.Requests.Memory()
		gomega.Expect(memReq).ToNot(gomega.BeNil())
		gomega.Expect(memReq.Cmp(customMemReq)).To(gomega.BeZero())

		ephemeralStorageReq := mounterContainer.Resources.Requests.StorageEphemeral()
		gomega.Expect(ephemeralStorageReq).ToNot(gomega.BeNil())
		gomega.Expect(ephemeralStorageReq.Cmp(customEphemeralStorageReq)).To(gomega.BeZero())

		cpuLim := mounterContainer.Resources.Limits.Cpu()
		gomega.Expect(cpuLim).ToNot(gomega.BeNil())
		gomega.Expect(cpuLim.Cmp(customCPULim)).To(gomega.BeZero())

		memLim := mounterContainer.Resources.Limits.Memory()
		gomega.Expect(memLim).ToNot(gomega.BeNil())
		gomega.Expect(memLim.Cmp(customMemLim)).To(gomega.BeZero())

		ephemeralStorageLim := mounterContainer.Resources.Limits.StorageEphemeral()
		gomega.Expect(ephemeralStorageLim).ToNot(gomega.BeNil())
		gomega.Expect(ephemeralStorageLim.Cmp(customEphemeralStorageLim)).To(gomega.BeZero())

		// Assert Container Image
		gomega.Expect(mounterContainer.Image).To(gomega.Equal(customImage))

		// Assert fsGroup
		gomega.Expect(mounterPod.Spec.SecurityContext).ToNot(gomega.BeNil())
		gomega.Expect(mounterPod.Spec.SecurityContext.FSGroup).ToNot(gomega.BeNil())
		gomega.Expect(ptr.Deref(mounterPod.Spec.SecurityContext.FSGroup, 0)).To(gomega.Equal(customFSGroup))

		// Assert ServiceAccountName
		gomega.Expect(mounterPod.Spec.ServiceAccountName).To(gomega.Equal(specs.K8sServiceAccountName))

		// Assert DNSPolicy
		gomega.Expect(mounterPod.Spec.DNSPolicy).To(gomega.Equal(customDNSPolicy))

		// Assert User-Created Cache Label
		gomega.Expect(mounterPod.Labels).To(gomega.HaveKeyWithValue(webhook.GcsfuseCacheCreatedByUserLabel, "true"))

		// Assert Custom Buffer Volume
		var foundBufferVolume *corev1.Volume
		for i := range mounterPod.Spec.Volumes {
			if mounterPod.Spec.Volumes[i].Name == webhook.SidecarContainerBufferVolumeName {
				foundBufferVolume = &mounterPod.Spec.Volumes[i]
				break
			}
		}
		gomega.Expect(foundBufferVolume).ToNot(gomega.BeNil(), "expected custom gke-gcsfuse-buffer volume in Mounter Pod spec")
		gomega.Expect(foundBufferVolume.EmptyDir).ToNot(gomega.BeNil(), "expected EmptyDir volume source for gke-gcsfuse-buffer")
		gomega.Expect(foundBufferVolume.EmptyDir.Medium).To(gomega.Equal(corev1.StorageMediumMemory))
		gomega.Expect(foundBufferVolume.EmptyDir.SizeLimit).ToNot(gomega.BeNil())
		gomega.Expect(foundBufferVolume.EmptyDir.SizeLimit.Cmp(customBufferSize)).To(gomega.BeZero())

		// Assert Custom Cache Volume
		var foundCacheVolume *corev1.Volume
		for i := range mounterPod.Spec.Volumes {
			if mounterPod.Spec.Volumes[i].Name == webhook.SidecarContainerCacheVolumeName {
				foundCacheVolume = &mounterPod.Spec.Volumes[i]
				break
			}
		}
		gomega.Expect(foundCacheVolume).ToNot(gomega.BeNil(), "expected custom gke-gcsfuse-cache volume in Mounter Pod spec")
		gomega.Expect(foundCacheVolume.EmptyDir).ToNot(gomega.BeNil(), "expected EmptyDir volume source for gke-gcsfuse-cache")
		gomega.Expect(foundCacheVolume.EmptyDir.Medium).To(gomega.Equal(corev1.StorageMediumMemory))
		gomega.Expect(foundCacheVolume.EmptyDir.SizeLimit).ToNot(gomega.BeNil())
		gomega.Expect(foundCacheVolume.EmptyDir.SizeLimit.Cmp(customCacheSize)).To(gomega.BeZero())

		// Verify valid client pod read and write operations.
		ginkgo.By("Verifying valid client pod can read and write to the shared mount")
		validPod.VerifyRWMount(f, sharedMountPath)
		validPod.VerifyWriteAndReadFile(f, fmt.Sprintf("%s/data-template-override.txt", sharedMountPath), "hello from template override test")

		// Negative validation: Verify webhook rejects a client pod with mismatched KSA while Mounter Pod is running.
		ginkgo.By("Verifying mutating webhook rejects a client pod with mismatched KSA while Mounter Pod is running")
		mismatchedKSAPod2 := specs.NewTestPod(f.ClientSet, f.Namespace)
		mismatchedKSAPod2.SetupVolume(sharedVR, sharedVolName, sharedMountPath, false /* readOnly */)
		mismatchedKSAPod2.SetServiceAccount(mismatchedKSAName)
		mismatchedKSAPod2.SetNonRootSecurityContext(0, 0, int(customFSGroup))
		mismatchedKSAPod2.CreateExpectErrorContaining(ctx, "does not match the one specified in volume's PodTemplate")
	})
}
