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
	"time"

	"local/test/e2e/specs"
	"local/test/e2e/utils"

	putil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"google.golang.org/api/iterator"
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
	control "cloud.google.com/go/storage/control/apiv2"
	"cloud.google.com/go/storage/control/apiv2/controlpb"
)

const (
	// StorageClass param keys.
	fuseFileCacheMediumPriorityKey           = "fuseFileCacheMediumPriority"
	fuseMemoryAllocatableFactorKey           = "fuseMemoryAllocatableFactor"
	fuseEphemeralStorageAllocatableFactorKey = "fuseEphemeralStorageAllocatableFactor"
	gcsfuseMetadataPrefetchOnMountKey        = "gcsfuseMetadataPrefetchOnMount"

	gcsFuseCsiDriverName = "gcsfuse.csi.storage.gke.io"

	metadataStatCacheMaxSizeMiBMountOptionKey = "metadata-cache:stat-cache-max-size-mb"
	metadataTypeCacheMaxSizeMiBMountOptionKey = "metadata-cache:type-cache-max-size-mb"
	fileCacheSizeMiBMountOptionKey            = "file-cache:max-size-mb"
	cacheDirKey                               = "cache-dir"

	anywhereCacheZonesKey           = "anywhereCacheZones"
	anywhereCacheAdmissionPolicyKey = "anywhereCacheAdmissionPolicy"
	anywhereCacheTTLKey             = "anywhereCacheTTL"

	anywhereCacheBackoffDuration = 22 * time.Second
	acBackoffFactor              = 1.2
	acBackoffCap                 = 4000 * time.Second
	acBackoffSteps               = 30
	acBackoffJitter              = 0.1

	gcsFuseCsiRecommendationLog = "GCSFuseCSIRecommendation"

	acCreating = "creating"
	acReady    = "running"

	vbm = storagev1.VolumeBindingWaitForFirstConsumer
)

var (
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

func (t *gcsFuseCSIProfilesTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {

	type local struct {
		config             *storageframework.PerTestConfig
		volumeResourceList []*storageframework.VolumeResource
	}
	var l local

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("profiles", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged
	ctx := context.Background()

	init := func(configPrefix ...string) {
		projectID := os.Getenv(utils.ProjectEnvVar)
		gomega.Expect(projectID).NotTo(gomega.BeEmpty())
		projectNumber := os.Getenv(utils.ProjectNumberEnvVar)
		gomega.Expect(projectNumber).NotTo(gomega.BeEmpty())

		err := utils.ValidateIAMRoleExists(ctx, projectID, utils.ProfilesUserRoleID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "recieved err while validating profiles iam role: %v", err)

		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		// Saving testScenario because l.config.Prefix gets overriden when calling  storageframework.createVolumeResourceOverriden (configPrefix is used to pass bucket names from test drivers create volume to tests).
		testScenario := l.config.Prefix

		l.volumeResourceList = []*storageframework.VolumeResource{}
		// Three Volumes, one for each sc archetype (Checkpointing, Training, Serving).
		for i, profile := range availableProfiles {

			l.volumeResourceList = append(l.volumeResourceList, storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{}))
			// e2e/storage/framework does not support annotation injection on persistent volumes.
			// To follow current e2e workflow we will allow the framework to create the pv and pvcs without annotations,
			// then delete them, modify the spec to include the annotations and redeploy.
			// pvc is also deleted to set sc.
			// TODO(chrisThePattyEater): Once e2e/storage/framework supports annotation injection, we can remove modifyAndRedeployPVCPVs function and call CreateVolumeResource with needed annotations directly.
			l.volumeResourceList[i] = modifyAndRedeployPVCPVs(ctx, f, testScenario, l.config, l.volumeResourceList[i], profile)
			klog.Infof("Modifying PV and PVC for profile: %s, vr : %s", profile, l.volumeResourceList[i].Pv.Spec.CSI.VolumeHandle)

		}

	}
	cleanup := func() {
		var cleanUpErrs []error

		for _, vr := range l.volumeResourceList {
			cleanUpErrs = append(cleanUpErrs, vr.CleanupResource(ctx))
		}
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	validatePVAnnotations := func(pvName string, expectedAnnotations map[string]string, ctx context.Context) {
		klog.Infof("Validating PV %s has expected annotations: %v", pvName, expectedAnnotations)
		cs := l.config.Framework.ClientSet
		pv, err := cs.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to get PV %s", pv.Name)
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

	ginkgo.It("should override profiles all overridable values", func() {
		IsOSSEnvVar := os.Getenv(specs.IsOSSEnvVar)
		if IsOSSEnvVar != "true" {
			ginkgo.Skip("Skipping test: Test is not yet supported on managed")
		}

		init(specs.ProfilesOverrideAllOverridablePrefix)
		defer cleanup()

		for _, vr := range l.volumeResourceList {
			validatePVAnnotations(vr.Pv.Name, map[string]string{
				putil.AnnotationNumObjects:      "40",
				putil.AnnotationTotalSize:       "50",
				putil.AnnotationStatus:          putil.ScanOverride,
				putil.AnnotationLastUpdatedTime: "anything", // This value is ignored, we just verify a timestamp was set
			}, ctx)
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
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("echo 'hello world' > %v/data && grep 'hello world' %v/data", mountPath, mountPath))
		}

		ginkgo.By("Verify mo are passed correctly to all ss")
		for i, tPod := range podsArray {
			tPod.VerifyMountOptionsArePassedWithConfigFormat(f.Namespace.Name, map[string]string{metadataStatCacheMaxSizeMiBMountOptionKey: "10", fileCacheSizeMiBMountOptionKey: "30", cacheDirKey: fmt.Sprintf("/gcsfuse-cache/.volumes/%s", l.volumeResourceList[i].Pv.Name)})
		}

		// No recommendation should be found because smart cache calculation is disabled when cx overrides cache sizes.
		ginkgo.By("Verifying no recommendation was made")
		for _, tpod := range podsArray {
			tpod.VerifyDriverLogsDoNotContain(gcsFuseCsiRecommendationLog)
		}

		t.validateAC(l.volumeResourceList, ctx)
		klog.Infof("Successfully validated AnywhereCache creation for all profiles test")

		t.disableAC(l.volumeResourceList, ctx)
	})
}

// e2e/storage/framework does not support injecting custom annotations or modifying
// StorageClass on pre-provisioned PVs directly during creation. To work around this,
// we let the framework create the resources, then delete them, modify the specs,
// and recreate them with the desired profile-specific settings.
func modifyAndRedeployPVCPVs(ctx context.Context, f *framework.Framework, testScenario string, config *storageframework.PerTestConfig, vr *storageframework.VolumeResource, profile string) *storageframework.VolumeResource {
	cs := config.Framework.ClientSet
	cs.CoreV1().PersistentVolumeClaims(config.Framework.Namespace.Name).Delete(ctx, vr.Pvc.Name, metav1.DeleteOptions{})
	cs.CoreV1().PersistentVolumes().Delete(ctx, vr.Pv.Name, metav1.DeleteOptions{})
	waitForPVCDeleted(cs, config.Framework.Namespace.Name, vr.Pvc.Name, ctx)
	waitForPVDeleted(cs, vr.Pv.Name, ctx)

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

func waitForPVCDeleted(clientset kubernetes.Interface, namespace, pvcName string, ctx context.Context) error {
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

func waitForPVDeleted(clientset kubernetes.Interface, pvName string, ctx context.Context) error {
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
		va[anywhereCacheTTLKey] = "2h"
		va[anywhereCacheAdmissionPolicyKey] = "admit-on-second-miss"
		if sc == "gcsfusecsi-serving" {
			// We are hardcoding the zones for ac to be us-central1-c because one of the available zones is an ai
			// specific zone which has limited quota, we should not make caches in this zone.
			va[anywhereCacheZonesKey] = "us-central1-c"
		}
	}
	pv.Spec.CSI.VolumeAttributes = va
	pv.Annotations = annotations
	pv.Spec.MountOptions = mo
	pv.Spec.StorageClassName = sc

	pv.Name = pv.Name + "-modified"
	return sanitizeForRecreation(pv)
}

func (t *gcsFuseCSIProfilesTestSuite) validateAC(vrList []*storageframework.VolumeResource, ctx context.Context) {
	backoffSpec := wait.Backoff{
		Duration: anywhereCacheBackoffDuration,
		Factor:   acBackoffFactor,
		Cap:      acBackoffCap,
		Steps:    acBackoffSteps,
		Jitter:   acBackoffJitter,
	}

	for _, vr := range vrList {
		klog.Info("handling vr %+v", vr)
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
			acIterator := t.storageControlClient.ListAnywhereCaches(ctx, req)
			countNumberOfACs := 0
			for {
				// Get the next item
				ac, err := acIterator.Next()

				if err == iterator.Done {
					gomega.Expect(countNumberOfACs).NotTo(gomega.BeZero())
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
				countNumberOfACs++
				if countNumberOfACs > 1 {
					return false, fmt.Errorf("more than 1 AnywhereCache found for bucket %q", vr.Pv.Spec.CSI.VolumeHandle)
				}
				framework.Logf("ListAnywhereCache API Succedded: %v validating ttl...", ac)

				expectedTTLAsTimeDuration, err := time.ParseDuration(vr.Pv.Spec.CSI.VolumeAttributes[anywhereCacheTTLKey])
				if err != nil {
					return false, err
				}

				if ac.Ttl.AsDuration() != expectedTTLAsTimeDuration {
					return false, fmt.Errorf("AC TTL not as expected, expected: %q, got: %q", expectedTTLAsTimeDuration, ac.Ttl.AsDuration())
				}
				framework.Logf("TTL CHECK Succedded: %v validationg policy...", ac)
				expectedAPolicy := vr.Pv.Spec.CSI.VolumeAttributes[anywhereCacheAdmissionPolicyKey]
				if expectedAPolicy != ac.AdmissionPolicy {
					return false, fmt.Errorf("AC AdmissionPolicy not as expected, expected: %q, got: %q", expectedAPolicy, ac.AdmissionPolicy)
				}
				state := ac.State
				framework.Logf("AdmissionPolicy CHECK Succedded: %v validationg state, current state is %s...", ac, state)
				switch state {
				case acCreating:
					framework.Logf("AnywhereCache %q is still creating retrying...", ac.Name)
					return false, nil
				case acReady:
					framework.Logf("Success AnywhereCache %q is in running state", ac.Name)
					continue
				default:
					return false, fmt.Errorf("AnywhereCache in unexpected state %q", state)
				}
			}
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed waiting for AnywhereCache to be ready, err: %v", err)
	}
}

// disableAC disables AnywhereCaches created during test. This is needed because buckets with AC cannot be deleted. You must first disable AC and wait for it to be deleted.
func (t *gcsFuseCSIProfilesTestSuite) disableAC(vrList []*storageframework.VolumeResource, ctx context.Context) {
	bucketsNeedingACDeletion := []string{}
	disableErrors := []error{}

	for _, vr := range vrList {
		if vr.Pv.Spec.StorageClassName != "gcsfusecsi-serving" {
			continue
		}
		bucketsNeedingACDeletion = append(bucketsNeedingACDeletion, vr.Pv.Spec.CSI.VolumeHandle)

		req := &controlpb.ListAnywhereCachesRequest{
			Parent: fmt.Sprintf("projects/_/buckets/%s", vr.Pv.Spec.CSI.VolumeHandle),
		}
		acIterator := t.storageControlClient.ListAnywhereCaches(ctx, req)
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
			_, err = t.storageControlClient.DisableAnywhereCache(ctx, req)
			if err != nil {
				disableErrors = append(disableErrors, err)
			}
		}

	}
	gomega.Expect(disableErrors).To(gomega.BeEmpty(), "error(s) encountered while disabling AnywhereCaches: %v", disableErrors)
	framework.Logf("Disabled AnywhereCaches now waiting for caches to be deleted")
	for _, bucket := range bucketsNeedingACDeletion {
		t.waitForAnywhereCacheDeletion(bucket, ctx)
	}
}

func (t *gcsFuseCSIProfilesTestSuite) waitForAnywhereCacheDeletion(bucket string, ctx context.Context) error {
	backoff := wait.Backoff{
		Duration: anywhereCacheBackoffDuration,
		Factor:   acBackoffFactor,
		Jitter:   acBackoffJitter,
		Steps:    acBackoffSteps,
		Cap:      acBackoffCap,
	}
	acDeleteAttempt := 0
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		acDeleteAttempt++
		framework.Logf("Attempt %d: Checking AnywhereCache deletion for bucket %q", acDeleteAttempt, bucket)
		req := &controlpb.ListAnywhereCachesRequest{
			Parent: fmt.Sprintf("projects/_/buckets/%s", bucket),
		}
		acIterator := t.storageControlClient.ListAnywhereCaches(ctx, req)
		_, err := acIterator.Next()

		if err == iterator.Done {
			// Condition met: Cache is gone.
			return true, nil
		}
		// Some other error or cache still exists, retrying.
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("error while waiting for AnywhereCache deletion: %w", err)
	}
	return nil
}
