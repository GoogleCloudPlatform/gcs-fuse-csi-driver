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
	"fmt"
	"time"

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
	corev1listers "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
)

const (
	scannerComponentName = "gke-gcsfuse-scanner"

	// Event reasons
	ReasonScannerStarted              = "ScannerStarted"
	ReasonScanOperationStartError     = "ScanOperationStartError"
	ReasonScanOperationStartSucceeded = "ScanOperationStartSucceeded"
	ReasonScanOperationFailed         = "ScanOperationFailed"
	ReasonScanOperationSucceeded      = "ScanOperationSucceeded"
	ReasonScanOperationTimedOut       = "ScanOperationTimedOut"
	ReasonScannerFinished             = "ScannerFinished"
)

// ScannerConfig holds the configuration for the Scanner.
type ScannerConfig struct {
	KubeAPIQPS     float64       // QPS limit for Kubernetes API client.
	KubeAPIBurst   int           // Burst limit for Kubernetes API client.
	ResyncPeriod   time.Duration // Resync period for informers.
	KubeConfigPath string        // Optional: Path to kubeconfig file. If empty, InClusterConfig is used.
	RateLimiter    workqueue.TypedRateLimiter[string]
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
		eventRecorder: eventRecorder,
	}

	klog.Info("Setting up event handlers for PersistentVolumes")
	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    scanner.addPV,
		UpdateFunc: scanner.updatePV,
		DeleteFunc: scanner.deletePV,
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
	// TODO(urielguzman): Get PV object
	// TODO(urielguzman): Implement syncPV for actual logic
	err := s.syncPV(ctx, key)
	if err == nil {
		s.queue.Forget(key)
		klog.V(6).Infof("Successfully synced PV %q", key)
	} else {
		klog.Errorf("Error syncing PV %q: %v", key, err)
		s.queue.AddRateLimited(key)
	}
	return true
}

// syncPV is the main reconciliation function for a PV.
// Currently, it's a placeholder.
func (s *Scanner) syncPV(_ context.Context, key string) error {
	klog.V(6).Infof("syncPV called for %q", key)
	// TODO(urielguzman): Add relevance check
	// TODO(urielguzman): Add scanning logic
	// TODO(urielguzman): Add annotation logic
	return nil
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
	klog.V(6).Infof("PV ADDED: %s", pv.Name)
	s.enqueuePV(pv)
}

// updatePV is the update event handler for PersistentVolumes.
func (s *Scanner) updatePV(oldObj, newObj any) {
	oldPV, ok := oldObj.(*v1.PersistentVolume)
	if !ok {
		klog.Errorf("UpdateFunc PV: old object is not a PersistentVolume, got %T", oldObj)
		return
	}
	newPV, ok := newObj.(*v1.PersistentVolume)
	if !ok {
		klog.Errorf("UpdateFunc PV: new object is not a PersistentVolume, got %T", newObj)
		return
	}
	if oldPV.ResourceVersion == newPV.ResourceVersion {
		return
	}
	klog.V(6).Infof("PV UPDATED: %s", newPV.Name)
	s.enqueuePV(newPV)
}

// deletePV is the delete event handler for PersistentVolumes.
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
	s.enqueuePV(pv) // Enqueue on delete to handle any cleanup if necessary
}
