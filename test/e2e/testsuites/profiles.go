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
	profileMountOptionKey                     = "profile"
	cacheDirKey                               = "cache-dir"

	anywhereCacheZonesKey           = "anywhereCacheZones"
	anywhereCacheAdmissionPolicyKey = "anywhereCacheAdmissionPolicy"
	anywhereCacheTTLKey             = "anywhereCacheTTL"
	bucketScanResyncPeriodKey       = "bucketScanResyncPeriod"
	useBucketMetricsKey             = "useBucketMetrics"

	anywhereCacheBackoffDuration = 22 * time.Second
	acBackoffFactor              = 1.2
	acBackoffCap                 = 4000 * time.Second
	acBackoffSteps               = 30
	acBackoffJitter              = 0.1

	retryPolling = 5 * time.Second
	retryTimeout = 10 * time.Minute

	gcsFuseCsiRecommendationLog = "GCSFuseCSIRecommendation"

	acCreating = "creating"
	acReady    = "running"

	vbm = storagev1.VolumeBindingWaitForFirstConsumer

	servingProfile       = "gcsfusecsi-serving"
	trainingProfile      = "gcsfusecsi-training"
	checkpointingProfile = "gcsfusecsi-checkpointing"

	controllerNS            = "gcs-fuse-csi-driver"
	controllerLabelSelector = "app=gcs-fuse-csi-driver"
)

var (
	availableProfiles                 = []string{servingProfile, trainingProfile, checkpointingProfile}
	projectID                         string
	projectNumber                     string
	controllerAnnotationValidationMap = map[string]string{
		putil.AnnotationLastUpdatedTime: "anything",
		putil.AnnotationStatus:          "completed",
		putil.AnnotationNumObjects:      "20852700",
		// Total size is expected to be 0 since size is the aggregate of object sizes and
		// the bucket we are using is 20852700 empty files.
		putil.AnnotationTotalSize: "0",
	}
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

	init := func(configPrefix ...string) *storageframework.PerTestConfig {
		// Validate IAM Role exists. Currently disabled as the role is not created is not allowed in boskos projects.
		// TODO(fuechr): Reenable once we have a way to create the role in boskos projects.
		// projectID := os.Getenv(utils.ProjectEnvVar)
		// gomega.Expect(projectID).NotTo(gomega.BeEmpty())
		// err := utils.ValidateIAMRoleExists(ctx, projectID, utils.ProfilesUserRoleID)
		// gomega.Expect(err).NotTo(gomega.HaveOccurred(), "recieved err while validating profiles iam role: %v", err)

		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		// Saving testScenario because l.config.Prefix gets overriden when calling  storageframework.createVolumeResourceOverriden (configPrefix is used to pass bucket names from test drivers create volume to tests).
		testScenario := l.config.Prefix

		l.volumeResourceList = []*storageframework.VolumeResource{}

		// e2e/storage/framework does not support annotation injection on persistent volumes.
		// To follow current e2e workflow we will allow the framework to create the pv and pvcs without annotations,
		// then delete them, modify the spec to include the annotations and redeploy.
		// pvc is also deleted to set sc.
		// TODO(fuechr): Once e2e/storage/framework supports annotation injection, we can remove modifyAndRedeployPVCPVs function and call CreateVolumeResource with needed annotations directly.
		switch testScenario {
		case specs.ProfilesControllerCrashTestPrefix:
			l.volumeResourceList = append(l.volumeResourceList, storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{}))
			l.volumeResourceList[0] = modifyAndRedeployPVCPVs(ctx, f, testScenario, l.config, l.volumeResourceList[0], checkpointingProfile) // Setting to checkpointing to avoid the anywhere cache worklfow.
		default:
			for i, profile := range availableProfiles {
				l.volumeResourceList = append(l.volumeResourceList, storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{}))
				l.volumeResourceList[i] = modifyAndRedeployPVCPVs(ctx, f, testScenario, l.config, l.volumeResourceList[i], profile)
			}
		}
		return l.config
	}
	cleanup := func() {
		var cleanUpErrs []error

		for _, vr := range l.volumeResourceList {
			cleanUpErrs = append(cleanUpErrs, vr.CleanupResource(ctx))
		}
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	getPV := func(pvName string, ctx context.Context) *v1.PersistentVolume {
		cs := l.config.Framework.ClientSet
		pv, err := cs.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to get PV %s", pv.Name)
		gomega.Expect(pv).NotTo(gomega.BeNil())
		return pv
	}

	// validatePVAnnotationsAndReturnScan checks that the given PV has the expected annotations.
	// It returns the scan timestamp found in the annotation last updated time.
	// If the expected value for an annotation is "anything", the value is not validated, only existence is checked.
	validatePVAnnotationsAndReturnScan := func(g gomega.Gomega, pvName string, expectedAnnotations map[string]string, ctx context.Context) string {
		klog.Infof("Validating PV %s has expected annotations: %v", pvName, expectedAnnotations)
		pv := getPV(pvName, ctx)
		var scanTime string

		for key, expectedValue := range expectedAnnotations {
			actualValue, exists := pv.Annotations[key]
			g.Expect(exists).To(gomega.BeTrue(), fmt.Sprintf("annotation %s not found on PV %s", key, pv.Name))
			if key == putil.AnnotationLastUpdatedTime {
				// Validating that bucket scan timestamp exists.
				scanTime = actualValue
			}
			if expectedValue == "anything" {
				continue
			}
			g.Expect(actualValue).To(gomega.Equal(expectedValue), fmt.Sprintf("annotation %s on PV %s: expected %s, got %s", key, pv.Name, expectedValue, actualValue))
		}
		return scanTime

	}

	ginkgo.It("should rescan pv after controller is killed", ginkgo.Serial, func(ctx context.Context) {
		IsOSSEnvVar := os.Getenv(specs.IsOSSEnvVar)
		if IsOSSEnvVar != "true" {
			ginkgo.Skip("Skipping profiles controller crash test for managed, killing the managed controller is not a supported operation")
		}

		testConfig := init(specs.ProfilesControllerCrashTestPrefix)
		killController(ctx, testConfig)
		defer cleanup()

		var lastScanTime string
		// Wait for controller to come back up and save the scan time for rescan validation.
		gomega.Eventually(func(g gomega.Gomega) {
			lastScanTime = validatePVAnnotationsAndReturnScan(g, l.volumeResourceList[0].Pv.Name, controllerAnnotationValidationMap, ctx)
			gomega.Expect(lastScanTime).NotTo(gomega.BeEmpty())
		}).WithTimeout(retryTimeout).WithPolling(retryPolling).Should(gomega.Succeed())

		// Verify pod came up succesfully.
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetupVolume(l.volumeResourceList[0], volumeName, mountPath, false)

		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		tPod.WaitForRunning(ctx)

		// Kill the controller again and wait 5 min, expectation is the pv should be rescanned.
		killController(ctx, testConfig)

		ginkgo.By("Verifying PV was rescanned by checking updated timestamp")
		var newScanTime string
		gomega.Eventually(func(g gomega.Gomega) {
			newScanTime = validatePVAnnotationsAndReturnScan(gomega.Default, l.volumeResourceList[0].Pv.Name, controllerAnnotationValidationMap, ctx)
			g.Expect(newScanTime).NotTo(gomega.BeEmpty())
			g.Expect(newScanTime).NotTo(gomega.Equal(lastScanTime))
		}).WithTimeout(retryTimeout).WithPolling(retryPolling).Should(gomega.Succeed())

		ginkgo.By("Successfully verified PV rescan after controller restart")

	})

	ginkgo.It("should override profiles all overridable values", ginkgo.Serial, func() {
		err := utils.AddComputeBindingForAC(ctx)
		if err != nil {
			klog.Errorf("Failed while prepping anywherecache iam bindings for profiles tests: %v", err)
		}
		init(specs.ProfilesOverrideAllOverridablePrefix)
		defer cleanup()

		for _, vr := range l.volumeResourceList {
			validatePVAnnotationsAndReturnScan(gomega.Default, vr.Pv.Name, map[string]string{
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
			sc := l.volumeResourceList[i].Pv.Spec.StorageClassName
			profileMO := fmt.Sprintf("aiml-%s", strings.TrimPrefix(sc, "gcsfusecsi-"))
			tPod.VerifyMountOptionsArePassedWithConfigFormat(f.Namespace.Name, map[string]string{profileMountOptionKey: profileMO, metadataStatCacheMaxSizeMiBMountOptionKey: "10", fileCacheSizeMiBMountOptionKey: "30", cacheDirKey: fmt.Sprintf("/gcsfuse-cache/.volumes/%s", l.volumeResourceList[i].Pv.Name)})
		}

		// No recommendation should be found because smart cache calculation is disabled when cx overrides cache sizes.
		ginkgo.By("Verifying no recommendation was made")
		for _, tpod := range podsArray {
			stdout, err := tpod.FindLogsByNewLine(gcsFuseCsiRecommendationLog)
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "error while getting logs from pod")
			gomega.Expect(stdout).To(gomega.BeEmpty(), "expected no recommendation logs, but found: %s", stdout)
		}
		isZBEnabled := os.Getenv(utils.IsZBEnabledEnvVar)
		if isZBEnabled == "true" {
			// AC is not supported for zb, so we only validate AC for non zb tests.
			return
		}

		t.validateAC(l.volumeResourceList, ctx)
		klog.Infof("Successfully validated AnywhereCache creation for all profiles test")

		t.disableAC(l.volumeResourceList, ctx)
	})
}

func killController(ctx context.Context, config *storageframework.PerTestConfig) {
	cs := config.Framework.ClientSet
	pods, err := cs.CoreV1().Pods(controllerNS).List(ctx, metav1.ListOptions{
		LabelSelector: controllerLabelSelector,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(pods.Items).NotTo(gomega.BeEmpty())

	var controllerPod v1.Pod
	found := false

	for _, pod := range pods.Items {
		// The Deployment name is gcs-fuse-csi-controller,
		// so the pod name will always start with this.
		if strings.HasPrefix(pod.Name, "gcs-fuse-csi-controller") {
			controllerPod = pod
			found = true
			break
		}
	}

	gomega.Expect(found).NotTo(gomega.BeFalse())

	ginkgo.By("Deleting the controller pod")
	err = cs.CoreV1().Pods(controllerNS).Delete(
		ctx,
		controllerPod.Name,
		metav1.DeleteOptions{},
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	ginkgo.By("Waiting for a new controller pod to become Ready")
	gomega.Eventually(func() bool {
		pods, err := cs.CoreV1().Pods(controllerNS).List(ctx, metav1.ListOptions{
			LabelSelector: controllerLabelSelector,
		})
		if err != nil || len(pods.Items) == 0 {
			return false
		}

		for _, pod := range pods.Items {
			if pod.Name == controllerPod.Name {
				continue // old pod
			}
			if pod.Status.Phase != v1.PodRunning {
				continue
			}
			for _, cond := range pod.Status.Conditions {
				if cond.Type == v1.PodReady && cond.Status == v1.ConditionTrue {
					return true
				}
			}
		}
		return false
	}, 5*time.Minute, 5*time.Second).Should(gomega.BeTrue())
}

// e2e/storage/framework does not support injecting custom annotations or modifying
// StorageClass on pre-provisioned PVs directly during creation. To work around this,
// we let the framework create the resources, then delete them, modify the specs,
// and recreate them with the desired profile-specific settings.
func modifyAndRedeployPVCPVs(ctx context.Context, f *framework.Framework, testScenario string, config *storageframework.PerTestConfig, vr *storageframework.VolumeResource, profile string) *storageframework.VolumeResource {
	klog.Infof("Modifying PV and PVC for profile: %s, vr : %s", profile, vr.Pv.Spec.CSI.VolumeHandle)
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
		// Modify Annotations.
		annotations[putil.AnnotationNumObjects] = "40"
		annotations[putil.AnnotationTotalSize] = "50"
		annotations[putil.AnnotationStatus] = putil.ScanOverride

		// Modify Mount Options.
		mo = append(mo, fmt.Sprintf("%s:%s", metadataStatCacheMaxSizeMiBMountOptionKey, "10"))
		// Purposely omit one - mo = append(mo, fmt.Sprintf("%s:%s", metadataTypeCacheMaxSizeMiBMountOptionKey, "20"))
		mo = append(mo, fmt.Sprintf("%s:%s", fileCacheSizeMiBMountOptionKey, "30"))

		if sc == "gcsfusecsi-serving" {
			// We are hardcoding the zones for ac to be us-central1-c because one of the available zones is an ai
			// specific zone which has limited quota, we should not make caches in this zone.
			va[anywhereCacheTTLKey] = "2h"
			va[anywhereCacheAdmissionPolicyKey] = "admit-on-second-miss"
			isZBEnabled := os.Getenv(utils.IsZBEnabledEnvVar)
			if isZBEnabled == "false" {
				// AC is not supported for zb, so we only use AC for non zb tests.
				va[anywhereCacheZonesKey] = "us-central1-c"
			}

		}
	case specs.ProfilesControllerCrashTestPrefix:
		va[bucketScanResyncPeriodKey] = "5m"
	default:
		framework.Failf("Unknown test scenario: %s", testScenario)
	}
	pv.Spec.CSI.VolumeAttributes = va
	pv.Annotations = annotations
	pv.Spec.MountOptions = mo
	pv.Spec.StorageClassName = sc

	pv.Name = pv.Name + "-modified"
	return sanitizeForRecreation(pv)
}

func (t *gcsFuseCSIProfilesTestSuite) validateAC(vrList []*storageframework.VolumeResource, ctx context.Context) {
	for _, vr := range vrList {
		klog.Info("handling vr %+v", vr)
		// AC is only enabled for serving profile.
		if vr.Pv.Spec.StorageClassName != "gcsfusecsi-serving" {
			continue
		}
		count := 0
		gomega.Eventually(ctx, func(g gomega.Gomega) {
			framework.Logf("Attempt %d: Validating AnywhereCache for bucket %q", count, vr.Pv.Spec.CSI.VolumeHandle)
			count++
			req := &controlpb.ListAnywhereCachesRequest{
				Parent: fmt.Sprintf("projects/_/buckets/%s", vr.Pv.Spec.CSI.VolumeHandle),
			}
			acIterator := t.storageControlClient.ListAnywhereCaches(ctx, req)
			countNumberOfACs := 0
			for {
				ac, err := acIterator.Next()

				if err == iterator.Done {
					g.Expect(countNumberOfACs).NotTo(gomega.BeZero(), "Expected exactly 1 AnywhereCache")
					return
				}
				g.Expect(err).To(gomega.Succeed(), "ListAnywhereCache API failed")
				g.Expect(ac).NotTo(gomega.BeNil(), "AnywhereCache is nil")

				countNumberOfACs++
				// Fail if we find more than one.
				g.Expect(countNumberOfACs).To(gomega.BeNumerically("<=", 1), "More than 1 AnywhereCache found")
				framework.Logf("ListAnywhereCache API Succeeded: %v. Validating TTL...", ac)

				// TTL Validation.
				expectedTTLAsTimeDuration, err := time.ParseDuration(vr.Pv.Spec.CSI.VolumeAttributes[anywhereCacheTTLKey])
				g.Expect(err).To(gomega.Succeed(), "Failed to parse expected TTL duration")
				g.Expect(ac.Ttl.AsDuration()).To(gomega.Equal(expectedTTLAsTimeDuration), "AC TTL mismatch")

				// Admission Policy Validation.
				expectedAPolicy := vr.Pv.Spec.CSI.VolumeAttributes[anywhereCacheAdmissionPolicyKey]
				g.Expect(ac.AdmissionPolicy).To(gomega.Equal(expectedAPolicy), "AC AdmissionPolicy mismatch")

				// State Validation.
				state := ac.State
				framework.Logf("Policy check succeeded. State is %s...", state)

				switch state {
				case acCreating:
					// Fail the expectation so Eventually retries.
					g.Expect(fmt.Errorf("AnywhereCache in unexpected state %q", state)).To(gomega.Succeed())
				case acReady:
					framework.Logf("Success: AnywhereCache %q is in ready state", ac.Name)
					// Continue the iterator to ensure no other ACs exist
					continue
				default:
					g.Expect(fmt.Errorf("AnywhereCache in unexpected state %q", state)).To(gomega.Succeed())
				}
			}
		}).WithTimeout(acBackoffCap). // Set a high ceiling, the 'Steps' will limit the actual time
						WithPolling(anywhereCacheBackoffDuration).
						Should(gomega.Succeed())
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
