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

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
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
	pvLister      corev1listers.PersistentVolumeLister
	scLister      storagelisters.StorageClassLister
	pvSynced      cache.InformerSynced
	scSynced      cache.InformerSynced
	factory       informers.SharedInformerFactory
	queue         workqueue.TypedRateLimitingInterface[string]
	eventRecorder record.EventRecorder
	// Set of PV names being tracked/processed. This is
	// used to avoid reprocessing if multiple user Pods
	// trigger a PV addition to the queue.
	trackedPVs map[string]struct{}
	// Mutex to protect trackedPVs.
	pvMutex sync.RWMutex
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
	factory := informers.NewSharedInformerFactory(kubeClient, config.ResyncPeriod)
	pvInformer := factory.Core().V1().PersistentVolumes()
	scInformer := factory.Storage().V1().StorageClasses()

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	eventRecorder := eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: scannerComponentName})

	scanner := &Scanner{
		kubeClient:    kubeClient,
		factory:       factory,
		pvLister:      pvInformer.Lister(),
		scLister:      scInformer.Lister(),
		pvSynced:      pvInformer.Informer().HasSynced,
		scSynced:      scInformer.Informer().HasSynced,
		queue:         workqueue.NewTypedRateLimitingQueue(rateLimiter),
		trackedPVs:    make(map[string]struct{}),
		eventRecorder: eventRecorder,
	}

	klog.Info("Setting up event handlers for PersistentVolumes")
	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    scanner.addPV,
		DeleteFunc: scanner.deletePV,
		// UpdateFunc is not required because subsequent Pod creation events
		// will trigger the scanner.
	})

	// TODO(urielguzman): Add logic to make Pod creation events trigger the scanner
	// and remove the scheduling gates.

	return scanner, nil
}

// Run starts the scanner controller.
func (s *Scanner) Run(ctx context.Context) {
	defer runtime.HandleCrash()
	defer s.queue.ShutDown()

	stopCh := ctx.Done()

	klog.Info("Starting informers")
	s.factory.Start(stopCh)

	klog.Info("Waiting for informer caches to sync")
	if !cache.WaitForCacheSync(stopCh, s.pvSynced, s.scSynced) {
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

	klog.V(6).Infof("Processing PV %q", key)
	err := s.syncPV(ctx, key)
	if err == nil {
		s.queue.Forget(key)
		s.pvMutex.Lock()
		if _, exists := s.trackedPVs[key]; exists {
			klog.V(6).Infof("PV %s finished processing, removing from tracking", key)
			delete(s.trackedPVs, key)
		}
		s.pvMutex.Unlock()
		klog.V(6).Infof("Successfully synced PV %q", key)
	} else {
		klog.Errorf("Error syncing PV %q: %v", key, err)
		s.queue.AddRateLimited(key)
	}
	return true
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
func (s *Scanner) updatePVScanResult(ctx context.Context, pv *v1.PersistentVolume, info *bucketInfo, status string) error {
	annotationsToUpdate := map[string]*string{
		annotationStatus:          stringPtr(status),
		annotationNumObjects:      int64Ptr(info.numObjects),
		annotationTotalSize:       int64Ptr(info.totalSizeBytes),
		annotationLastUpdatedTime: stringPtr(timeNow().UTC().Format(time.RFC3339)),
		annotationHNSEnabled:      boolPtr(info.isHNSEnabled),
	}
	klog.Infof("Updating PV %s with scan result: %+v, status: %s", pv.Name, info, status)
	err := s.patchPVAnnotations(ctx, pv.Name, annotationsToUpdate)
	if err != nil {
		klog.Errorf("Failed to update annotations on PV %s with status %s: %v", pv.Name, status, err)
		return err
	}
	klog.Infof("Successfully updated annotations on PV %s with status %s", pv.Name, status)
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
	if lastUpdatedTimeStr, ok := pv.Annotations[annotationLastUpdatedTime]; ok {
		lastUpdatedTime, err := time.Parse(time.RFC3339, lastUpdatedTimeStr)
		if err != nil {
			klog.Warningf("PV %s: Failed to parse annotation %s value %q: %v. Proceeding with scan", pv.Name, annotationLastUpdatedTime, lastUpdatedTimeStr, err)
		} else {
			elapsed := timeNow().Sub(lastUpdatedTime)
			if elapsed < *currentScanTTL {
				klog.Infof("PV %s: Skipping scan, only %s elapsed since last scan, which is less than TTL %s", pv.Name, elapsed.Round(time.Second), currentScanTTL)
				return nil, nil
			}
			klog.V(6).Infof("PV %s: Proceeding with scan, %s elapsed since last scan, TTL is %s", pv.Name, elapsed.Round(time.Second), currentScanTTL)
		}
	} else {
		klog.V(6).Infof("PV %s: No last updated time annotation found. Proceeding with scan", pv.Name)
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
	s.queue.Add(key)
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

	s.pvMutex.Lock()
	defer s.pvMutex.Unlock()

	if _, isTracked := s.trackedPVs[pv.Name]; !isTracked {
		s.trackedPVs[pv.Name] = struct{}{}
		klog.V(6).Infof("PV %s: %s - New relevant PV. Enqueuing for scan", eventType, pv.Name)
		s.enqueuePV(pv)
	} else {
		klog.V(6).Infof("PV %s: %s - Already tracked. No action", eventType, pv.Name)
	}
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
		s.queue.Forget(key)
	}
}
