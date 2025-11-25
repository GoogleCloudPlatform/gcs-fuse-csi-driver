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

package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/googleapis/gax-go/v2"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/auth"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/gcfg.v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"cloud.google.com/go/storage"
	"cloud.google.com/go/storage/dataflux"
	"google.golang.org/genproto/googleapis/api/metric"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	monitoringpb "cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	control "cloud.google.com/go/storage/control/apiv2"
	controlpb "cloud.google.com/go/storage/control/apiv2/controlpb"
	profilesutil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	compute "google.golang.org/api/compute/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	v1listers "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
)

const (
	scannerComponentName                        = "gke-gcsfuse-scanner"
	csiDriverName                               = "gcsfuse.csi.storage.gke.io"
	volumeAttributeScanTimeoutKey               = "bucketScanTimeout"
	volumeAttributeScanTTLKey                   = "bucketScanTTL"
	volumeAttributeAnywhereCacheAdmissionPolicy = "anywhereCacheAdmissionPolicy"
	volumeAttributeEnableAnywhereCache          = "enableAnywhereCache"
	volumeAttributeAnywhereCacheTTL             = "anywhereCacheTTL"
	leaseName                                   = "gke-gcsfuse-scanner-leader"

	// Event reasons
	reasonScanOperationStartError                   = "ScanOperationStartError"
	reasonScanOperationStartSucceeded               = "ScanOperationStartSucceeded"
	reasonScanOperationFailed                       = "ScanOperationFailed"
	reasonScanOperationWarning                      = "ScanOperationWarning"
	reasonScanOperationSucceeded                    = "ScanOperationSucceeded"
	reasonScanOperationTimedOut                     = "ScanOperationTimedOut"
	reasonScanOperationAnywhereCacheCreateError     = "ScanOperationAnywhereCacheCreateError"
	reasonScanOperationAnywhereCacheCreateSucceeded = "ScanOperationAnywhereCacheCreateSucceeded"

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

	// Cloud Monitoring metrics
	objectCountMetric = "storage.googleapis.com/storage/object_count"
	totalBytesMetric  = "storage.googleapis.com/storage/v2/total_bytes"

	// Anywhere Cache constants
	admitOnFirstMiss  = "admit-on-first-miss"
	admitOnSecondMiss = "admit-on-second-miss"
)

var (
	// defaultScanTimeoutDuration is the default timeout for a single bucket scan.
	defaultScanTimeoutDuration = 2 * time.Minute

	// defaultScanTTLDuration is the default TTL for skipping bucket scans.
	defaultScanTTLDuration = 168 * time.Hour // 7 days

	// To allow mocking time in tests
	timeNow = time.Now

	// Fake error to signal worker to re-queue the Pod without emitting an error log.
	errRequeuePod = errors.New("requeuing pod")

	bucketAttrs            = defaultBucketAttrs
	scanBucketWithMetrics  = defaultScanBucketWithMetrics
	scanBucketWithDataflux = defaultScanBucketWithDataflux

	// anywhere cache vars
	hourDuration = durationpb.New(time.Hour)

	// Used for testing
	utilGetZonesForClusterLocation = util.GetZonesForALocation
)

// stringPtr returns a pointer to the passed string.
func stringPtr(s string) *string { return &s }

// int64Ptr returns a pointer to the string representation of the passed int64.
func int64Ptr(i int64) *string { return stringPtr(strconv.FormatInt(i, 10)) }

type ConfigFile struct {
	Global ConfigGlobal `gcfg:"global"`
}

type ConfigGlobal struct {
	TokenURL  string `gcfg:"token-url"`
	TokenBody string `gcfg:"token-body"`
}

// DatafluxConfig holds the configuration for the Dataflux lister.
type DatafluxConfig struct {
	Parallelism          int
	BatchSize            int
	SkipDirectoryObjects bool
}

// ScannerConfig holds the configuration for the Scanner.
type ScannerConfig struct {
	KubeAPIQPS                       float64       // QPS limit for Kubernetes API client.
	KubeAPIBurst                     int           // Burst limit for Kubernetes API client.
	ResyncPeriod                     time.Duration // Resync period for informers.
	KubeConfigPath                   string        // Optional: Path to kubeconfig file. If empty, InClusterConfig is used.
	CloudConfigPath                  string
	RateLimiter                      workqueue.TypedRateLimiter[string]
	DatafluxConfig                   *DatafluxConfig
	LeaderElection                   bool
	LeaderElectionNamespace          string
	LeaderElectionLeaseDuration      time.Duration
	LeaderElectionRenewDeadline      time.Duration
	LeaderElectionRetryPeriod        time.Duration
	LeaderElectionHealthCheckTimeout time.Duration
	ClusterLocation                  string
	ProjectNumber                    string
	HTTPEndpoint                     string
}

// bucketInfo holds the results of a bucket scan.
// isOverride will be true if the PV is using the "override" status.
type bucketInfo struct {
	name             string
	dir              string
	projectNumber    string
	onlyDirSpecified bool
	numObjects       int64
	totalSizeBytes   int64
	isOverride       bool
}

// Scanner is the main controller structure.
type Scanner struct {
	kubeClient           kubernetes.Interface
	pvLister             v1listers.PersistentVolumeLister
	pvcLister            v1listers.PersistentVolumeClaimLister
	scLister             storagelisters.StorageClassLister
	podLister            v1listers.PodLister
	pvSynced             cache.InformerSynced
	pvcSynced            cache.InformerSynced
	scSynced             cache.InformerSynced
	podSynced            cache.InformerSynced
	factory              informers.SharedInformerFactory
	podFactory           informers.SharedInformerFactory
	queue                workqueue.TypedRateLimitingInterface[string]
	eventRecorder        record.EventRecorder
	datafluxConfig       *DatafluxConfig
	gcsClient            *storage.Client
	metricClient         *monitoring.MetricClient
	config               *ScannerConfig
	computeService       *compute.Service
	storageControlClient storageControlClient
	metricManager        metrics.PrometheusMetricManager
	mux                  *http.ServeMux

	// scanBucket is a function to scan the bucket, can be overridden in tests.
	scanBucketImpl func(scanner *Scanner, ctx context.Context, bucketI *bucketInfo, scanTimeout time.Duration, pv *v1.PersistentVolume) error

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

	// Identity of this controller, generated at creation time and not persisted
	// across restarts. Useful only for debugging, for seeing the source of events.
	id string
}

// storageControlClient defines the interface that the real and mock clients satisfy.
// This allows us to swap the real client with a mock one during tests.
type storageControlClient interface {
	CreateAnywhereCache(context.Context, *controlpb.CreateAnywhereCacheRequest, ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error)
	Close() error
}

// buildKubeConfig creates a Kubernetes rest.Config for the client.
func buildKubeConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("error building kubeconfig from path %q: %w", kubeconfigPath, err)
		}
		klog.Infof("Using Kubeconfig: %q", kubeconfigPath)
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

func generateTokenSource(ctx context.Context, configFile *ConfigFile) (oauth2.TokenSource, error) {
	// If configFile.Global.TokenURL is defined use AltTokenSource
	if configFile != nil && configFile.Global.TokenURL != "" && configFile.Global.TokenURL != "nil" {
		tokenSource := auth.NewAltTokenSource(ctx, configFile.Global.TokenURL, configFile.Global.TokenBody)
		klog.Infof("Using AltTokenSource %#v", tokenSource)
		return tokenSource, nil
	}

	// Use DefaultTokenSource
	tokenSource, err := google.DefaultTokenSource(
		ctx,
		compute.CloudPlatformScope)

	// DefaultTokenSource relies on GOOGLE_APPLICATION_CREDENTIALS env var being set.
	if gac, ok := os.LookupEnv("GOOGLE_APPLICATION_CREDENTIALS"); ok {
		klog.Infof("GOOGLE_APPLICATION_CREDENTIALS env var set %v", gac)
	} else {
		klog.Warningf("GOOGLE_APPLICATION_CREDENTIALS env var not set")
	}
	klog.Infof("Using DefaultTokenSource %#v", tokenSource)

	return tokenSource, err
}

func buildCloudConfig(configPath string) (*ConfigFile, error) {
	if configPath == "" {
		return nil, nil
	}

	reader, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("couldn't open cloud provider configuration at %s: %w", configPath, err)
	}
	defer reader.Close()

	cfg := &ConfigFile{}
	if err := gcfg.FatalOnly(gcfg.ReadInto(cfg, reader)); err != nil {
		return nil, fmt.Errorf("couldn't read cloud provider configuration at %s: %w", configPath, err)
	}
	klog.Infof("Config file read %#v", cfg)

	return cfg, nil
}

// NewScanner creates a new Scanner instance.
func NewScanner(config *ScannerConfig) (*Scanner, error) {
	kubeconfig, err := buildKubeConfig(config.KubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	// Add a uniquifier so that two processes on the same host don't accidentally both become active.
	hostname, err := os.Hostname()
	if err != nil {
		klog.Errorf("Failed to get hostname for scanner controller: %v", err)
		return nil, err
	}
	id := hostname + "_" + string(uuid.NewUUID())

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
	mux := http.NewServeMux()

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
		datafluxConfig:     config.DatafluxConfig,
		scanBucketImpl:     defaultScanBucket,
		config:             config,
		id:                 id,
		metricManager:      metrics.NewPrometheusMetricManager(config.HTTPEndpoint, mux),
		mux:                mux,
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

// run is the main function of the Scanner. It initializes necessary clients,
// starts Kubernetes informers, waits for caches to sync, and then runs the worker loop.
// This function blocks until the context is cancelled.
func (s *Scanner) run(ctx context.Context) {
	defer utilruntime.HandleCrash()
	defer s.queue.ShutDown()

	cloudConfig, err := buildCloudConfig(s.config.CloudConfigPath)
	if err != nil {
		klog.Errorf("Failed to build cloudconfig: %v", err)
		return
	}

	tokenSource, err := generateTokenSource(ctx, cloudConfig)
	if err != nil {
		klog.Errorf("Failed to generate token source: %v", err)
		return
	}

	gcsClient, err := storage.NewClient(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		klog.Errorf("Failed to create GCS client: %v", err)
		return
	}
	s.gcsClient = gcsClient
	defer func() {
		if err := gcsClient.Close(); err != nil {
			klog.Errorf("Failed to close gcs client: %v", err)
		}
	}()

	metricClient, err := monitoring.NewMetricClient(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		klog.Errorf("Failed to create metric client: %v", err)
		return
	}
	s.metricClient = metricClient
	defer func() {
		if err := metricClient.Close(); err != nil {
			klog.Errorf("Failed to close metric client: %v", err)
		}
	}()

	computeService, err := compute.NewService(ctx, option.WithScopes(compute.ComputeReadonlyScope), option.WithTokenSource(tokenSource))
	if err != nil {
		klog.Errorf("Failed to create compute service: %v", err)
		return
	}
	s.computeService = computeService

	storageControlClient, err := control.NewStorageControlClient(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		klog.Errorf("Failed to create storage control client: %v", err)
		return
	}
	s.storageControlClient = storageControlClient
	defer func() {
		if err := storageControlClient.Close(); err != nil {
			klog.Errorf("Failed to close storage control client: %v", err)
		}
	}()

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

// runWithLeaderElection wraps the run function with leader election logic.
// It ensures that only one instance of the Scanner is active in the cluster.
// The scanner's main logic (s.run) is executed only if this instance becomes the leader.
// This function blocks until leader election fails or the context is cancelled.
func (s *Scanner) runWithLeaderElection(ctx context.Context, cancel context.CancelFunc) {
	rl, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		s.config.LeaderElectionNamespace,
		leaseName,
		nil,
		s.kubeClient.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity: s.id,
		})
	if err != nil {
		klog.Fatalf("Error creating resourcelock: %v", err)
	}

	healthCheck := leaderelection.NewLeaderHealthzAdaptor(s.config.LeaderElectionHealthCheckTimeout)
	s.mux.Handle("/healthz/leader-election", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := healthCheck.Check(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("internal server error: %v", err), http.StatusInternalServerError)
		} else {
			fmt.Fprint(w, "ok")
		}
	}))

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Name:          scannerComponentName,
		Lock:          rl,
		LeaseDuration: s.config.LeaderElectionLeaseDuration,
		RenewDeadline: s.config.LeaderElectionRenewDeadline,
		RetryPeriod:   s.config.LeaderElectionRetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				klog.Infof("Scanner controller %q started leading", s.id)
				s.run(ctx)
			},
			OnStoppedLeading: func() {
				klog.Errorf("%q is no longer the leader, shutting down", s.id)
				cancel() // Trigger graceful shutdown.
			},
		},
		WatchDog: healthCheck,
	})
}

// Start begins the Scanner process in a new goroutine.
// It will use leader election if s.config.LeaderElection is true,
// otherwise it starts the scanner directly.
func (s *Scanner) Start(ctx context.Context, cancel context.CancelFunc) {
	// Start HTTP server for metrics and leader election health checks
	// (if leader election is enabled).
	s.metricManager.InitializeHTTPHandler()

	if s.config.LeaderElection {
		s.runWithLeaderElection(ctx, cancel)
	} else {
		s.run(ctx)
	}
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

	// Process the queued item based on its key prefix.
	// The prefix determines the item type (e.g., Pod, PV) and dictates
	// the synchronization logic to be applied.
	switch {
	case strings.HasPrefix(key, podPrefix):
		syncType = "Pod"
		itemKey = strings.TrimPrefix(key, podPrefix)
		klog.V(6).Infof("Processing %q %q", syncType, itemKey)
		err = s.syncPod(ctx, itemKey)
		if errors.Is(err, errRequeuePod) {
			// errRequeuePod is not a real error, so don't count it as a failure.
			s.metricManager.RecordSyncPodMetric(nil)
		} else {
			s.metricManager.RecordSyncPodMetric(err)
		}
	case strings.HasPrefix(key, pvPrefix):
		syncType = "PV"
		itemKey = strings.TrimPrefix(key, pvPrefix)
		klog.V(6).Infof("Processing %q %q", syncType, itemKey)
		err = s.syncPV(ctx, itemKey)
		s.metricManager.RecordSyncPVMetric(err)

		// PV specific tracking removal
		if err == nil {
			s.pvMutex.Lock()
			if _, exists := s.trackedPVs[itemKey]; exists {
				klog.V(6).Infof("PV %q finished processing, removing from tracking", itemKey)
				delete(s.trackedPVs, itemKey)
			}
			s.pvMutex.Unlock()
		}
	default:
		klog.Errorf("Unknown key prefix for %q", key)
		s.queue.Forget(key)
		return true
	}

	// Handle the result of the sync operation.
	// Based on the error, an item is either removed from the queue,
	// re-queued for a later retry, or logged as a permanent failure.
	switch {
	case err == nil:
		s.queue.Forget(key)
		klog.V(6).Infof("Successfully synced %q %q", syncType, itemKey)
	case errors.Is(err, errRequeuePod):
		// Specific requeue signal for Pods waiting on PV scans or PVC binds.
		// This is not a "true" error, so it doesn't need to be logged as such.
		klog.Infof("Requeuing %q %q: %v", syncType, itemKey, err)
		s.queue.AddRateLimited(key)
	default:
		klog.Errorf("Error syncing %q %q: %v", syncType, itemKey, err)
		if status.Code(err) != codes.InvalidArgument {
			// Don't re-queue InvalidArgument  errors since these are fixed
			// until the user fixes their spec and re-deploys.
			// All other errors will be requeued with exponential back-off,
			// e.g. Kubernetes API server or internal errors.
			s.queue.AddRateLimited(key)
		}
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
		// Return internal error, since the key is formatted by the controller.
		return status.Errorf(codes.Internal, "failed to split meta namespace key %q: %v", key, err)
	}

	pod, err := s.podLister.Pods(namespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Pod %q in namespace %q has been deleted", name, namespace)
			return nil
		}
		// API server error, retry with backoff.
		return status.Errorf(codes.Internal, "failed to get Pod %q in namespace %q: %v", name, namespace, err)
	}

	klog.Infof("Syncing Pod: %q/%q", pod.Namespace, pod.Name)

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
				klog.Warningf("PVC %q/%q for Pod %q not found, will recheck Pod later", namespace, pvcName, key)
				needsRequeue = true
				continue
			}
			// API server error, retry with backoff.
			return status.Errorf(codes.Internal, "failed to get PVC %q/%q: %v", namespace, pvcName, err)
		}

		pvName := pvc.Spec.VolumeName
		if pvName == "" {
			klog.Infof("PVC %q/%q for Pod %q is not bound to a PV yet, requeue Pod to evaluate later", namespace, pvcName, key)
			needsRequeue = true
			continue
		}

		pv, err := s.pvLister.Get(pvName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.Warningf("PV %q (from PVC %q/%q) for Pod %q not found, this should not happen if PVC is bound", pvName, namespace, pvcName, key)
				// This state is unexpected, but treat as transient.
				needsRequeue = true
				continue
			}
			// API server error, retry with backoff
			return status.Errorf(codes.Internal, "failed to get PV %q: %v", pvName, err)
		}

		sc, err := s.getStorageClass(pv)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to get StorageClass for PV %q: %v", key, err)
		}

		bucketI, isScanPending, err := s.checkPVRelevance(pv, sc)
		if err != nil {
			return fmt.Errorf("error checking PV %q relevance for Pod %q: %w", pvName, key, err)
		}

		// If bucketI is nil, PV is not relevant or TTL is not expired.
		// If bucketI.isOverride is true, PV is relevant but scanning is bypassed.
		// In either of these two cases, we DO NOT enqueue the PV for scanning,
		// and we proceed to check the next volume.
		pvRelevant := bucketI != nil
		if pvRelevant && !bucketI.isOverride && isScanPending {
			klog.Infof("Pod %q uses relevant PV %q (PVC %q/%q) requiring a scan", key, pvName, namespace, pvcName)
			anyPVRelevant = true
			s.enqueuePVIfNotTracked(pv) // Enqueue the PV for scanning if not already tracked
		}
	}

	if anyPVRelevant {
		klog.Infof("Pod %q uses one or more relevant PVs, will recheck Pod later to ensure scans complete", key)
		return fmt.Errorf("%w: waiting for PV scans to complete for Pod %q", errRequeuePod, key)
	}

	if needsRequeue {
		klog.Infof("Pod %q has unbound or missing PVCs, will recheck Pod later", key)
		return fmt.Errorf("%w: waiting for PVCs to be ready for Pod %q", errRequeuePod, key)
	}

	// If no PVs are relevant (including the override case) and no other reason to requeue, remove the scheduling gate
	if err := s.removeSchedulingGate(ctx, pod); err != nil {
		return status.Errorf(codes.Internal, "failed to remove scheduling gate from Pod %s/%s: %v", pod.Namespace, pod.Name, err)
	}
	return nil
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
		klog.V(6).Infof("Scheduling gate %q not found on Pod %q/%q", schedulingGateName, pod.Namespace, pod.Name)
		return nil // Nothing to do
	}

	klog.Infof("Removing scheduling gate %q from Pod %q/%q", schedulingGateName, pod.Namespace, pod.Name)

	patchData := map[string]any{
		"spec": map[string]any{
			"schedulingGates": newGates,
		},
	}
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to marshal patch data for Pod %q/%q: %w", pod.Namespace, pod.Name, err)
	}

	_, err = s.kubeClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Warningf("Failed to patch Pod %q/%q because it was not found", pod.Namespace, pod.Name)
			return nil
		}
		return fmt.Errorf("failed to patch Pod %q/%q to remove scheduling gate: %w", pod.Namespace, pod.Name, err)
	}

	klog.Infof("Successfully removed scheduling gate %q from Pod %q/%q", schedulingGateName, pod.Namespace, pod.Name)
	return nil
}

func (s *Scanner) getDurationAttribute(pv *v1.PersistentVolume, attributeKey string, defaultDuration time.Duration) (*time.Duration, error) {
	if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
		if durationStr, ok := pv.Spec.CSI.VolumeAttributes[attributeKey]; ok {
			parsedDuration, err := time.ParseDuration(durationStr)
			if err != nil {
				return nil, fmt.Errorf("invalid duration format for %q: %q, error: %w", attributeKey, durationStr, err)
			}
			if parsedDuration <= 0 {
				return nil, fmt.Errorf("non-positive duration for %q: %q", attributeKey, durationStr)
			}
			klog.Infof("PV %q: Using %q key from VolumeAttributes: %q", pv.Name, attributeKey, parsedDuration)
			return &parsedDuration, nil
		}
	}
	klog.V(6).Infof("PV %q: No %q key in VolumeAttributes. Using default %q", pv.Name, attributeKey, defaultDuration)
	return &defaultDuration, nil
}

// bypassScanForOverride handles a PV with the override mode set.
// No actual scan is performed. It uses the pre-validated bucketInfo to patch the PV
// with the user-provided data and record a corresponding event.
func (s *Scanner) bypassScanForOverride(ctx context.Context, pv *v1.PersistentVolume, key string, bucketI *bucketInfo) error {
	klog.Infof("PV %q is set to 'override' mode. Bypassing bucket scan and applying user-provided annotations.", key)

	// The status annotation is already "override", but we patch it here along with
	// the timestamp to mark the operation as complete and update the in-memory map.
	s.eventRecorder.Eventf(pv, v1.EventTypeNormal, reasonScanOperationSucceeded, "Override mode detected for PV %q. Bypassing scan and using user-provided values: %d objects, %d bytes: %t", pv.Name, bucketI.numObjects, bucketI.totalSizeBytes)
	if patchErr := s.updatePVScanResult(ctx, pv, bucketI, profilesutil.ScanOverride); patchErr != nil {
		return patchErr
	}
	return nil // Remove from queue since this is considered a complete and successful "scan" (bypass).
}

// syncPV is the core reconciliation function for a PersistentVolume.
// It checks if the PV is relevant and if a scan is required, performs the bucket scan, and updates the PV annotations.
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
		// Internal API error, retry with exponential back-off.
		return status.Errorf(codes.Internal, "failed to get PV %q: %v", key, err)
	}

	sc, err := s.getStorageClass(pv)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get StorageClass for PV %q: %v", key, err)
	}
	// Skip PVs that are not relevant, e.g. PVs that don't use the gcsfuse profiles feature or
	// that have been scanned too recently.
	bucketI, isScanPending, err := s.checkPVRelevance(pv, sc)
	if err != nil {
		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationStartError, "Relevance check failed: %v", err)
		return fmt.Errorf("relevance check failed for PV %q: %w", pv.Name, err)
	}
	pvRelevant := bucketI != nil
	if !pvRelevant {
		klog.V(6).Infof("PV %q is not relevant, skipping sync", key)
		// Remove irrelevant PV from queue.
		return nil
	}

	klog.Infof("PV %q is relevant, bucket: %q, dir: %q, onlyDirSpecified: %t", key, bucketI.name, bucketI.dir, bucketI.onlyDirSpecified)

	// ----- At this stage, the PV has been considered eligible for a scan. -----

	if bucketI.isOverride {
		// Bypass the scanner if the override mode is set.
		err = s.bypassScanForOverride(ctx, pv, key, bucketI)
		if err != nil {
			return err
		}
	}

	if isScanPending {
		// Get the bucket scan timeout limit. This may have been overriden by the customer.
		currentScanTimeout, err := s.getDurationAttribute(pv, volumeAttributeScanTimeoutKey, defaultScanTimeoutDuration)
		if err != nil {
			s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationStartError, "Bucket scan timeout configuration error: %v", err)
			return status.Errorf(codes.InvalidArgument, "bucket scan timeout configuration error: %v", err)
		}

		s.eventRecorder.Eventf(pv, v1.EventTypeNormal, reasonScanOperationStartSucceeded, "Started bucket scan for PV %q, bucket %q, directory %q, with timeout %s", pv.Name, bucketI.name, bucketI.dir, currentScanTimeout)
		klog.Infof("Bucket scan operation starting for PV %q, bucket %q, dir %q, timeout %q", pv.Name, bucketI.name, bucketI.dir, currentScanTimeout)

		err = s.scanBucket(ctx, bucketI, *currentScanTimeout, pv)

		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				// A bucket scan timeout is benign and expected if the bucket has a large number of objects.
				// We send a warning only to inform the customer about the timeout.
				duration := timeNow().Sub(syncStartTime)
				s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationTimedOut, "Bucket scan timed out after %s for bucket %q, directory %q (%v). Updating with partial results: %d objects, %d bytes", currentScanTimeout, bucketI.name, bucketI.dir, duration.Round(time.Second), bucketI.numObjects, bucketI.totalSizeBytes)
				if patchErr := s.updatePVScanResult(ctx, pv, bucketI, scanTimeout); patchErr != nil {
					return fmt.Errorf("failed to patch PV %q after timeout, err: %w", pv.Name, patchErr)
				}
				// Remove since we still consider this a successful scan.
				return nil
			}
			// For any other error, re-queue.
			klog.Errorf("Error scanning bucket for PV %q: %v", pv.Name, err)
			s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationFailed, "Bucket scan failed for bucket %q, directory %q: %v", bucketI.name, bucketI.dir, err)
			return fmt.Errorf("error scanning bucket for PV %q: %w", pv.Name, err)
		}

		// The scan has been successful and complete results have been used.
		duration := timeNow().Sub(syncStartTime)
		s.eventRecorder.Eventf(pv, v1.EventTypeNormal, reasonScanOperationSucceeded, "Bucket scan completed successfully for bucket %q, directory %q (%v): %d objects, %d bytes", bucketI.name, bucketI.dir, duration.Round(time.Second), bucketI.numObjects, bucketI.totalSizeBytes)
		if patchErr := s.updatePVScanResult(ctx, pv, bucketI, scanCompleted); patchErr != nil {
			return fmt.Errorf("failed to patch PV %q results, err: %w", pv.Name, patchErr)
		}
	}

	// Check if the pv uses a anywhere cache enabled storage class.
	shouldEnableAnywhereCache := false
	enableAnywhereCache, ok := sc.Parameters[volumeAttributeEnableAnywhereCache]
	if !ok {
		return nil
	}
	shouldEnableAnywhereCache, err = strconv.ParseBool(enableAnywhereCache)
	if err != nil {
		klog.Errorf("Failed to enable Anywhere Cache requests for PV %q: %v", pv.Spec.StorageClassName, err)
		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationAnywhereCacheCreateError, "Failed to enable Anywhere Cache for PV %q: %v", pv.Spec.StorageClassName, err)
		return status.Errorf(codes.InvalidArgument, "failed to parse enableAnywhereCache for PV %q: %v", pv.Spec.StorageClassName, err)
	}

	if !shouldEnableAnywhereCache {
		return nil
	}

	err = s.createAnywhereCache(ctx, pv)
	if err != nil {
		klog.Errorf("Failed to create Anywhere Cache requests for PV %q: %v", pv.Name, err)
		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationAnywhereCacheCreateError, "Failed to create Anywhere Cache requests for PV %q: %v", pv.Name, err)
		return fmt.Errorf("failed to create Anywhere Cache request for PV %q: %w", pv.Name, err)
	}
	s.eventRecorder.Eventf(pv, v1.EventTypeNormal, reasonScanOperationAnywhereCacheCreateSucceeded, "Submitted anywhere cache requests for for bucket: %s", bucketI.name)
	return nil
}

// createAnywhereCache does prework and makes request to create anywherecache.
// Prework: get zones, create clients, and sets up the request for anywhere cache.
// Create request: sends a create anywhere cache for each zone of the cluster (api is idempotent).
// If any retryable error occurs we requeue the pv to try again later.
func (s *Scanner) createAnywhereCache(ctx context.Context, pv *v1.PersistentVolume) error {
	anywhereCacheTTL, err := getAnywhereCacheTTLFromPV(pv)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to get anywhere cache ttl for PV %q: %v", pv.Name, err)
	}
	anywhereCacheAdmissionPolicy, err := getAnywhereCacheAdmissionPolicyFromPV(pv)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to get anywhere cache admission policy for PV %q: %v", pv.Name, err)
	}

	zones, err := utilGetZonesForClusterLocation(ctx, s.config.ProjectNumber, s.computeService, s.config.ClusterLocation)
	if err != nil {
		return fmt.Errorf("failed to get zones for region: %w", err)
	}
	if s.storageControlClient == nil {
		return status.Errorf(codes.Internal, "storage control client should not be nil")
	}

	var errs []error
	for _, zone := range zones {
		req := &controlpb.CreateAnywhereCacheRequest{
			// projects/{project}/buckets/{bucket} is the required format for the api
			Parent: fmt.Sprintf(`projects/_/buckets/%s`, util.ParseVolumeID(pv.Spec.CSI.VolumeHandle)),
			AnywhereCache: &controlpb.AnywhereCache{
				Ttl:             anywhereCacheTTL,
				Zone:            zone,
				AdmissionPolicy: anywhereCacheAdmissionPolicy,
			},
		}

		_, err = s.storageControlClient.CreateAnywhereCache(ctx, req)
		if err != nil {
			if status.Code(err) == codes.AlreadyExists {
				// AC already exists for this bucket/zone no further action needed.
				continue
			}
			errs = append(errs, fmt.Errorf("%s:[%w]", zone, err))
		}
	}
	if len(errs) <= 0 {
		return nil
	}
	return fmt.Errorf("errors occurred while creating anywhere caches: %w", errors.Join(errs...))
}

// getAnywhereCacheTTLFromPV returns the value of 'anywhereCacheTTL', defaults to 1h for no value or error if invalid value is present.
func getAnywhereCacheTTLFromPV(pv *v1.PersistentVolume) (*durationpb.Duration, error) {
	// TODO(fuechr) Check StorageClass for ttl as fallback
	ttl, ok := pv.Spec.CSI.VolumeAttributes[volumeAttributeAnywhereCacheTTL]

	if !ok {
		klog.Infof("no ttl volume attribute provided, defaulting the anywhere cache ttl to 1h for pv: %s", pv.Name)
		return hourDuration, nil
	}

	ttlAsDuration, err := time.ParseDuration(ttl)
	if err != nil {
		return nil, err
	}

	return durationpb.New(ttlAsDuration), nil
}

// getAnywhereCacheAdmissionPolicyFromPV returns the value of 'anywhereCacheAdmissionPolicy', defaulting to 'admit-on-first-miss' if no value is provided.
func getAnywhereCacheAdmissionPolicyFromPV(pv *v1.PersistentVolume) (string, error) {
	// TODO(fuechr) Check StorageClass for admission policy as fallback
	admissionPolicy, ok := pv.Spec.CSI.VolumeAttributes[volumeAttributeAnywhereCacheAdmissionPolicy]
	if !ok {
		klog.Infof("no admission policy volume attribute provided, defaulting the anywhere cache admission policy to %q for pv: %s", admitOnFirstMiss, pv.Name)
		return admitOnFirstMiss, nil
	}

	switch admissionPolicy {
	case admitOnFirstMiss, admitOnSecondMiss:
		return admissionPolicy, nil
	default:
		return "", fmt.Errorf("invalid anywhere cache admission policy provided provided: %s, valid values are %q or %q", admissionPolicy, admitOnFirstMiss, admitOnSecondMiss)
	}
}

func (s *Scanner) scanBucket(ctx context.Context, bucketI *bucketInfo, scanTimeout time.Duration, pv *v1.PersistentVolume) error {
	return s.scanBucketImpl(s, ctx, bucketI, scanTimeout, pv)
}

func defaultBucketAttrs(ctx context.Context, gcsClient *storage.Client, bucketName string) (*storage.BucketAttrs, error) {
	attrs, err := gcsClient.Bucket(bucketName).Attrs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket attributes for %q: %w", bucketName, err)
	}
	return attrs, nil
}

// defaultScanBucket performs a bucket scan.
// It collects the number of objects, total size.
// This function respects the provided context and the scanTimeout.
// It returns partial results if the timeout is reached (context.DeadlineExceeded).
//
// If the `only-dir` mount option is specified, the bucket will be scanned using the
// GCS Dataflux client library for the specified directory. Otherwise, Google Cloud Metrics
// will be used and fallback to the GCS Dataflux client library in the case of any errors or
// unavailable metrics.
//
// Optionally, GCS Dataflux client scanning on the entire bucket can be forced by specifying `only-dir=/`
func defaultScanBucket(s *Scanner, ctx context.Context, bucketI *bucketInfo, scanTimeout time.Duration, pv *v1.PersistentVolume) error {
	// Get bucket attributes to determine project number.
	// Use the parent context for this, as it's a quick metadata call.
	bucketAttrs, err := bucketAttrs(ctx, s.gcsClient, bucketI.name)
	if err != nil {
		return fmt.Errorf("failed to get bucket attributes for %q: %w", bucketI.name, err)
	}
	bucketI.projectNumber = fmt.Sprint(bucketAttrs.ProjectNumber)

	if bucketI.onlyDirSpecified {
		klog.Infof("'only-dir' is set for bucket %q, dir %q. Scanning with Dataflux.", bucketI.name, bucketI.dir)
		dfErr := scanBucketWithDataflux(ctx, s.gcsClient, bucketI, scanTimeout, s.datafluxConfig)
		if dfErr != nil {
			klog.Errorf("Dataflux scan failed for bucket %q, dir %q: %v", bucketI.name, bucketI.dir, dfErr)
			// No fallback, as metrics are not applicable for a specific directory.
		}
		return dfErr
	} else {
		klog.Infof("onlyDirSpecified is false for bucket %q. Attempting scan with GCS Bucket Metrics first.", bucketI.name)
		mErr := scanBucketWithMetrics(ctx, s.metricClient, bucketI)
		if mErr == nil {
			klog.Infof("Successfully scanned bucket %q using GCS Bucket Metrics: %d objects, %d bytes", bucketI.name, bucketI.numObjects, bucketI.totalSizeBytes)
			return nil
		}

		s.eventRecorder.Eventf(pv, v1.EventTypeWarning, reasonScanOperationWarning, "Unable to scan bucket %q using GCS Bucket Metrics, falling back to Dataflux: %v", bucketI.name, mErr)
		// Fallback to Dataflux for the whole bucket
		dfErr := scanBucketWithDataflux(ctx, s.gcsClient, bucketI, scanTimeout, s.datafluxConfig)
		if dfErr != nil {
			klog.Errorf("Dataflux scan (fallback) failed for bucket %q: %v", bucketI.name, dfErr)
			return dfErr
		}
		klog.Infof("Successfully scanned bucket %q using Dataflux fallback.", bucketI.name)
		return nil
	}
}

// defaultScanBucketWithMetrics fetches bucket size and object count from Cloud Monitoring.
func defaultScanBucketWithMetrics(ctx context.Context, metricClient *monitoring.MetricClient, bucketI *bucketInfo) error {
	klog.V(6).Infof("Fetching metrics for bucket %q in project %q", bucketI.name, bucketI.projectNumber)

	objectCount, err := fetchMetricValue(ctx, metricClient, bucketI.projectNumber, bucketI, objectCountMetric)
	if err != nil {
		return fmt.Errorf("failed to fetch object count metric: %w", err)
	}

	totalBytes, err := fetchMetricValue(ctx, metricClient, bucketI.projectNumber, bucketI, totalBytesMetric)
	if err != nil {
		return fmt.Errorf("failed to fetch total bytes metric: %w", err)
	}

	bucketI.numObjects = objectCount
	bucketI.totalSizeBytes = totalBytes
	return nil
}

// fetchMetricValue retrieves the latest value for a given metric type from Cloud Monitoring.
func fetchMetricValue(ctx context.Context, metricClient *monitoring.MetricClient, projectNumber string, bucketI *bucketInfo, metricType string) (int64, error) {
	filter := fmt.Sprintf(`metric.type="%s" AND resource.type="gcs_bucket" AND resource.labels.bucket_name="%s"`, metricType, bucketI.name)

	now := time.Now()
	// Metrics are published daily, look back up to 48 hours to be safe.
	startTime := now.Add(-48 * time.Hour)
	interval := &monitoringpb.TimeInterval{
		EndTime:   timestamppb.New(now),
		StartTime: timestamppb.New(startTime),
	}

	req := &monitoringpb.ListTimeSeriesRequest{
		Name:     fmt.Sprintf("projects/%s", projectNumber),
		Filter:   filter,
		Interval: interval,
		Aggregation: &monitoringpb.Aggregation{
			AlignmentPeriod:  durationpb.New(24 * time.Hour), // Align over a day
			PerSeriesAligner: monitoringpb.Aggregation_ALIGN_NEXT_OLDER,
		},
	}

	it := metricClient.ListTimeSeries(ctx, req)
	if it == nil {
		return 0, status.Errorf(codes.Internal, "ListTimeSeries returned nil iterator for metric %q", metricType)
	}

	resp, err := it.Next()
	if err == iterator.Done {
		return 0, status.Errorf(codes.NotFound, "no time series data found for metric %q in bucket %q", metricType, bucketI.name)
	}
	if err != nil {
		return 0, status.Errorf(codes.Internal, "error iterating over time series for metric %q: %v", metricType, err)
	}

	if len(resp.Points) == 0 {
		return 0, status.Errorf(codes.NotFound, "no points found in time series for metric %q in bucket %q", metricType, bucketI.name)
	}

	latestPoint := resp.Points[0]
	if resp.ValueType == metric.MetricDescriptor_INT64 {
		return latestPoint.Value.GetInt64Value(), nil
	}
	if resp.ValueType == metric.MetricDescriptor_DOUBLE {
		return int64(latestPoint.Value.GetDoubleValue()), nil
	}

	return 0, status.Errorf(codes.Internal, "unsupported value type for metric %q: %v", metricType, resp.ValueType)
}

// defaultScanBucketWithDataflux performs a bucket scan using the GCS Dataflux library.
func defaultScanBucketWithDataflux(ctx context.Context, gcsClient *storage.Client, bucketI *bucketInfo, scanTimeout time.Duration, datafluxConfig *DatafluxConfig) error {
	klog.Infof("Starting Dataflux scan for bucket: %q, dir: %q, timeout: %q", bucketI.name, bucketI.dir, scanTimeout)
	scanCtx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	if datafluxConfig == nil {
		return status.Errorf(codes.Internal, "datafluxConfig is nil")
	}

	dfInput := &dataflux.ListerInput{
		BucketName:           bucketI.name,
		Parallelism:          datafluxConfig.Parallelism,
		BatchSize:            datafluxConfig.BatchSize,
		Query:                storage.Query{},
		SkipDirectoryObjects: datafluxConfig.SkipDirectoryObjects,
	}

	// Optimize resource consumption by filtering only relevant fields.
	dfInput.Query.SetAttrSelection([]string{"Name", "Size"})

	// Only scan for objects under this directory, if defined.
	if bucketI.dir != "" && bucketI.dir != "/" {
		// Ensure that the directory name ends with "/" to avoid picking up
		// files with prefixes of other directories, since GCS "nested"
		// objects are just file names with "/" in the names.
		// TODO(urielguzman): Add E2E test for this scenario.
		dfInput.Query.Prefix = strings.Trim(bucketI.dir, "/") + "/"
	}

	klog.Infof("Dataflux ListerInput created: %+v", dfInput)
	df := dataflux.NewLister(gcsClient, dfInput)
	defer df.Close()

	var numObjects int64
	var totalSizeBytes int64

	accumulate := func(objects []*storage.ObjectAttrs) {
		numObjects += int64(len(objects))
		for _, obj := range objects {
			totalSizeBytes += obj.Size
		}
	}

	startTime := timeNow()
	for {
		objects, err := df.NextBatch(scanCtx)
		switch {
		case errors.Is(err, iterator.Done):
			// The scan is completed. Accumulate the last batch.
			accumulate(objects)
			klog.Infof("Dataflux listing complete for bucket %q, dir %q. Found %d objects, total size %d bytes in %q", bucketI.name, bucketI.dir, numObjects, totalSizeBytes, time.Since(startTime).Round(time.Millisecond))
			bucketI.numObjects = numObjects
			bucketI.totalSizeBytes = totalSizeBytes
			return nil
		case errors.Is(err, context.DeadlineExceeded):
			// The scan has timed out. Accumulate the last batch.
			accumulate(objects)
			klog.Warningf("Scan for bucket %q, dir %q timed out after %q: %v. Returning partial results: %d objects, %d bytes", bucketI.name, bucketI.dir, time.Since(startTime).Round(time.Millisecond), err, numObjects, totalSizeBytes)
			bucketI.numObjects = numObjects
			bucketI.totalSizeBytes = totalSizeBytes
			return context.DeadlineExceeded
		case err == nil:
			// The scan is not yet finished. Continue accumulating metadata.
			accumulate(objects)
			klog.V(6).Infof("Bucket %q, dir %q: Scanned %d objects, total size %d bytes so far", bucketI.name, bucketI.dir, numObjects, totalSizeBytes)
		default:
			return status.Errorf(codes.Internal, "error getting next batch from dataflux for bucket %q, dir %q: %v", bucketI.name, bucketI.dir, err)
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
		return status.Errorf(codes.Internal, "failed to marshal annotation patch data for PV %q: %v", pvName, err)
	}
	klog.V(6).Infof("Patching PV %q annotations with: %q", pvName, string(patchBytes))
	_, err = s.kubeClient.CoreV1().PersistentVolumes().Patch(ctx, pvName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Warningf("Failed to patch PV %q because it was not found", pvName)
			return nil
		}
		return status.Errorf(codes.Internal, "failed to patch PV %q annotations: %v", pvName, err)
	}
	klog.V(6).Infof("Successfully patched annotations for PV %q", pvName)
	return nil
}

// updatePVScanResult updates the PV annotations with the results of a bucket scan.
// It sets the status, number of objects, total size, and last updated time.
// It also updates the in-memory lastSuccessfulScan map.
func (s *Scanner) updatePVScanResult(ctx context.Context, pv *v1.PersistentVolume, bucketI *bucketInfo, status string) error {
	currentTime := timeNow()
	annotationsToUpdate := map[string]*string{
		profilesutil.AnnotationStatus:          stringPtr(status),
		profilesutil.AnnotationNumObjects:      int64Ptr(bucketI.numObjects),
		profilesutil.AnnotationTotalSize:       int64Ptr(bucketI.totalSizeBytes),
		profilesutil.AnnotationLastUpdatedTime: stringPtr(currentTime.UTC().Format(time.RFC3339)),
	}
	klog.Infof("Updating PV %q with scan result: %+v, status: %q", pv.Name, bucketI, status)
	err := s.patchPVAnnotations(ctx, pv.Name, annotationsToUpdate)
	if err != nil {
		klog.Errorf("Failed to update annotations on PV %q with status %q: %v", pv.Name, status, err)
		return err
	}
	klog.Infof("Successfully updated annotations on PV %q with status %q", pv.Name, status)

	// Update in-memory map only on terminal state updates.
	if status == scanCompleted || status == scanTimeout || status == profilesutil.ScanOverride {
		s.scanMutex.Lock()
		s.lastSuccessfulScan[pv.Name] = currentTime
		s.scanMutex.Unlock()
		klog.V(6).Infof("Updated lastSuccessfulScan map for PV %q to %q", pv.Name, currentTime)
	}
	return nil
}

// checkPVRelevance determines if a PersistentVolume is relevant for gcsfuse profiles and whether there is a scan pending.
// returns (bucketInfo, isScanPending, error)
// A PV is relevant if it uses the gcsfuse CSI driver and its StorageClass
// has a workloadType parameter set to serving, training, or checkpointing.
// The PV is pending a scan if the current time - last scan time >= scan TTL or it hasn't been scanned yet, and the status is not "override"..
// This function returns a bucketInfo with the bucket name and the directory if
// relevant, otherwise, it will return nil and any error.
func (s *Scanner) checkPVRelevance(pv *v1.PersistentVolume, sc *storagev1.StorageClass) (*bucketInfo, bool, error) {
	if pv == nil {
		return nil, false, nil
	}
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != csiDriverName || pv.Spec.CSI.VolumeHandle == "" {
		return nil, false, nil
	}

	scParams := sc.Parameters
	if scParams == nil {
		return nil, false, nil
	}
	workloadType, ok := scParams[workloadTypeKey]
	if !ok {
		klog.V(6).Infof("Workload type parameter key %q was not found in StorageClass %q for PV %q", workloadTypeKey, sc.Name, pv.Name)
		return nil, false, nil
	}

	// ---- At this stage, there is clearly a customer intent to use the scanner feature, so we start logging warnings -----

	if err := validateWorkloadType(workloadType); err != nil {
		return nil, false, status.Errorf(codes.InvalidArgument, "failed to validate workload type: %v", err)
	}

	bucketName := util.ParseVolumeID(pv.Spec.CSI.VolumeHandle)
	var dir string
	var onlyDirSpecified bool
	for _, mountOption := range pv.Spec.MountOptions {
		if val, ok := onlyDirValue(mountOption); ok {
			dir = val
			onlyDirSpecified = true
			break
		}
	}

	bucketI := &bucketInfo{
		name:             bucketName,
		dir:              dir,
		onlyDirSpecified: onlyDirSpecified,
	}

	// Handle the override annotation, if set.
	if bucketStatus, ok := pv.Annotations[profilesutil.AnnotationStatus]; ok && bucketStatus == profilesutil.ScanOverride {
		// Enforce required annotations for override mode and validate formats.
		numObjects, totalSizeBytes, err := profilesutil.ParseOverrideStatus(pv)
		if err != nil {
			return nil, false, fmt.Errorf("failed to validate arguments for PV %q with override mode: %v", pv.Name, err)
		}
		overrideInfo := &bucketInfo{
			numObjects:     numObjects,
			totalSizeBytes: totalSizeBytes,
		}

		klog.Infof("PV %q: Override mode detected. Bypassing scan.", pv.Name)
		// The PV is considered relevant but this directs syncPV and syncPod to the bypass logic.
		overrideInfo.name = bucketI.name
		overrideInfo.dir = bucketI.dir
		overrideInfo.onlyDirSpecified = bucketI.onlyDirSpecified
		overrideInfo.isOverride = true
		return overrideInfo, false, nil
	}
	lastScanTime, found, err := s.calculateLastScanTime(pv)
	if err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to calculate last scan time: %v", err)
	}

	if found {
		elapsed := timeNow().Sub(lastScanTime)

		currentScanTTL, err := s.getDurationAttribute(pv, volumeAttributeScanTTLKey, defaultScanTTLDuration)
		if err != nil {
			return nil, false, status.Errorf(codes.InvalidArgument, "bucket scan TTL configuration error: %v", err)
		}

		if elapsed < *currentScanTTL {
			klog.Infof("PV %q: Skipping scan, only %q elapsed since last scan, which is less than TTL %q", pv.Name, elapsed.Round(time.Second), currentScanTTL)
			return bucketI, false, nil
		}
		klog.V(6).Infof("PV %q: Proceeding with scan, %q elapsed since last scan, TTL is %q", pv.Name, elapsed.Round(time.Second), currentScanTTL)
		return bucketI, true, nil
	}

	// If the PV is relevant but doesn't yet have a last scan time, it hasn't been scanned yet.
	// It is unexpected that the PV has scanner annotations unless the override mode is enabled,
	// which has already been verified in the mutating webhook and verified above. This should be flagged to the user to avoid
	// unexpected behavior.
	if annotationsUsed := profilesutil.PvAnnotationIntersection(pv, []string{
		profilesutil.AnnotationStatus,
		profilesutil.AnnotationNumObjects,
		profilesutil.AnnotationTotalSize,
	}); len(annotationsUsed) > 0 {
		return nil, false, status.Errorf(codes.InvalidArgument, "scanner annotations for PV %q found in non-override mode: %+v", pv.Name, annotationsUsed)
	}

	klog.V(6).Infof("PV %q: No last scan time found in memory or annotations. Proceeding with scan", pv.Name)
	return bucketI, true, nil
}

// calculateLastScanTime returns the last successful scan of a PV. If it doesn't exist in
// memory (e.g. first time scanning the PV), it checks the PV annotatins. The function
// returns a boolean to indicate if the value was found, otherwise, it returns an error.
func (s *Scanner) calculateLastScanTime(pv *v1.PersistentVolume) (time.Time, bool, error) {
	// Check if the last scan time appears in memory first.
	s.scanMutex.RLock()
	lastScanTimeFromMemory, ok := s.lastSuccessfulScan[pv.Name]
	s.scanMutex.RUnlock()
	if ok {
		klog.V(6).Infof("PV %q: Found last scan time in memory: %q", pv.Name, lastScanTimeFromMemory)
		return lastScanTimeFromMemory, true, nil
	}

	// Check if the last scan time appears in the PV annotations.
	lastScanTimeFromAnnotation, ok := pv.Annotations[profilesutil.AnnotationLastUpdatedTime]
	if !ok {
		return time.Time{}, false, nil
	}
	parsedTime, err := time.Parse(time.RFC3339, lastScanTimeFromAnnotation)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("PV %q: Failed to parse annotation %q value %q: %v", pv.Name, profilesutil.AnnotationLastUpdatedTime, lastScanTimeFromAnnotation, err)
	}
	klog.V(6).Infof("PV %q: Found last scan time in annotation: %q", pv.Name, lastScanTimeFromAnnotation)
	return parsedTime, true, nil
}

// onlyDirValue parses a mount option string to extract the value of "only-dir".
// It returns the directory value and true if the prefix is found, otherwise empty string and false.
// The directory value is trimmed to exclude leading or trailing '/'.
func onlyDirValue(s string) (string, bool) {
	prefix := "only-dir"
	for _, delim := range []string{"=", ":"} {
		if strings.HasPrefix(s, prefix+delim) {
			val := strings.TrimPrefix(s, prefix+delim)
			return strings.Trim(val, "/"), true
		}
	}
	return "", false
}

// enqueuePV enqueues a PersistentVolume.
func (s *Scanner) enqueuePV(pv *v1.PersistentVolume) {
	key, err := cache.MetaNamespaceKeyFunc(pv)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pv, err))
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
		klog.V(6).Infof("PV %q: Not tracked, enqueuing for scan", pv.Name)
		s.enqueuePV(pv)
	} else {
		klog.V(6).Infof("PV %q: Already tracked, skipping enqueue", pv.Name)
	}
}

// addPV is the add event handler for PersistentVolumes.
func (s *Scanner) addPV(obj any) {
	pv, ok := obj.(*v1.PersistentVolume)
	if !ok {
		klog.Errorf("AddFunc PV: Expected PersistentVolume but got %T", obj)
		return
	}
	klog.V(6).Infof("AddFunc PV: PV ADDED: %q", pv.Name)
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
		klog.V(6).Infof("DeleteFunc PV: PV TOMBSTONE: %q", pv.Name)
	} else {
		klog.V(6).Infof("DeleteFunc PV: PV DELETED: %q", pv.Name)
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
		klog.V(6).Infof("Removed PV %q from lastSuccessfulScan map", key)

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
	klog.V(6).Infof("AddFunc Pod: Pod ADDED: %q/%q", pod.Namespace, pod.Name)
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
		klog.V(6).Infof("DeleteFunc Pod: Pod TOMBSTONE: %q/%q", pod.Namespace, pod.Name)
	} else {
		klog.V(6).Infof("DeleteFunc Pod: Pod DELETED: %q/%q", pod.Namespace, pod.Name)
	}

	key, err := cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.Errorf("DeleteFunc Pod: Error mapping key from object: %v", err)
		return
	}
	klog.V(6).Infof("DeleteFunc Pod: Forgetting Pod %q from queue", key)
	s.queue.Forget(podPrefix + key)
}

// enqueuePod enqueues a Pod.
func (s *Scanner) enqueuePod(pod *v1.Pod) {
	key, err := cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pod, err))
		return
	}
	klog.V(6).Infof("Enqueuing Pod %q", key)
	s.queue.Add(podPrefix + key)
}

func (s *Scanner) getStorageClass(pv *v1.PersistentVolume) (*storagev1.StorageClass, error) {
	if pv == nil || pv.Spec.CSI == nil || pv.Spec.CSI.Driver != csiDriverName || pv.Spec.CSI.VolumeHandle == "" {
		return nil, nil
	}

	scName := pv.Spec.StorageClassName
	if scName == "" {
		return nil, nil
	}
	sc, err := s.scLister.Get(scName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get StorageClass %q: %w", scName, err)
		}
		// If the customer specifies a "dummy" StorageClass, this must be handled gracefully. Example:
		// https://cloud.google.com/kubernetes-engine/docs/how-to/cloud-storage-fuse-csi-driver-pv#create-a-persistentvolume
		// TODO(urielguzman): Add a notificaiton map so that the PV is re-processed when the StorageClass exists.
		// We shouldn't re-process the PV indefinetely because the "dummy" StorageClass may never exist.
		// Example: https://github.com/kubernetes-csi/lib-volume-populator/blob/master/populator-machinery/controller.go#L685
		klog.Warningf("StorageClass %q not found for PV %q", scName, pv.Name)
		return nil, nil
	}
	return sc, nil
}
