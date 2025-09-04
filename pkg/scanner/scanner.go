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

package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	v1listers "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
)

const (
	scannerComponentName              = "gke-gcsfuse-scanner"
	csiDriverName                     = "gcsfuse.csi.storage.gke.io"
	paramWorkloadTypeKey              = "workloadType"
	paramWorkloadTypeInferenceKey     = "inference"
	paramWorkloadTypeTrainingKey      = "training"
	paramWorkloadTypeCheckpointingKey = "checkpointing"
	volumeAttributeScanTimeoutKey     = "bucketScanTimeout"
	volumeAttributeScanTTLKey         = "bucketScanTTL"

	// Annotation keys
	annotationPrefix          = "gke-gcsfuse"
	annotationStatus          = annotationPrefix + "/bucket-scan-status"
	annotationNumObjects      = annotationPrefix + "/bucket-scan-num-objects"
	annotationTotalSize       = annotationPrefix + "/bucket-scan-total-size-bytes"
	annotationLastUpdatedTime = annotationPrefix + "/bucket-scan-last-updated-time"
	annotationHNSEnabled      = annotationPrefix + "/bucket-scan-hns-enabled"

	// Event reasons
	reasonScanOperationStartError     = "ScanOperationStartError"
	reasonScanOperationStartSucceeded = "ScanOperationStartSucceeded"
	reasonScanOperationFailed         = "ScanOperationFailed"
	reasonScanOperationSucceeded      = "ScanOperationSucceeded"
	reasonScanOperationTimedOut       = "ScanOperationTimedOut"

	// Bucket scan status values
	scanCompleted = "completed"
	scanTimeout   = "timeout"

	// Key prefixes for workqueue
	pvPrefix  = "pv/"
	podPrefix = "pod/"

	// Scheduling Gate
	schedulingGateName = "gke-gcsfuse/bucket-scan-pending"

	// Label for filtering Pods
	profileManagedLabelKey   = "gke-gcsfuse/profile-managed"
	profileManagedLabelValue = "true"
)

var (
	// defaultScanTimeoutDuration is the default timeout for a single bucket scan.
	defaultScanTimeoutDuration = 2 * time.Minute

	// defaultScanTTLDuration is the default TTL for skipping bucket scans.
	defaultScanTTLDuration = 168 * time.Hour // 7 days

	// To allow mocking time in tests
	timeNow = time.Now

	// scanBucket is a function to scan the bucket, can be overridden in tests.
	scanBucket = defaultScanBucket

	// Fake error to signal worker to re-queue the Pod without emitting an error log.
	errRequeuePod = errors.New("requeuing pod")
)

// stringPtr returns a pointer to the passed string.
func stringPtr(s string) *string { return &s }

// boolPtr returns a pointer to the string representation of the passed bool.
func boolPtr(b bool) *string { return stringPtr(strconv.FormatBool(b)) }

// int64Ptr returns a pointer to the string representation of the passed int64.
func int64Ptr(i int64) *string { return stringPtr(strconv.FormatInt(i, 10)) }

// ScannerConfig holds the configuration for the Scanner.
type ScannerConfig struct {
	KubeAPIQPS     float64       // QPS limit for Kubernetes API client.
	KubeAPIBurst   int           // Burst limit for Kubernetes API client.
	ResyncPeriod   time.Duration // Resync period for informers.
	KubeConfigPath string        // Optional: Path to kubeconfig file. If empty, InClusterConfig is used.
	RateLimiter    workqueue.TypedRateLimiter[string]
}

// bucketInfo holds the results of a bucket scan.
type bucketInfo struct {
	name           string
	dir            string
	numObjects     int64
	totalSizeBytes int64
	isHNSEnabled   bool
}

// Scanner is the main controller structure.
type Scanner struct {
	kubeClient    kubernetes.Interface
	pvLister      v1listers.PersistentVolumeLister
	pvcLister     v1listers.PersistentVolumeClaimLister
	scLister      storagelisters.StorageClassLister
	podLister     v1listers.PodLister
	pvSynced      cache.InformerSynced
	pvcSynced     cache.InformerSynced
	scSynced      cache.InformerSynced
	podSynced     cache.InformerSynced
	factory       informers.SharedInformerFactory
	podFactory    informers.SharedInformerFactory
	queue         workqueue.TypedRateLimitingInterface[string]
	eventRecorder record.EventRecorder

	// pvMutex protects trackedPVs.
	pvMutex sync.RWMutex
	// Set of PV names being tracked/processed. This is
	// used to avoid reprocessing if multiple user Pods
	// trigger a PV addition to the queue.
	trackedPVs map[string]struct{}

	// scanMutex protects lastSuccessfulScan.
	scanMutex sync.RWMutex
	// Map to track the last successful scan time for each PV in this instance.
	// This is required because there exists a non-zero time window where a PV
	// patch may not be reflected in the informer's cache after syncPV processes
	// and removes the key from trackedPVs, potentially resulting in multiple
	// scans not seeing the PV's annotationLastUpdatedTime.
	lastSuccessfulScan map[string]time.Time
}

// buildConfig creates a Kubernetes rest.Config for the client.
func buildConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("error building kubeconfig from path %s: %w", kubeconfigPath, err)
		}
		klog.Infof("Using Kubeconfig: %s", kubeconfigPath)
		return cfg, nil
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("error building in-cluster kubeconfig: %w", err)
	}
	klog.Info("Using In-Cluster Kubeconfig")
	return cfg, nil
}

// trimPodObject is a transform function for the Pod informer to reduce memory usage.
// It keeps only the fields relevant to the scanner logic.
func trimPodObject(obj any) (any, error) {
	if accessor, err := meta.Accessor(obj); err == nil {
		accessor.SetManagedFields(nil)
	} else {
		klog.Warningf("Failed to get meta accessor for object: %v", err)
	}

	podObj, ok := obj.(*v1.Pod)
	if !ok {
		return obj, nil
	}

	// Create a new Pod object with only the fields we need.
	trimmedPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podObj.ObjectMeta.Name,      // Needed for workqueue key.
			Namespace: podObj.ObjectMeta.Namespace, // Needed for workqueue key.
		},
		Spec: v1.PodSpec{
			Volumes:         podObj.Spec.Volumes,         // Needed to check PVs requiring scans.
			SchedulingGates: podObj.Spec.SchedulingGates, // Needed to remove scheduling gates.
		},
	}

	return trimmedPod, nil
}

// NewScanner creates a new Scanner instance.
func NewScanner(config *ScannerConfig) (*Scanner, error) {
	kubeconfig, err := buildConfig(config.KubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	kubeconfig.QPS = float32(config.KubeAPIQPS)
	kubeconfig.Burst = config.KubeAPIBurst
	klog.Infof("KubeClient QPS: %f, Burst: %d", kubeconfig.QPS, kubeconfig.Burst)
	kubeClient, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	rateLimiter := config.RateLimiter
	if rateLimiter == nil {
		rateLimiter = workqueue.DefaultTypedControllerRateLimiter[string]()
	}

	// Factory for PV, PVC and SC informers
	factory := informers.NewSharedInformerFactory(kubeClient, config.ResyncPeriod)
	pvInformer := factory.Core().V1().PersistentVolumes()
	pvcInformer := factory.Core().V1().PersistentVolumeClaims()
	scInformer := factory.Storage().V1().StorageClasses()

	// Factory for Pod informer with label selector and transform
	// The label is patched by the mutating webhook.
	podLabelSelector := fmt.Sprintf("%s=%s", profileManagedLabelKey, profileManagedLabelValue)
	tweakFunc := func(options *metav1.ListOptions) {
		options.LabelSelector = podLabelSelector
	}
	podFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		config.ResyncPeriod,
		informers.WithTweakListOptions(tweakFunc),
		informers.WithTransform(trimPodObject),
	)
	podInformer := podFactory.Core().V1().Pods()
	klog.Infof("Pod informer configured with label selector: %q and a transform function", podLabelSelector)

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	eventRecorder := eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: scannerComponentName})

	scanner := &Scanner{
		kubeClient:         kubeClient,
		factory:            factory,
		podFactory:         podFactory,
		pvLister:           pvInformer.Lister(),
		pvcLister:          pvcInformer.Lister(),
		scLister:           scInformer.Lister(),
		podLister:          podInformer.Lister(),
		pvSynced:           pvInformer.Informer().HasSynced,
		pvcSynced:          pvcInformer.Informer().HasSynced,
		scSynced:           scInformer.Informer().HasSynced,
		podSynced:          podInformer.Informer().HasSynced,
		queue:              workqueue.NewTypedRateLimitingQueue(rateLimiter),
		trackedPVs:         make(map[string]struct{}),
		lastSuccessfulScan: make(map[string]time.Time),
		eventRecorder:      eventRecorder,
	}

	klog.Info("Setting up event handlers for PersistentVolumes")
	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    scanner.addPV,
		DeleteFunc: scanner.deletePV,
		// UpdateFunc is not required because subsequent Pod creation events
		// will trigger the scanner.
	})

	klog.Info("Setting up event handlers for Pods")
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    scanner.addPod,
		DeleteFunc: scanner.deletePod,
	})

	return scanner, nil
}

// Run starts the scanner controller.
func (s *Scanner) Run(ctx context.Context) {
	defer runtime.HandleCrash()
	defer s.queue.ShutDown()

	stopCh := ctx.Done()

	klog.Info("Starting informers")
	s.factory.Start(stopCh)
	s.podFactory.Start(stopCh)

	klog.Info("Waiting for informer caches to sync")
	if !cache.WaitForCacheSync(stopCh, s.pvSynced, s.pvcSynced, s.scSynced, s.podSynced) {
		klog.Error("Failed to wait for caches to sync")
		return
	}
	klog.Info("Informer caches synced successfully")

	klog.Info("Starting worker")
	go wait.Until(func() { s.runWorker(ctx) }, time.Second, ctx.Done())

	klog.Info("Scanner started")
	<-stopCh
	klog.Info("Scanner shutting down")
}

// runWorker runs a worker thread that processes items from the queue.
func (s *Scanner) runWorker(ctx context.Context) {
	for s.processNextWorkItem(ctx) {
	}
	klog.Info("Worker shutting down")
}

// processNextWorkItem retrieves and processes the next item from the work queue.
func (s *Scanner) processNextWorkItem(ctx context.Context) bool {
	key, quit := s.queue.Get()
	if quit {
		return false
	}
	defer s.queue.Done(key)

	var err error
	syncType := "Unknown"
	itemKey := key

	if strings.HasPrefix(key, podPrefix) {
		syncType = "Pod"
		itemKey = strings.TrimPrefix(key, podPrefix)
		klog.V(6).Infof("Processing %s %q", syncType, itemKey)
		err = s.syncPod(ctx, itemKey)
	} else if strings.HasPrefix(key, pvPrefix) {
		syncType = "PV"
		itemKey = strings.TrimPrefix(key, pvPrefix)
		klog.V(6).Infof("Processing %s %q", syncType, itemKey)
		err = s.syncPV(ctx, itemKey)

		// PV specific tracking removal
		if err == nil {
			s.pvMutex.Lock()
			if _, exists := s.trackedPVs[itemKey]; exists {
				klog.V(6).Infof("PV %s finished processing, removing from tracking", itemKey)
				delete(s.trackedPVs, itemKey)
			}
			s.pvMutex.Unlock()
		}
	} else {
		klog.Errorf("Unknown key prefix for %q", key)
		s.queue.Forget(key)
		return true
	}

	if err == nil {
		s.queue.Forget(key)
		klog.V(6).Infof("Successfully synced %s %q", syncType, itemKey)
	} else if errors.Is(err, errRequeuePod) {
		// Specific requeue signal for Pods waiting on PV scans or PVC binds.
		// This is not a "true" error, so it doesn't need to be logged as such.
		klog.Infof("Requeuing %s %q: %v", syncType, itemKey, err)
		s.queue.AddRateLimited(key)
	} else {
		// All other errors will be requeued with exponential back-off and
		// logged as errors, e.g. Kubernetes API server or transient errors.
		klog.Errorf("Error syncing %s %q: %v", syncType, itemKey, err)
		s.queue.AddRateLimited(key)
	}
	return true
}

// syncPod is the core reconciliation function for a Pod. It checks if any of the
// Pod's PVs require scanning and removes the Pod's scheduling gate:
//  1. If any PV requires scanning, both the PVs and the Pod are enqueued to be
//     handled later. This eventually results in the PV being scanned by syncPV,
//     and the Pod's scheduling gate being removed by syncPod (step #2).
//  2. If none of the PVs require scanning, the Pod's scheduling gate is removed,
//     allowing the Kubernetes scheduler to find a Node for the Pod.
//
// The function returns the error `errRequeuePod` if the Pod should be requeued
// but the error doesn't need to be logged, for example: If the PVC is unbound
// or the volumes are not scanned yet.
func (s *Scanner) syncPod(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("Failed to split meta namespace key %q: %v", key, err)
		return nil // Don't re-queue bad keys
	}

	pod, err := s.podLister.Pods(namespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Pod %q in namespace %q has been deleted", name, namespace)
			return nil
		}
		klog.Errorf("Failed to get Pod %q in namespace %q: %v", name, namespace, err)
		return err
	}

	klog.Infof("Syncing Pod: %s/%s", pod.Namespace, pod.Name)

	var anyPVRelevant bool
	var needsRequeue bool

	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			continue
		}

		pvcName := vol.PersistentVolumeClaim.ClaimName
		pvc, err := s.pvcLister.PersistentVolumeClaims(namespace).Get(pvcName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.Warningf("PVC %s/%s for Pod %s not found, will recheck Pod later", namespace, pvcName, key)
				needsRequeue = true
				continue
			}
			klog.Errorf("Failed to get PVC %s/%s for Pod %s: %v", namespace, pvcName, key, err)
			return fmt.Errorf("failed to get PVC %s/%s: %w", namespace, pvcName, err) // API server error, retry with backoff
		}

		pvName := pvc.Spec.VolumeName
		if pvName == "" {
			klog.Infof("PVC %s/%s for Pod %s is not bound to a PV yet, requeue Pod to evaluate later", namespace, pvcName, key)
			needsRequeue = true
			continue
		}

		pv, err := s.pvLister.Get(pvName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.Warningf("PV %s (from PVC %s/%s) for Pod %s not found, this should not happen if PVC is bound", pvName, namespace, pvcName, key)
				// This state is unexpected, but treat as transient.
				return fmt.Errorf("PV %s not found: %w", pvName, err)
			}
			klog.Errorf("Failed to get PV %s for Pod %s (from PVC %s/%s): %v", pvName, key, namespace, pvcName, err)
			return fmt.Errorf("failed to get PV %s: %w", pvName, err) // API server error, retry with backoff
		}

		bucketInfo, err := s.checkPVRelevance(pv)
		if err != nil {
			return fmt.Errorf("error checking PV %s relevance for Pod %s: %v", pvName, key, err)
		}

		if bucketInfo != nil {
			klog.Infof("Pod %s uses relevant PV %s (PVC %s/%s)", key, pvName, namespace, pvcName)
			anyPVRelevant = true
			s.enqueuePVIfNotTracked(pv) // Enqueue the PV for scanning if not already tracked
		}
	}

	if anyPVRelevant {
		klog.Infof("Pod %s uses one or more relevant PVs, will recheck Pod later to ensure scans complete", key)
		return fmt.Errorf("%w: waiting for PV scans to complete for Pod %s", errRequeuePod, key)
	}

	if needsRequeue {
		klog.Infof("Pod %s has unbound or missing PVCs, will recheck Pod later", key)
		return fmt.Errorf("%w: waiting for PVCs to be ready for Pod %s", errRequeuePod, key)
	}

	// If no PVs are relevant and no other reason to requeue, remove the scheduling gate
	return s.removeSchedulingGate(ctx, pod)
}

func (s *Scanner) removeSchedulingGate(ctx context.Context, pod *v1.Pod) error {
	var newGates []v1.PodSchedulingGate
	gateFound := false
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == schedulingGateName {
			gateFound = true
		} else {
			newGates = append(newGates, gate)
		}
	}

	if !gateFound {
		klog.V(6).Infof("Scheduling gate %s not found on Pod %s/%s", schedulingGateName, pod.Namespace, pod.Name)
		return nil // Nothing to do
	}

	klog.Infof("Removing scheduling gate %s from Pod %s/%s", schedulingGateName, pod.Namespace, pod.Name)

	patchData := map[string]any{
		"spec": map[string]any{
			"schedulingGates": newGates,
		},
	}
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to marshal patch data for Pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}

	_, err = s.kubeClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Warningf("Failed to patch Pod %s/%s because it was not found", pod.Namespace, pod.Name)
			return nil
		}
		return fmt.Errorf("failed to patch Pod %s/%s to remove scheduling gate: %w", pod.Namespace, pod.Name, err)
	}

	klog.Infof("Successfully removed scheduling gate %s from Pod %s/%s", schedulingGateName, pod.Namespace, pod.Name)
	return nil
}

func (s *Scanner) getDurationAttribute(pv *v1.PersistentVolume, attributeKey string, defaultDuration time.Duration) (*time.Duration, error) {
	if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
		if durationStr, ok := pv.Spec.CSI.VolumeAttributes[attributeKey]; ok {
			parsedDuration, err := time.ParseDuration(durationStr)
			if err != nil {
				return nil, fmt.Errorf("invalid duration format for %s: %q, error: %w", attributeKey, durationStr, err)
			}
			if parsedDuration <= 0 {
				return nil, fmt.Errorf("non-positive duration for %s: %q", attributeKey, durationStr)
			}
			klog.Infof("PV %s: Using %q key from VolumeAttributes: %s", pv.Name, attributeKey, parsedDuration)
			return &parsedDuration, nil
		}
	}
	klog.V(6).Infof("PV %s: No %q key in VolumeAttributes. Using default %s", pv.Name, attributeKey, defaultDuration)
	return &defaultDuration, nil
}

// syncPV is the core reconciliation function for a PersistentVolume.
// It checks if the PV is relevant, performs the bucket scan, and updates the PV annotations.
func (s *Scanner) syncPV(ctx context.Context, key string) error {
	syncStartTime := timeNow()
	klog.Infof("Started syncing PV %q", key)

	pv, err := s.pvLister.Get(key)
	if err != nil {
		// The PV may have already been deleted. This is normal and we should remove it from tracking.
		if apierrors.IsNotFound(err) {
			klog.Infof("PV %q has been deleted, removing from tracking", key)
			return nil
		}
		klog.Errorf("Failed to get PV %q from lister: %v", key, err)
		return err
	}

	// Skip PVs that are not relevant, e.g. PVs that don't use the gcsfuse profiles feature or
	// that have been scanned too recently.
	bucketInfo, err := s.checkPVRelevance(pv)
	if err != nil {
		klog.Errorf("Relevance check failed for PV %s: %v", pv.Name, err)
		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationStartError, "Relevance check failed: %v", err)
		return fmt.Errorf("relevance check failed for PV %s: %w", pv.Name, err)
	}
	if bucketInfo == nil {
		klog.V(6).Infof("PV %q is no longer relevant, skipping sync", key)
		return nil // Remove irrelevant PV from queue
	}
	klog.Infof("PV %q is relevant, bucket: %s, dir: %s", key, bucketInfo.name, bucketInfo.dir)

	// ----- At this stage, the PV has been considered eligible for a scan. -----

	// Get the bucket scan timeout limit. This may have been overriden by the customer.
	currentScanTimeout, err := s.getDurationAttribute(pv, volumeAttributeScanTimeoutKey, defaultScanTimeoutDuration)
	if err != nil {
		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationStartError, "Bucket scan timeout configuration error: %v", err)
		return nil // Avoid re-queueing on static customer misconfig.
	}

	s.eventRecorder.Eventf(pv, v1.EventTypeNormal, reasonScanOperationStartSucceeded, "Started bucket scan for PV %s, bucket %s, directory '%s', with timeout %s", pv.Name, bucketInfo.name, bucketInfo.dir, currentScanTimeout)
	klog.Infof("Bucket scan operation starting for PV %s, bucket %s, dir %s, timeout %s", pv.Name, bucketInfo.name, bucketInfo.dir, currentScanTimeout)

	info, err := scanBucket(ctx, bucketInfo, *currentScanTimeout)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			// A bucket scan timeout is benign and expected if the bucket has a large number of objects.
			// We send a warning only to inform the customer about the timeout.
			duration := timeNow().Sub(syncStartTime)
			s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationTimedOut, "Bucket scan timed out after %s for bucket %s, directory '%s' (%v). Updating with partial results. Consider increasing timeout if bucket size is big", currentScanTimeout, bucketInfo.name, bucketInfo.dir, duration)
			if patchErr := s.updatePVScanResult(ctx, pv, info, scanTimeout); patchErr != nil {
				return fmt.Errorf("failed to patch PV %s after timeout, err: %w", pv.Name, patchErr)
			}
			return nil // Remove since we still consider this a successful scan.
		}
		// For any other error, re-queue.
		klog.Errorf("Error scanning bucket for PV %s: %v", pv.Name, err)
		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationFailed, "Bucket scan failed for bucket %s, directory '%s': %v", bucketInfo.name, bucketInfo.dir, err)
		return fmt.Errorf("error scanning bucket for PV %s: %w", pv.Name, err)
	}

	// The scan has been successful and complete results have been used.
	duration := timeNow().Sub(syncStartTime)
	s.eventRecorder.Eventf(pv, v1.EventTypeNormal, reasonScanOperationSucceeded, "Bucket scan completed successfully for bucket %s, directory '%s' (%v)", bucketInfo.name, bucketInfo.dir, duration)
	if patchErr := s.updatePVScanResult(ctx, pv, info, scanCompleted); patchErr != nil {
		return fmt.Errorf("failed to patch PV %s results, err: %w", pv.Name, patchErr)
	}
	return nil // Remove since this is a complete and successful scan.
}

// defaultScanBucket simulates scanning a GCS bucket to gather metadata.
// It collects the number of objects, total size, and HNS status.
// This function respects the provided context and the scanTimeout.
// It returns partial results if the timeout is reached (context.DeadlineExceeded).
// TODO(urielguzman): Add real Dataflux and bucket metrics fallback logic in subsequent PR.
func defaultScanBucket(ctx context.Context, info *bucketInfo, scanTimeout time.Duration) (*bucketInfo, error) {
	klog.Infof("Simulating scan for bucket: %s, dir: %s, timeout: %s", info.name, info.dir, scanTimeout)
	scanCtx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	result := &bucketInfo{
		name:         info.name,
		dir:          info.dir,
		isHNSEnabled: true, // Example
	}

	// Simulate work in ticks
	tickDuration := 200 * time.Millisecond
	ticker := time.NewTicker(tickDuration)
	defer ticker.Stop()

	for {
		select {
		case <-scanCtx.Done():
			// This is the scanTimeout
			klog.Warningf("Scan for bucket %s, dir %s timed out after %s. Returning partial results: %+v", result.name, result.dir, scanTimeout, result)
			return result, context.DeadlineExceeded
		case <-ctx.Done():
			// This means the caller cancelled the operation.
			klog.Infof("Scan for bucket %s, dir %s cancelled by caller", result.name, result.dir)
			return nil, ctx.Err()
		case <-ticker.C:
			// Simulate incremental discovery of objects and size
			result.numObjects += 1000
			result.totalSizeBytes += 1000000
		}
	}
}

// patchPVAnnotations updates the annotations for a given PV.
// annotationsToUpdate should contain the desired state of the annotations to be patched.
func (s *Scanner) patchPVAnnotations(ctx context.Context, pvName string, annotationsToUpdate map[string]*string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled: %w", err)
	}
	patchData := map[string]any{
		"metadata": map[string]any{"annotations": annotationsToUpdate},
	}
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to marshal annotation patch data for PV %s: %w", pvName, err)
	}
	klog.V(6).Infof("Patching PV %s annotations with: %s", pvName, string(patchBytes))
	_, err = s.kubeClient.CoreV1().PersistentVolumes().Patch(ctx, pvName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Warningf("Failed to patch PV %s because it was not found", pvName)
			return nil
		}
		return fmt.Errorf("failed to patch PV %s annotations: %w", pvName, err)
	}
	klog.V(6).Infof("Successfully patched annotations for PV %s", pvName)
	return nil
}

// updatePVScanResult updates the PV annotations with the results of a bucket scan.
// It sets the status, number of objects, total size, HNS status, and last updated time.
// It also updates the in-memory lastSuccessfulScan map.
func (s *Scanner) updatePVScanResult(ctx context.Context, pv *v1.PersistentVolume, info *bucketInfo, status string) error {
	currentTime := timeNow()
	annotationsToUpdate := map[string]*string{
		annotationStatus:          stringPtr(status),
		annotationNumObjects:      int64Ptr(info.numObjects),
		annotationTotalSize:       int64Ptr(info.totalSizeBytes),
		annotationLastUpdatedTime: stringPtr(currentTime.UTC().Format(time.RFC3339)),
		annotationHNSEnabled:      boolPtr(info.isHNSEnabled),
	}
	klog.Infof("Updating PV %s with scan result: %+v, status: %s", pv.Name, info, status)
	err := s.patchPVAnnotations(ctx, pv.Name, annotationsToUpdate)
	if err != nil {
		klog.Errorf("Failed to update annotations on PV %s with status %s: %v", pv.Name, status, err)
		return err
	}
	klog.Infof("Successfully updated annotations on PV %s with status %s", pv.Name, status)

	// Update in-memory map only on terminal state updates.
	if status == scanCompleted || status == scanTimeout {
		s.scanMutex.Lock()
		s.lastSuccessfulScan[pv.Name] = currentTime
		s.scanMutex.Unlock()
		klog.V(6).Infof("Updated lastSuccessfulScan map for PV %s to %s", pv.Name, currentTime)
	}
	return nil
}

// checkPVRelevance determines if a PersistentVolume is relevant for scanning.
// A PV is relevant if it uses the gcsfuse CSI driver and its StorageClass
// has a workloadType parameter set to inference, training, or checkpointing.
// The PV is relevant if the current time - last scan time > scan TTL
// This function returns a bucketInfo with the bucket name and the directory if
// relevant, otherwise, it will return nil and any error.
func (s *Scanner) checkPVRelevance(pv *v1.PersistentVolume) (*bucketInfo, error) {
	if pv == nil {
		return nil, nil
	}
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != csiDriverName || pv.Spec.CSI.VolumeHandle == "" {
		return nil, nil
	}

	scName := pv.Spec.StorageClassName
	if scName == "" {
		return nil, nil
	}
	sc, err := s.scLister.Get(scName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get StorageClass %s: %w", scName, err)
		}
		// If the customer specifies a "dummy" StorageClass, this must be handled gracefully. Example:
		// https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-pv#create-a-persistentvolume
		klog.Warningf("StorageClass %s not found for PV %s", scName, pv.Name)
		return nil, nil
	}

	scParams := sc.Parameters
	if scParams == nil {
		return nil, nil
	}
	workloadType, ok := scParams[paramWorkloadTypeKey]
	if !ok {
		klog.V(6).Infof("Workload type parameter key %s was not found in StorageClass %s for PV %s", paramWorkloadTypeKey, scName, pv.Name)
		return nil, nil
	}

	// ---- At this stage, there is clearly a customer intent to use the scanner feature, so we start logging warnings -----

	switch workloadType {
	case paramWorkloadTypeInferenceKey, paramWorkloadTypeTrainingKey, paramWorkloadTypeCheckpointingKey:
	default:
		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationStartError, "Found invalid '%s' parameter %q in StorageClass %s for PV %s", paramWorkloadTypeKey, workloadType, scName, pv.Name)
		return nil, nil // Avoid re-queuing on static customer misconfig.
	}

	// Check if the scan should be skipped based on TTL
	currentScanTTL, err := s.getDurationAttribute(pv, volumeAttributeScanTTLKey, defaultScanTTLDuration)
	if err != nil {
		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationStartError, "Bucket scan TTL configuration error: %v", err)
		return nil, nil // Avoid re-queuing on static customer misconfig.
	}

	var lastScanTime time.Time
	found := false
	source := ""

	s.scanMutex.RLock()
	if lastTime, ok := s.lastSuccessfulScan[pv.Name]; ok {
		lastScanTime = lastTime
		found = true
		source = "memory"
		klog.V(6).Infof("PV %s: Found last scan time in memory: %s", pv.Name, lastScanTime)
	}
	s.scanMutex.RUnlock()

	if !found {
		if lastUpdatedTimeStr, ok := pv.Annotations[annotationLastUpdatedTime]; ok {
			parsedTime, err := time.Parse(time.RFC3339, lastUpdatedTimeStr)
			if err != nil {
				klog.Warningf("PV %s: Failed to parse annotation %s value %q: %v. Assuming no recent scan", pv.Name, annotationLastUpdatedTime, lastUpdatedTimeStr, err)
			} else {
				lastScanTime = parsedTime
				found = true
				source = "annotation"
				klog.V(6).Infof("PV %s: Found last scan time in annotations: %s", pv.Name, lastScanTime)
			}
		}
	}

	if found {
		elapsed := timeNow().Sub(lastScanTime)
		if elapsed < *currentScanTTL {
			klog.Infof("PV %s: Skipping scan, only %s elapsed since last scan (source: %s), which is less than TTL %s", pv.Name, elapsed.Round(time.Second), source, currentScanTTL)
			return nil, nil
		}
		klog.V(6).Infof("PV %s: Proceeding with scan, %s elapsed since last scan (source: %s), TTL is %s", pv.Name, elapsed.Round(time.Second), source, currentScanTTL)
	} else {
		klog.V(6).Infof("PV %s: No last scan time found in memory or annotations. Proceeding with scan", pv.Name)
	}

	bucketName := pv.Spec.CSI.VolumeHandle
	var dir string
	for _, mountOption := range pv.Spec.MountOptions {
		if val, ok := getOnlyDirValue(mountOption); ok {
			dir = val
			break
		}
	}
	return &bucketInfo{
		name: bucketName,
		dir:  dir}, nil
}

// getOnlyDirValue parses a mount option string to extract the value of "only-dir".
// It returns the directory value and true if the prefix is found, otherwise empty string and false.
func getOnlyDirValue(s string) (string, bool) {
	prefix := "only-dir="
	if strings.HasPrefix(s, prefix) {
		return strings.TrimPrefix(s, prefix), true
	}
	return "", false
}

// enqueuePV enqueues a PersistentVolume.
func (s *Scanner) enqueuePV(pv *v1.PersistentVolume) {
	key, err := cache.MetaNamespaceKeyFunc(pv)
	if err != nil {
		runtime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pv, err))
		return
	}
	klog.V(6).Infof("Enqueuing PV %q", key)
	s.queue.Add(pvPrefix + key)
}

// enqueuePVIfNotTracked enqueues a PersistentVolume if it's not already tracked.
func (s *Scanner) enqueuePVIfNotTracked(pv *v1.PersistentVolume) {
	s.pvMutex.Lock()
	defer s.pvMutex.Unlock()

	if _, isTracked := s.trackedPVs[pv.Name]; !isTracked {
		s.trackedPVs[pv.Name] = struct{}{}
		klog.V(6).Infof("PV %s: Not tracked, enqueuing for scan", pv.Name)
		s.enqueuePV(pv)
	} else {
		klog.V(6).Infof("PV %s: Already tracked, skipping enqueue", pv.Name)
	}
}

// addPV is the add event handler for PersistentVolumes.
func (s *Scanner) addPV(obj any) {
	pv, ok := obj.(*v1.PersistentVolume)
	if !ok {
		klog.Errorf("AddFunc PV: Expected PersistentVolume but got %T", obj)
		return
	}
	s.handlePVEvent(pv, "ADD")
}

// This function is called by addPV and updatePV event handlers.
func (s *Scanner) handlePVEvent(pv *v1.PersistentVolume, eventType string) {
	bucketInfo, err := s.checkPVRelevance(pv)
	if err != nil {
		klog.Errorf("PV %s: Error checking relevance for %s: %v", eventType, pv.Name, err)
		return
	}

	if bucketInfo == nil {
		klog.V(6).Infof("PV %s: %s - Not relevant", eventType, pv.Name)
		return
	}

	s.enqueuePVIfNotTracked(pv)
}

// deletePV is the event handler for PersistentVolume Delete events.
// It removes the PV from the workqueue and the internal tracking map.
func (s *Scanner) deletePV(obj any) {
	pv, ok := obj.(*v1.PersistentVolume)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Errorf("DeleteFunc PV: Expected PV or Tombstone but got %T", obj)
			return
		}
		pv, ok = tombstone.Obj.(*v1.PersistentVolume)
		if !ok {
			klog.Errorf("DeleteFunc PV: Expected PV in Tombstone but got %T", tombstone.Obj)
			return
		}
		klog.V(6).Infof("PV TOMBSTONE: %s", pv.Name)
	} else {
		klog.V(6).Infof("PV DELETED: %s", pv.Name)
	}

	key, err := cache.MetaNamespaceKeyFunc(pv)
	if err != nil {
		klog.Errorf("DeleteFunc PV: Error mapping key from object: %v", err)
	} else {
		s.pvMutex.Lock()
		delete(s.trackedPVs, key)
		s.pvMutex.Unlock()

		s.scanMutex.Lock()
		delete(s.lastSuccessfulScan, key)
		s.scanMutex.Unlock()
		klog.V(6).Infof("Removed PV %s from lastSuccessfulScan map", key)

		s.queue.Forget(pvPrefix + key)
	}
}

// addPod is the add event handler for Pods.
func (s *Scanner) addPod(obj any) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.Errorf("AddFunc Pod: Expected Pod but got %T", obj)
		return
	}
	klog.V(6).Infof("Pod ADDED: %s/%s", pod.Namespace, pod.Name)
	s.enqueuePod(pod)
}

// deletePod is the event handler for Pod Delete events.
func (s *Scanner) deletePod(obj any) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Errorf("DeleteFunc Pod: Expected Pod or Tombstone but got %T", obj)
			return
		}
		pod, ok = tombstone.Obj.(*v1.Pod)
		if !ok {
			klog.Errorf("DeleteFunc Pod: Expected Pod in Tombstone but got %T", tombstone.Obj)
			return
		}
		klog.V(6).Infof("Pod TOMBSTONE: %s/%s", pod.Namespace, pod.Name)
	} else {
		klog.V(6).Infof("Pod DELETED: %s/%s", pod.Namespace, pod.Name)
	}

	key, err := cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.Errorf("DeleteFunc Pod: Error mapping key from object: %v", err)
		return
	}
	klog.V(6).Infof("Forgetting Pod %q from queue", key)
	s.queue.Forget(podPrefix + key)
}

// enqueuePod enqueues a Pod.
func (s *Scanner) enqueuePod(pod *v1.Pod) {
	key, err := cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		runtime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pod, err))
		return
	}
	klog.V(6).Infof("Enqueuing Pod %q", key)
	s.queue.Add(podPrefix + key)
}
