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
	"os"
	"strings"
	"sync"
	"time"

	specs "local/test/e2e/specs"
	utils "local/test/e2e/utils"

	putil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2epv "k8s.io/kubernetes/test/e2e/framework/pv"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"

	// The client library for IAM Admin API
	iamadmin "cloud.google.com/go/iam/admin/apiv1"
	control "cloud.google.com/go/storage/control/apiv2"
	"cloud.google.com/go/storage/control/apiv2/controlpb"

	// The protocol buffer definitions for the IAM Admin API
	adminpb "cloud.google.com/go/iam/admin/apiv1/adminpb"
)

const (
	// StorageClass param keys.
	workloadTypeKey                          = "workloadType"
	workloadTypeServingKey                   = "serving"
	workloadTypeTrainingKey                  = "training"
	workloadTypeCheckpointingKey             = "checkpointing"
	fuseFileCacheMediumPriorityKey           = "fuseFileCacheMediumPriority"
	fuseMemoryAllocatableFactorKey           = "fuseMemoryAllocatableFactor"
	fuseEphemeralStorageAllocatableFactorKey = "fuseEphemeralStorageAllocatableFactor"
	gcsfuseMetadataPrefetchOnMountKey        = "gcsfuseMetadataPrefetchOnMount"
	enableAnywhereCacheKey                   = "enableAnywhereCache"
	volumeContextKeySkipCSIBucketAccessCheck = "skipCSIBucketAccessCheck"

	roleID               = "gke.gcsfuse.profileUser"
	gcsFuseCsiDriverName = "gcsfuse.csi.storage.gke.io"

	metadataStatCacheMaxSizeMiBMountOptionKey = "metadata-cache:stat-cache-max-size-mb"
	metadataTypeCacheMaxSizeMiBMountOptionKey = "metadata-cache:type-cache-max-size-mb"
	fileCacheSizeMiBMountOptionKey            = "file-cache:max-size-mb"

	volumeAttributeAnywhereCacheAdmissionPolicy = "anywhereCacheAdmissionPolicy"
	volumeAttributeEnableAnywhereCache          = "enableAnywhereCache"
	volumeAttributeAnywhereCacheTTL             = "anywhereCacheTTL"

	acBackoffDuration = 22 * time.Second
	acBackoffFactor   = 1.1
	acBackoffCap      = 3600 * time.Second
	acBackoffSteps    = 30
	acBackoffJitter   = 0.1

	gcsFuseCsiRecommendationLog = "GCS Fuse CSI recommendation:"

	acCreating = "creating"
	acReady    = "running"
)

var (
	vbm               = storagev1.VolumeBindingWaitForFirstConsumer
	setupOnce         sync.Once
	availableProfiles = []string{"gcsfusecsi-serving", "gcsfusecsi-training", "gcsfusecsi-checkpointing"}
	projectID         string
	projectNumber     string
)

type gcsFuseCSIProfilesTestSuite struct {
	tsInfo                       storageframework.TestSuiteInfo
	storageControlClient         *control.StorageControlClient // Used to validate anywherecahe.
	anywhereCachesNeedingCleanup []string
}

// InitGcsFuseCSIProfilesTestSuite returns gcsFuseCSIMultiVolumeTestSuite that implements TestSuite interface.
func InitGcsFuseCSIProfilesTestSuite() storageframework.TestSuite {
	storageControlClient, err := control.NewStorageControlClient(context.Background())
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create storage control client")

	return &gcsFuseCSIProfilesTestSuite{
		storageControlClient: storageControlClient,
		tsInfo: storageframework.TestSuiteInfo{
			Name: "profiles",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsPreprovisionedPV,
			},
		},
		anywhereCachesNeedingCleanup: []string{},
	}
}

func (t *gcsFuseCSIProfilesTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIProfilesTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func createIAMRoleForProfilesTests(ctx context.Context, config *storageframework.PerTestConfig, projectNumber string, projectID string) error {
	ginkgo.By("Creating IAM Role for GCSFuse Profiles Tests")

	iamClient, err := iamadmin.NewIamClient(ctx)
	if err != nil {
		return fmt.Errorf("Failed to create IAM client: %v", err)
	}
	defer iamClient.Close()

	createRoleReq := &adminpb.CreateRoleRequest{
		Parent: fmt.Sprintf("projects/%s", projectID),
		RoleId: roleID,
		Role: &adminpb.Role{
			Title:       "GCSFuse CSI Profile Tuner",
			Description: "Allows scanning GCS buckets for objects and retrieving bucket metadata and the creation of Anywhere Caches.",
			IncludedPermissions: []string{
				"storage.objects.list",
				"storage.buckets.get",
				"storage.anywhereCaches.create",
				"storage.anywhereCaches.get",
				"storage.anywhereCaches.list",
			},
		},
	}

	_, err = iamClient.CreateRole(ctx, createRoleReq)
	if err != nil {
		if status.Code(err) != codes.AlreadyExists {
			return fmt.Errorf("Failed to create IAM role: %v", err)
		}
	}
	return nil
}

func (t *gcsFuseCSIProfilesTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {

	type local struct {
		config             *storageframework.PerTestConfig
		volumeResourceList []*storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("profiles", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		projectID := os.Getenv(utils.ProjectEnvVar)
		gomega.Expect(projectID).NotTo(gomega.BeEmpty())
		projectNumber := os.Getenv(utils.ProjectNumberEnvVar)
		gomega.Expect(projectNumber).NotTo(gomega.BeEmpty())
		err := createIAMRoleForProfilesTests(ctx, l.config, projectNumber, projectID)
		framework.ExpectNoError(err, "Failed to create iam role for profiles tests")

		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		// Saving testScenario because l.config.Prefix gets overriden when calling  storageframework.createVolumeResourceOverriden (configPrefix is used to pass bucket names from test drivers create volume to tests)
		testScenario := l.config.Prefix

		l.volumeResourceList = []*storageframework.VolumeResource{}
		// Three Volumes, one for each sc archetype (Checkpointing, Training, Serving).
		for i, profile := range availableProfiles {

			l.volumeResourceList = append(l.volumeResourceList, storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{}))
			l.volumeResourceList[i] = modifyAndRedeployPVCPVs(ctx, f, testScenario, l.config, l.volumeResourceList[i], profile)
			klog.Infof("Modifying PV and PVC for profile: %s, vr : %s", profile, l.volumeResourceList[i].Pv.Spec.CSI.VolumeHandle)

		}

		// e2e/storage/framework does not support annotation injection on persistent volumes.
		// To follow current e2e workflow we will allow the framework to create the pv and pvcs without annotations,
		// then delete them, modify the spec to include the annotations and redeploy.
		// pvc is also delete to set sc

	}
	cleanup := func() {
		var cleanUpErrs []error
		t.disableAC(l.volumeResourceList)
		for _, vr := range l.volumeResourceList {
			cleanUpErrs = append(cleanUpErrs, vr.CleanupResource(ctx))
		}
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	validatePVAnnotations := func(pvName string, expectedAnnotations map[string]string, mountOptions string) {
		klog.Infof("Validating PV %s has expected annotations: %v", pvName, expectedAnnotations)
		cs := l.config.Framework.ClientSet
		pv, err := cs.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), fmt.Sprintf("failed to get PV %s", pv.Name))
		gomega.Expect(pv).NotTo(gomega.BeNil())
		for key, expectedValue := range expectedAnnotations {
			actualValue, exists := pv.Annotations[key]
			gomega.Expect(exists).To(gomega.BeTrue(), fmt.Sprintf("annotation %s not found on PV %s", key, pv.Name))
			if key == putil.AnnotationLastUpdatedTime {
				// Validating that bucket scan timestamp exists.
				continue
			}
			gomega.Expect(actualValue).To(gomega.Equal(expectedValue), fmt.Sprintf("annotation %s on PV %s: expected %s, got %s", key, pv.Name, expectedValue, actualValue))
		}
	}

	ginkgo.It("should override profiles all overridable values", func(ctx context.Context) {
		init(specs.ProfilesOverrideAllOverridablePrefix)
		defer cleanup()

		for _, vr := range l.volumeResourceList {
			validatePVAnnotations(vr.Pv.Name, map[string]string{
				putil.AnnotationNumObjects:      "40",
				putil.AnnotationTotalSize:       "50",
				putil.AnnotationStatus:          putil.ScanOverride,
				putil.AnnotationLastUpdatedTime: "anything", // This value is ignored, we just verify a timestamp was set
			}, "")
		}

		ginkgo.By("Configuring the profiles pods")
		podsArray := []*specs.TestPod{}
		for _, vr := range l.volumeResourceList {
			tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
			tPod.SetupVolume(vr, volumeName, mountPath, false)

			tPod.OverrideGCSFuseCache()

			podsArray = append(podsArray, tPod)
		}

		ginkgo.By("Sleeping 2 minutes for the service account being propagated")
		time.Sleep(time.Minute * 2)

		ginkgo.By("Deploying the profiles pods")
		for _, tPod := range podsArray {
			tPod.Create(ctx)
			defer tPod.Cleanup(ctx)
		}

		ginkgo.By("Checking that the pods are running")
		for _, tPod := range podsArray {
			tPod.WaitForRunning(ctx)
		}

		ginkgo.By("Checking that the pod command exec with no error")
		for _, tPod := range podsArray {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		}

		ginkgo.By("Verify mo are passed correctly to all ss")
		for _, tPod := range podsArray {
			tPod.VerifyMountOptionsArePassed(f.Namespace.Name, map[string]string{metadataStatCacheMaxSizeMiBMountOptionKey: "10", fileCacheSizeMiBMountOptionKey: "30"})
		}

		ginkgo.By("Verifying no recommendation was made")
		for _, tpod := range podsArray {
			tpod.VerifyDriverLogsDoNotContain(gcsFuseCsiRecommendationLog)
		}

		for i, bucketName := range strings.Split(l.config.Prefix, ",") {
			//tSS.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/%v/data-%v && grep 'hello world' %v/%v/data-%v", mountPath, bucketName, i, mountPath, bucketName, i))
			klog.Infof("bucketName for test is: %s, at pos %s", bucketName, i)
		}
		t.validateAC(l.volumeResourceList)
		klog.Infof("Successfully validated AnywhereCache creation for all profiles test")
	})
}

func modifyAndRedeployPVCPVs(ctx context.Context, f *framework.Framework, testScenario string, config *storageframework.PerTestConfig, vr *storageframework.VolumeResource, profile string) *storageframework.VolumeResource {
	cs := config.Framework.ClientSet
	cs.CoreV1().PersistentVolumeClaims(config.Framework.Namespace.Name).Delete(ctx, vr.Pvc.Name, metav1.DeleteOptions{})
	cs.CoreV1().PersistentVolumes().Delete(ctx, vr.Pv.Name, metav1.DeleteOptions{})
	waitForPVCDeleted(cs, config.Framework.Namespace.Name, vr.Pvc.Name)
	waitForPVDeleted(cs, vr.Pv.Name)

	// Modify PV & PVC.
	klog.Info("pv annotations before %s", vr.Pv.Annotations)
	vr.Pv = modifyPVForProfiles(testScenario, vr.Pv, profile)
	klog.Info("pv annotations after %s", vr.Pv.Annotations)
	vr.Pvc = modifyPVCForProfiles(testScenario, vr.Pvc, profile)

	// Recreate PV & PVC.
	newPv, err := cs.CoreV1().PersistentVolumes().Create(ctx, vr.Pv, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to recreate PV")
	newPvc, err := cs.CoreV1().PersistentVolumeClaims(config.Framework.Namespace.Name).Create(ctx, vr.Pvc, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to recreate PV")
	vr.Pv = newPv
	vr.Pvc = newPvc

	// Wait for Binding.
	err = e2epv.WaitOnPVandPVC(ctx, f.ClientSet, f.Timeouts, f.Namespace.Name, vr.Pv, vr.Pvc)
	framework.ExpectNoError(err, "PVC, PV failed to bind")
	return vr
}

// Helper to remove metadata that prevents recreation
func sanitizeForRecreation(pv *v1.PersistentVolume) *v1.PersistentVolume {
	pv.ResourceVersion = ""
	pv.UID = ""
	pv.SelfLink = ""
	pv.CreationTimestamp = metav1.Time{}
	// Clear status, as we want a fresh start
	pv.Status = v1.PersistentVolumeStatus{}
	// If the old PV was bound, we need to clear the ClaimRef so it can be bound again
	if pv.Spec.ClaimRef != nil {
		pv.Spec.ClaimRef.UID = ""
		pv.Spec.ClaimRef.ResourceVersion = ""
	}
	return pv
}

// Helper to remove metadata for PVC
func sanitizeForRecreationPVC(pvc *v1.PersistentVolumeClaim) *v1.PersistentVolumeClaim {
	pvc.ResourceVersion = ""
	pvc.UID = ""
	pvc.SelfLink = ""
	pvc.CreationTimestamp = metav1.Time{}
	pvc.Status = v1.PersistentVolumeClaimStatus{}
	return pvc
}

func waitForPVCDeleted(clientset kubernetes.Interface, namespace, pvcName string) error {
	ctx := context.TODO()
	for {
		_, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		if err != nil {
			// Not found means deleted; Done!
			if errors.IsNotFound(err) {
				break
			}
			// Some other error
			return err
		}
		time.Sleep(2 * time.Second)
	}
	return nil
}

func waitForPVDeleted(clientset kubernetes.Interface, pvName string) error {
	ctx := context.TODO()
	for {
		_, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				break
			}
			return err
		}
		time.Sleep(2 * time.Second)
	}
	return nil
}

func modifyPVCForProfiles(testScenario string, pvc *v1.PersistentVolumeClaim, sc string) *v1.PersistentVolumeClaim {
	pvc.Spec.StorageClassName = &sc
	pvc.Name = pvc.Name + "-modified"
	return sanitizeForRecreationPVC(pvc)
}
func modifyPVForProfiles(testScenario string, pv *v1.PersistentVolume, sc string) *v1.PersistentVolume {
	va := pv.Spec.CSI.VolumeAttributes
	annotations := pv.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	mo := pv.Spec.MountOptions
	if mo == nil {
		mo = []string{}
	}
	switch testScenario {
	case specs.ProfilesOverrideAllOverridablePrefix:
		// Modify Annotations
		annotations[putil.AnnotationNumObjects] = "40"
		annotations[putil.AnnotationTotalSize] = "50"
		annotations[putil.AnnotationStatus] = putil.ScanOverride

		// Modify Mount Options
		mo = append(mo, fmt.Sprintf("%s:%s", metadataStatCacheMaxSizeMiBMountOptionKey, "10"))
		// Purposely omit one - mo = append(mo, fmt.Sprintf("%s:%s", metadataTypeCacheMaxSizeMiBMountOptionKey, "20"))
		mo = append(mo, fmt.Sprintf("%s:%s", fileCacheSizeMiBMountOptionKey, "30"))

		// Modify volume attributes
		va[volumeAttributeAnywhereCacheTTL] = "2h"
		va[volumeAttributeAnywhereCacheAdmissionPolicy] = "admit-on-second-miss"
	}
	pv.Spec.CSI.VolumeAttributes = va
	pv.Annotations = annotations
	pv.Spec.MountOptions = mo
	pv.Spec.StorageClassName = sc

	pv.Name = pv.Name + "-modified"
	pv.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimDelete
	return sanitizeForRecreation(pv)
}

func (t *gcsFuseCSIProfilesTestSuite) validateAC(vrList []*storageframework.VolumeResource) {
	backoffSpec := wait.Backoff{
		Duration: acBackoffDuration,
		Factor:   acBackoffFactor,
		Cap:      acBackoffCap,
		Steps:    acBackoffSteps,
		Jitter:   acBackoffJitter,
	}

	for _, vr := range vrList {
		klog.Info("handling vr %s", vr)
		// AC is only enabled for serving profile.
		if vr.Pv.Spec.StorageClassName != "gcsfusecsi-serving" {
			continue
		}
		count := 0
		err := wait.ExponentialBackoff(backoffSpec, func() (bool, error) {
			framework.Logf("Attempt %d: Validating AnywhereCache for bucket %q", count, vr.Pv.Spec.CSI.VolumeHandle)
			count++
			req := &controlpb.ListAnywhereCachesRequest{
				Parent: fmt.Sprintf("projects/_/buckets/%s", vr.Pv.Spec.CSI.VolumeHandle),
			}
			acIterator := t.storageControlClient.ListAnywhereCaches(context.Background(), req)

			for {
				// Get the next item
				ac, err := acIterator.Next()

				if err == iterator.Done {
					klog.Infof("AnywhereCaches not found for bucket %q", vr.Pv.Spec.CSI.VolumeHandle)
					return true, nil
				}
				if err != nil {
					framework.Logf("ListAnywhereCache API failed, retrying: %v", err)
					return false, nil
				}
				if ac == nil {
					framework.Logf("AnywhereCache is nil, retrying...")
					return false, nil
				}

				framework.Logf("ListAnywhereCache API Succedded: %v validationg ttl...", ac)
				expectedTTLAsTimeDuration, _ := time.ParseDuration(vr.Pv.Spec.CSI.VolumeAttributes[volumeAttributeAnywhereCacheTTL])
				if ac.Ttl.AsDuration() != expectedTTLAsTimeDuration {
					framework.Logf("TTL is wrong, retrying...")
					return false, fmt.Errorf("AC TTL not as expected, expected: %q, got: %q", expectedTTLAsTimeDuration, ac.Ttl.AsDuration())
				}
				framework.Logf("TTL CHECK Succedded: %v validationg policy...", ac)
				expectedAPolicy := vr.Pv.Spec.CSI.VolumeAttributes[volumeAttributeAnywhereCacheAdmissionPolicy]
				if expectedAPolicy != ac.AdmissionPolicy {
					framework.Logf("AdmissionPolicy is wrong, retrying...")
					return false, fmt.Errorf("AC AdmissionPolicy not as expected, expected: %q, got: %q", expectedAPolicy, ac.AdmissionPolicy)
				}
				state := ac.State
				framework.Logf("AdmissionPolicy CHECK Succedded: %v validationg state, current state is %s...", ac, state)

				if state == acCreating {
					framework.Logf("AnywhereCache %q is still creating retrying...", ac.Name)
					return false, nil
				}
				if state == acReady {
					framework.Logf("Success AnywhereCache %q is in running state", ac.Name)
					continue
				}

				framework.Logf("AnywhereCache %q is ready", ac.Name)
			}

			framework.Logf("AnywhereCaches not found, likely a flake retrying...")
			return false, nil
		})
		framework.Logf("AnywhereCache %q is not ready this should not occur we ended while waiting", err)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed waiting for AnywhereCache to be ready, err: %v", err)
	}
}

// disableAC disables AnywhereCaches created during test. This is needed because buckets with AC cannot be deleted. You must first disable AC and wait for it to be deleted.
func (t *gcsFuseCSIProfilesTestSuite) disableAC(vrList []*storageframework.VolumeResource) {

	for _, vr := range vrList {
		if vr.Pv.Spec.StorageClassName != "gcsfusecsi-serving" {
			continue
		}
		req := &controlpb.ListAnywhereCachesRequest{
			Parent: fmt.Sprintf("projects/_/buckets/%s", vr.Pv.Spec.CSI.VolumeHandle),
		}
		acIterator := t.storageControlClient.ListAnywhereCaches(context.Background(), req)
		for {
			// Get the next item
			cache, err := acIterator.Next()

			if err == iterator.Done {
				break
			}

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "error encountered while listing AnywhereCaches: %v", err)

			req := &controlpb.DisableAnywhereCacheRequest{
				Name: cache.Name,
			}
			framework.Logf("Disabling AnywhereCache: %s", cache.Name)
			_, err = t.storageControlClient.DisableAnywhereCache(context.Background(), req)
			if err != nil {
				framework.Logf("Failed to disable AnywhereCache %s: %v", cache.Name, err)
			}
		}

	}
	framework.Logf("Disabled AnywhereCaches now waiting 1 hour for caches to be deleted")
	time.Sleep(time.Minute * 65) // Waiting 65 minutes to ensure ACs are deleted.
}
