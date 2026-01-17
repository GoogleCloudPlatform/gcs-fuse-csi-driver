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
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/storage"
	control "cloud.google.com/go/storage/control/apiv2"
	controlpb "cloud.google.com/go/storage/control/apiv2/controlpb"
	"github.com/google/go-cmp/cmp"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	putil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	compute "google.golang.org/api/compute/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testSCName            = "test-storage-class"
	testPVName            = "test-pv"
	testPVCName           = "test-pvc"
	testPodName           = "test-pod"
	testNamespace         = "default"
	testBucketName        = "test-bucket"
	testDirName           = "test-dir"
	oldAnnotationKey      = "old"
	newAnnotationKey      = "new"
	newAnnotationVal      = "newval"
	onlyDirMountOptPrefix = "only-dir"
	testProjectNumber     = "123456"
)

var (
	validSCParams = map[string]string{}
	validSC       = createStorageClass(testSCName, validSCParams)
	podLabels     = map[string]string{profileManagedLabelKey: profileManagedLabelValue}
	scanResult    = &bucketInfo{
		name:           testBucketName,
		numObjects:     1234,
		totalSizeBytes: 567890,
	}
	origbucketAttrs            = bucketAttrs
	origScanBucketWithMetrics  = scanBucketWithMetrics
	origScanBucketWithDataflux = scanBucketWithDataflux
)

type fakeScanFunc struct {
	called bool
	// Forced outputs.
	err            error
	numObjects     int64
	totalSizeBytes int64
}

func (f *fakeScanFunc) scan(bucketI *bucketInfo) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	bucketI.numObjects = f.numObjects
	bucketI.totalSizeBytes = f.totalSizeBytes
	return nil
}

func (f *fakeScanFunc) scanBucketWithMetrics(ctx context.Context, metricClient *monitoring.MetricClient, bucketI *bucketInfo) error {
	return f.scan(bucketI)
}

func (f *fakeScanFunc) scanBucketWithDataflux(ctx context.Context, gcsClient *storage.Client, bucketI *bucketInfo, scanTimeout time.Duration, datafluxConfig *DatafluxConfig) error {
	return f.scan(bucketI)
}

type fakebucketAttrsFunc struct {
	attrs *storage.BucketAttrs
	err   error
}

func (f *fakebucketAttrsFunc) getBucketAttributes(ctx context.Context, gcsClient *storage.Client, bucketName string) (*storage.BucketAttrs, error) {
	return f.attrs, f.err
}

// mockTime allows controlling the time in tests.
// This is useful for testing time-dependent logic, such as TTLs.
type mockTime struct {
	currentTime time.Time
}

// Now returns the current mocked time.
func (m *mockTime) Now() time.Time {
	return m.currentTime
}

// fakeScanBucketImplFunc provides a mock implementation of the scanBucketImpl function.
// It allows simulating different scan outcomes, including errors and timeouts.
type fakeScanBucketImplFunc struct {
	bucketI   *bucketInfo
	err       error
	wasCalled bool
}

// Scan is the mock implementation of the scanBucketImpl function.
// It checks for context cancellation (like timeouts) before returning the predefined info and error.
func (f *fakeScanBucketImplFunc) Scan(scanner *Scanner, ctx context.Context, bucketI *bucketInfo, scanTimeout time.Duration, pv *v1.PersistentVolume, sc *storagev1.StorageClass) error {
	f.wasCalled = true
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return ctx.Err()
	default:
		if f.err == nil && f.bucketI != nil {
			bucketI.numObjects = f.bucketI.numObjects
			bucketI.totalSizeBytes = f.bucketI.totalSizeBytes
		}
		return f.err
	}
}

// testFixture holds the necessary components for testing the Scanner.
// It encapsulates the fake Kubernetes client, listers, recorders, and mock functions.
type testFixture struct {
	scanner      *Scanner
	kubeClient   *fake.Clientset
	pvLister     corelisters.PersistentVolumeLister
	pvcLister    corelisters.PersistentVolumeClaimLister
	scLister     storagelisters.StorageClassLister
	podLister    corelisters.PodLister
	recorder     *record.FakeRecorder
	mockTimeImpl *mockTime
	scanBucketFn *fakeScanBucketImplFunc
	stopCh       chan struct{}
}

// newTestFixture initializes a new testFixture.
func newTestFixture(t *testing.T, initialObjects ...runtime.Object) *testFixture {
	t.Helper()

	kubeClient := fake.NewSimpleClientset(initialObjects...)

	// Factory for PV, PVC and SC informers
	factory := informers.NewSharedInformerFactory(kubeClient, 0 /* no resync period */)
	pvInformer := factory.Core().V1().PersistentVolumes()
	pvcInformer := factory.Core().V1().PersistentVolumeClaims()
	scInformer := factory.Storage().V1().StorageClasses()

	// Factory for Pod informer
	podLabelSelector := fmt.Sprintf("%s=%s", profileManagedLabelKey, profileManagedLabelValue)
	tweakFunc := func(options *metav1.ListOptions) {
		options.LabelSelector = podLabelSelector
	}
	podFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		0, // No resync period for tests
		informers.WithTweakListOptions(tweakFunc),
		informers.WithTransform(trimPodObject),
	)
	podInformer := podFactory.Core().V1().Pods()

	// Manually add initial objects to the informer indexers to ensure they are available in listers.
	for _, obj := range initialObjects {
		switch obj := obj.(type) {
		case *v1.PersistentVolume:
			if err := pvInformer.Informer().GetIndexer().Add(obj); err != nil {
				t.Fatalf("Failed to add PV to indexer: %v", err)
			}
		case *v1.PersistentVolumeClaim:
			if err := pvcInformer.Informer().GetIndexer().Add(obj); err != nil {
				t.Fatalf("Failed to add PVC to indexer: %v", err)
			}
		case *storagev1.StorageClass:
			if err := scInformer.Informer().GetIndexer().Add(obj); err != nil {
				t.Fatalf("Failed to add SC to indexer: %v", err)
			}
		case *v1.Pod:
			// Must apply transform before adding to indexer to mimic real behavior
			trimmedObj, err := trimPodObject(obj)
			if err != nil {
				t.Fatalf("Failed to transform Pod: %v", err)
			}
			if err := podInformer.Informer().GetIndexer().Add(trimmedObj); err != nil {
				t.Fatalf("Failed to add Pod to indexer: %v", err)
			}
		}
	}

	recorder := record.NewFakeRecorder(30)
	mt := &mockTime{currentTime: time.Date(2025, time.August, 27, 0, 0, 0, 0, time.UTC)}
	origTimeNow := timeNow
	timeNow = mt.Now

	fsb := &fakeScanBucketImplFunc{}
	stopCh := make(chan struct{})

	t.Cleanup(func() {
		timeNow = origTimeNow
		close(stopCh)
	})

	// Create the Scanner instance with fake/mock components.
	s := &Scanner{
		kubeClient:     kubeClient,
		pvLister:       pvInformer.Lister(),
		pvcLister:      pvcInformer.Lister(),
		scLister:       scInformer.Lister(),
		podLister:      podInformer.Lister(),
		pvSynced:       pvInformer.Informer().HasSynced,
		pvcSynced:      pvcInformer.Informer().HasSynced,
		scSynced:       scInformer.Informer().HasSynced,
		podSynced:      podInformer.Informer().HasSynced,
		factory:        factory,
		podFactory:     podFactory,
		queue:          workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		eventRecorder:  recorder,
		trackedPVs:     make(map[string]syncInfo),
		datafluxConfig: &DatafluxConfig{}, // Use default or test-specific config
		scanBucketImpl: fsb.Scan,          // Inject the fake scan function
		metricManager:  metrics.NewFakePrometheusMetricsManager(),
	}

	factory.Start(stopCh)
	podFactory.Start(stopCh)
	if !cache.WaitForCacheSync(stopCh, s.pvSynced, s.pvcSynced, s.scSynced, s.podSynced) {
		t.Fatalf("Failed to sync caches")
	}

	return &testFixture{
		scanner:      s,
		kubeClient:   kubeClient,
		pvLister:     pvInformer.Lister(),
		pvcLister:    pvcInformer.Lister(),
		scLister:     scInformer.Lister(),
		podLister:    podInformer.Lister(),
		recorder:     recorder,
		mockTimeImpl: mt,
		scanBucketFn: fsb,
		stopCh:       stopCh,
	}
}

// createStorageClass is a helper function to create a StorageClass object.
func createStorageClass(name string, params map[string]string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name, Labels: map[string]string{"gke-gcsfuse/profile": "true"}},
		Provisioner: csiDriverName,
		Parameters:  params,
	}
}

// createPV is a helper function to create a PersistentVolume object.
func createPV(name, scName, volumeHandle, driver string, mountOptions []string, annotations map[string]string, volAttributes map[string]string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
		Spec: v1.PersistentVolumeSpec{
			StorageClassName: scName,
			MountOptions:     mountOptions,
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:           driver,
					VolumeHandle:     volumeHandle,
					VolumeAttributes: volAttributes,
				},
			},
		},
	}
}

// createPVC is a helper function to create a PersistentVolumeClaim object.
func createPVC(name, namespace, pvName, scName string) *v1.PersistentVolumeClaim {
	spec := v1.PersistentVolumeClaimSpec{
		AccessModes: []v1.PersistentVolumeAccessMode{v1.ReadWriteMany},
		Resources: v1.VolumeResourceRequirements{
			Requests: v1.ResourceList{
				v1.ResourceStorage: *resource.NewQuantity(1, resource.BinarySI),
			},
		},
	}
	if pvName != "" {
		spec.VolumeName = pvName
	}
	if scName != "" {
		spec.StorageClassName = &scName
	}

	return &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: spec,
	}
}

// createPod is a helper function to create a Pod object.
func createPod(name, namespace string, volumes []v1.Volume, labels map[string]string, withGate bool) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: v1.PodSpec{
			Volumes: volumes,
		},
	}
	if withGate {
		pod.Spec.SchedulingGates = []v1.PodSchedulingGate{{Name: schedulingGateName}}
	}
	return pod
}

func TestCheckPVRelevance(t *testing.T) {
	now := time.Date(2025, time.August, 27, 0, 0, 0, 0, time.UTC)
	resyncPeriod, err := time.ParseDuration(defaultScanResyncPeriodVal)
	if err != nil {
		t.Fatalf("parse duration error: %v", err)
	}
	lastUpdateTimeWithinResyncPeriod := now.Add(-resyncPeriod / 2).Format(time.RFC3339)
	lastUpdateTimeOutsideResyncPeriod := now.Add(-resyncPeriod * 2).Format(time.RFC3339)

	validOverrideAnnotations := map[string]string{
		putil.AnnotationStatus:     putil.ScanOverride,
		putil.AnnotationNumObjects: "1000",
		putil.AnnotationTotalSize:  "200000",
	}

	projectNumber, err := strconv.ParseUint(testProjectNumber, 10, 64)
	if err != nil {
		t.Fatalf("strconv.ParseUint() error = %v", err)
	}
	attrs := &storage.BucketAttrs{
		Name:          testBucketName,
		ProjectNumber: projectNumber,
	}

	testCases := []struct {
		name              string
		pv                *v1.PersistentVolume
		scs               []*storagev1.StorageClass
		wantRelevant      bool
		wantErr           bool
		wantBucket        string
		wantDir           string
		wantIsOverride    bool
		wantIsPendingScan bool
		wantIsZonalBucket bool
		fakeGetAttrs      fakebucketAttrsFunc
	}{
		{
			name:              "Relevant PV - Should return relevant and pending scan",
			pv:                createPV(testPVName, testSCName, testBucketName, csiDriverName, []string{onlyDirMountOptPrefix + ":" + testDirName}, nil, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantRelevant:      true,
			wantBucket:        testBucketName,
			wantDir:           testDirName,
			wantIsPendingScan: true,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Relevant PV - Training - Should return relevant and pending scan",
			pv:   createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{}),
			},
			wantRelevant:      true,
			wantBucket:        testBucketName,
			wantIsPendingScan: true,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Relevant PV - Serving - Should return relevant and pending scan",
			pv:   createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{}),
			},
			wantRelevant:      true,
			wantBucket:        testBucketName,
			wantIsPendingScan: true,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Relevant PV - Checkpointing - Should return relevant and pending scan",
			pv:   createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{}),
			},
			wantRelevant:      true,
			wantBucket:        testBucketName,
			wantIsPendingScan: true,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Irrelevant - Wrong Driver - Should return irrelevant and not pending scan",
			pv:   createPV(testPVName, testSCName, testBucketName, "blah", nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{}),
			},
			wantRelevant:      false,
			wantIsPendingScan: false,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name:              "Irrelevant - SC is nil - Should return irrelevant and not pending scan",
			pv:                createPV(testPVName, testSCName, testBucketName, "blah", nil, nil, nil),
			wantRelevant:      false,
			wantIsPendingScan: false,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Relevant but no scan pending - Within resync period - Should return relevant and not pending scan",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				putil.AnnotationLastUpdatedTime: lastUpdateTimeWithinResyncPeriod,
			}, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantBucket:        testBucketName,
			wantRelevant:      true,
			wantIsPendingScan: false,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Relevant scan is pending - Outside resync period - Should return relevant and pending scan",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				putil.AnnotationLastUpdatedTime: lastUpdateTimeOutsideResyncPeriod,
			}, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantRelevant:      true,
			wantBucket:        testBucketName,
			wantIsPendingScan: true,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Relevant scan is pending - Zonal bucket by locationType - Should return relevant, pending scan, and isZonalBucket field should be true",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				putil.AnnotationLastUpdatedTime: lastUpdateTimeOutsideResyncPeriod,
			}, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantRelevant:      true,
			wantBucket:        testBucketName,
			wantIsPendingScan: true,
			wantIsZonalBucket: true,
			fakeGetAttrs: fakebucketAttrsFunc{attrs: &storage.BucketAttrs{
				Name:          testBucketName,
				ProjectNumber: projectNumber,
				LocationType:  "zone",
			}},
		},
		{
			name: "Relevant scan is pending - Zonal bucket by storageClass - Should return relevant, pending scan, and isZonalBucket field should be true",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				putil.AnnotationLastUpdatedTime: lastUpdateTimeOutsideResyncPeriod,
			}, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantRelevant:      true,
			wantBucket:        testBucketName,
			wantIsPendingScan: true,
			wantIsZonalBucket: true,
			fakeGetAttrs: fakebucketAttrsFunc{attrs: &storage.BucketAttrs{
				Name:          testBucketName,
				ProjectNumber: projectNumber,
				StorageClass:  "rapid",
			}},
		},
		{
			name:              "Relevant - Override mode - Should return relevant and override",
			pv:                createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, validOverrideAnnotations, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantRelevant:      true,
			wantBucket:        testBucketName,
			wantIsOverride:    true,
			wantIsPendingScan: false,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Irrelevant - Override mode - Missing Annotations - Should return error",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				putil.AnnotationStatus: putil.ScanOverride,
				// Missing other required annotations
			}, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantRelevant:      false,
			wantErr:           true,
			wantIsPendingScan: false,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Irrelevant - Override mode - Invalid NumObjects - Should return error",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				putil.AnnotationStatus:     putil.ScanOverride,
				putil.AnnotationNumObjects: "not-a-number",
				putil.AnnotationTotalSize:  "200000",
			}, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantRelevant:      false,
			wantErr:           true,
			wantIsPendingScan: false,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
		{
			name: "Irrelevant - Override mode - Negative NumObjects - Should return error",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				putil.AnnotationStatus:     putil.ScanOverride,
				putil.AnnotationNumObjects: "-100",
				putil.AnnotationTotalSize:  "200000",
			}, nil),
			scs:               []*storagev1.StorageClass{validSC},
			wantRelevant:      false,
			wantErr:           true,
			wantIsPendingScan: false,
			fakeGetAttrs:      fakebucketAttrsFunc{attrs: attrs},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			if tc.pv != nil {
				objs = append(objs, tc.pv)
			}
			if tc.scs != nil {
				for _, sc := range tc.scs {
					objs = append(objs, sc)
				}
			}
			f := newTestFixture(t, objs...)
			f.mockTimeImpl.currentTime = now
			var sc *storagev1.StorageClass
			if len(tc.scs) > 0 {
				sc = tc.scs[0]
			}

			bucketAttrs = tc.fakeGetAttrs.getBucketAttributes
			bucketI, isPendingScan, err := f.scanner.checkPVRelevance(context.TODO(), tc.pv, sc)

			t.Cleanup(func() {
				bucketAttrs = origbucketAttrs
			})

			if tc.wantErr {
				if err == nil {
					t.Errorf("checkPVRelevance(%v) = %v, nil, want error", tc.pv.Name, bucketI)
				}
			} else {
				if err != nil {
					t.Errorf("checkPVRelevance(%v) = %v, %v, want no error", tc.pv.Name, bucketI, err)
				}
			}

			isRelevant := bucketI != nil
			if isRelevant != tc.wantRelevant {
				t.Errorf("checkPVRelevance(%v) relevant = %v, want %v", tc.pv.Name, isRelevant, tc.wantRelevant)
			}

			if tc.wantRelevant && bucketI != nil {
				if bucketI.name != tc.wantBucket {
					t.Errorf("checkPVRelevance(%v) bucket = %q, want %q", tc.pv.Name, bucketI.name, tc.wantBucket)
				}
				if bucketI.dir != tc.wantDir {
					t.Errorf("checkPVRelevance(%v) dir = %q, want %q", tc.pv.Name, bucketI.dir, tc.wantDir)
				}
				if bucketI.isOverride != tc.wantIsOverride {
					t.Errorf("checkPVRelevance(%v) isOverride = %v, want %v", tc.pv.Name, bucketI.isOverride, tc.wantIsOverride)
				}
				if bucketI.isZonalBucket != tc.wantIsZonalBucket {
					t.Errorf("checkPVRelevance(%v) bucket = %t, want %t", tc.pv.Name, bucketI.isZonalBucket, tc.wantIsZonalBucket)
				}
			}
			if isPendingScan != tc.wantIsPendingScan {
				t.Errorf("checkPVRelevance(%v) isPendingScan = %v, want %v", tc.pv.Name, isPendingScan, tc.wantIsPendingScan)
			}
		})
	}
}

// TestSyncPV tests the syncPV function.
func TestSyncPV(t *testing.T) {
	pvName := testPVName
	scName := testSCName
	bucketName := testBucketName
	basePV := createPV(pvName, scName, bucketName, csiDriverName, nil, nil, nil)
	relevantSC := createStorageClass(scName, validSCParams)
	irrelevantSC := createStorageClass("irrelevant-sc", map[string]string{"some": "param"})
	irrelevantSC.Labels = nil
	pvIrrelevantSC := createPV("irrelevant-pv", "irrelevant-sc", bucketName, csiDriverName, nil, nil, nil)

	overrideAnnotations := map[string]string{
		putil.AnnotationStatus:     putil.ScanOverride,
		putil.AnnotationNumObjects: "111",
		putil.AnnotationTotalSize:  "2222",
	}
	pvOverride := createPV(pvName, scName, bucketName, csiDriverName, nil, overrideAnnotations, nil)

	attrs := &storage.BucketAttrs{
		Name:          testBucketName,
		ProjectNumber: 111,
	}
	attrsFunc := fakebucketAttrsFunc{attrs: attrs}
	bucketAttrs = attrsFunc.getBucketAttributes

	t.Cleanup(func() {
		bucketAttrs = origbucketAttrs
	})

	testCases := []struct {
		name            string
		key             string
		initialObjects  []runtime.Object
		bucketI         *bucketInfo // Mocked result for non-override scans
		scanErr         error
		patchErr        error
		wantErr         bool
		expectScanCall  bool
		expectedAnnots  map[string]string
		expectResync    bool
		controllerCrash bool
	}{
		{
			name:           "Successful Sync - Should patch PV annotations",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			bucketI:        scanResult,
			wantErr:        false,
			expectedAnnots: map[string]string{
				putil.AnnotationNumObjects: "1234",
				putil.AnnotationTotalSize:  "567890",
				putil.AnnotationStatus:     "completed",
			},
			expectScanCall: true,
		},
		{
			name:           "Successful Re-sync - Should patch PV annotations twice",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			bucketI:        scanResult,
			expectedAnnots: map[string]string{
				putil.AnnotationNumObjects: "1234",
				putil.AnnotationTotalSize:  "567890",
				putil.AnnotationStatus:     "completed",
			},
			wantErr:        false,
			expectScanCall: true,
			expectResync:   true,
		},
		{
			name:           "Successful Re-sync - Controller Crash - Should patch PV annotations twice",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			bucketI:        scanResult,
			expectedAnnots: map[string]string{
				putil.AnnotationNumObjects: "1234",
				putil.AnnotationTotalSize:  "567890",
				putil.AnnotationStatus:     "completed",
			},
			wantErr:         false,
			expectScanCall:  true,
			expectResync:    true,
			controllerCrash: true,
		},
		{
			name:           "Scan Error",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			scanErr:        fmt.Errorf("scan failed"),
			wantErr:        true,
			expectScanCall: true,
		},
		{
			name:           "Scan Timeout",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			bucketI:        scanResult,
			scanErr:        context.DeadlineExceeded,
			wantErr:        false,
			expectedAnnots: map[string]string{
				putil.AnnotationNumObjects: "0",
				putil.AnnotationTotalSize:  "0",
				putil.AnnotationStatus:     "timeout",
			},
			expectScanCall: true,
		},
		{
			name:           "Patch Error",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			bucketI:        scanResult,
			patchErr:       fmt.Errorf("patch failed"),
			wantErr:        true,
			expectScanCall: true,
		},
		{
			name:           "PV Not Found",
			key:            pvName,
			initialObjects: []runtime.Object{relevantSC},
			wantErr:        false, // Not found is not an error for syncPV, just stops processing.
			expectScanCall: false,
		},
		{
			name:           "SC Not Found",
			key:            pvName,
			initialObjects: []runtime.Object{},
			wantErr:        false,
			expectScanCall: false,
		},
		{
			name:           "PV Not Relevant",
			key:            "irrelevant-pv",
			initialObjects: []runtime.Object{pvIrrelevantSC, irrelevantSC},
			wantErr:        false,
			expectScanCall: false,
		},
		{
			name:           "Successful Sync - Override Mode - Should bypass scan",
			key:            pvName,
			initialObjects: []runtime.Object{pvOverride.DeepCopy(), relevantSC},
			wantErr:        false,
			expectScanCall: false, // Scan should be bypassed
			expectedAnnots: map[string]string{
				putil.AnnotationNumObjects: "111",
				putil.AnnotationTotalSize:  "2222",
				putil.AnnotationStatus:     "override",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFixture(t, tc.initialObjects...)

			f.scanBucketFn.bucketI = tc.bucketI
			f.scanBucketFn.err = tc.scanErr
			f.scanBucketFn.wasCalled = false

			if tc.patchErr != nil {
				f.kubeClient.PrependReactor("patch", "persistentvolumes", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, tc.patchErr
				})
			}

			ctx := context.Background()
			err := f.scanner.syncPV(ctx, tc.key)

			if tc.wantErr != (err != nil) {
				t.Errorf("syncPV(%q) returned error: %v, wantErr: %v", tc.key, err, tc.wantErr)
			}

			if f.scanBucketFn.wasCalled != tc.expectScanCall {
				t.Errorf("syncPV(%q) scanBucketImpl call status: got %v, want %v", tc.key, f.scanBucketFn.wasCalled, tc.expectScanCall)
			}

			if tc.expectedAnnots == nil {
				return
			}

			updatedPV, err := f.kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), tc.key, metav1.GetOptions{})
			if err != nil {
				t.Errorf("Failed to get PV: %v", err)
			}

			lastScanTime, ok := updatedPV.Annotations[putil.AnnotationLastUpdatedTime]
			if !ok {
				t.Errorf("Annotation %q not found in PV", putil.AnnotationLastUpdatedTime)
			}

			for key, want := range tc.expectedAnnots {
				if got := updatedPV.Annotations[key]; got != want {
					t.Errorf("Annotation %q: got %v, want %v", key, got, want)
				}
			}

			if tc.expectResync {
				if tc.controllerCrash {
					// Simulate controller losing the in-memory state because of a crash.
					f.scanner.trackedPVs = make(map[string]syncInfo)
				}
				scanResyncPeriod, err := time.ParseDuration(defaultScanResyncPeriodVal)
				if err != nil {
					t.Fatalf("parse duration error: %v", err)
				}
				f.mockTimeImpl.currentTime = f.mockTimeImpl.currentTime.Add(scanResyncPeriod * 2)
				err = f.scanner.syncPV(context.Background(), tc.key)
				if tc.wantErr != (err != nil) {
					t.Errorf("syncPV(%q) returned error: %v, wantErr: %v", tc.key, err, tc.wantErr)
				}
				updatedPV, err := f.kubeClient.CoreV1().PersistentVolumes().Get(ctx, tc.key, metav1.GetOptions{})
				if err != nil {
					t.Errorf("Failed to get PV: %v", err)
				}
				newScanTime, ok := updatedPV.Annotations[putil.AnnotationLastUpdatedTime]
				if !ok {
					t.Errorf("Annotation %q not found in PV", putil.AnnotationLastUpdatedTime)
				}
				if newScanTime == lastScanTime {
					t.Errorf("Expected a new scan, but the PV was only scanned once. Old timestamp: %s, new timestamp: %s", lastScanTime, newScanTime)
				}
			}
		})
	}
}

// TestSyncPod tests the syncPod function.
func TestSyncPod(t *testing.T) {
	// Common resources
	pvRelevant := createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil)
	pvNotRelevant := createPV("pv-not-relevant", "sc-not-relevant", testBucketName, "other-driver", nil, nil, nil)
	scValid := createStorageClass(testSCName, validSCParams)
	scNotRelevant := createStorageClass("sc-not-relevant", nil)
	pvcBoundRelevant := createPVC(testPVCName, testNamespace, testPVName, testSCName)
	pvcEmptySC := createPVC(testPVCName, testNamespace, testPVName, "")
	pvcBoundNotRelevant := createPVC("pvc-not-relevant", testNamespace, "pv-not-relevant", "sc-not-relevant")
	pvcUnbound := createPVC("pvc-unbound", testNamespace, "", testSCName)

	volRelevant := v1.Volume{Name: "vol1", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: testPVCName}}}
	volNotRelevant := v1.Volume{Name: "vol2", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: "pvc-not-relevant"}}}
	volUnbound := v1.Volume{Name: "vol3", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: "pvc-unbound"}}}
	volMissingPVC := v1.Volume{Name: "vol4", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: "pvc-missing"}}}

	// Resources for Override test
	pvOverrideAnnotations := map[string]string{
		putil.AnnotationStatus:     putil.ScanOverride,
		putil.AnnotationNumObjects: "100",
		putil.AnnotationTotalSize:  "200",
	}
	pvOverride := createPV("pv-override", testSCName, testBucketName, csiDriverName, nil, pvOverrideAnnotations, nil)
	pvcBoundOverride := createPVC("pvc-override", testNamespace, "pv-override", testSCName)
	volOverride := v1.Volume{Name: "vol-override", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: "pvc-override"}}}

	attrs := &storage.BucketAttrs{
		Name:          testBucketName,
		ProjectNumber: 111,
	}
	attrsFunc := fakebucketAttrsFunc{attrs: attrs}
	bucketAttrs = attrsFunc.getBucketAttributes

	t.Cleanup(func() {
		bucketAttrs = origbucketAttrs
	})

	testCases := []struct {
		name              string
		pod               *v1.Pod
		initialObjects    []runtime.Object
		expectRequeueErr  bool
		wantErr           bool
		expectGateRemoved bool
	}{
		{
			name:              "Pod with no volumes, gate removed",
			pod:               createPod(testPodName, testNamespace, nil, podLabels, true),
			initialObjects:    []runtime.Object{},
			expectGateRemoved: true,
		},
		{
			name:             "Pod with relevant PV, PV enqueued, Pod requeued",
			pod:              createPod(testPodName, testNamespace, []v1.Volume{volRelevant}, podLabels, true),
			initialObjects:   []runtime.Object{pvRelevant, scValid, pvcBoundRelevant},
			expectRequeueErr: true,
		},
		{
			name:              "Pod with irrelevant PV, gate removed",
			pod:               createPod(testPodName, testNamespace, []v1.Volume{volNotRelevant}, podLabels, true),
			initialObjects:    []runtime.Object{pvNotRelevant, scNotRelevant, pvcBoundNotRelevant},
			expectGateRemoved: true,
		},
		{
			name:             "Pod with mixed PVs (relevant and not), Pod requeued",
			pod:              createPod(testPodName, testNamespace, []v1.Volume{volNotRelevant, volRelevant}, podLabels, true),
			initialObjects:   []runtime.Object{pvRelevant, scValid, pvcBoundRelevant, pvNotRelevant, scNotRelevant, pvcBoundNotRelevant},
			expectRequeueErr: true,
		},
		{
			name:             "Pod with unbound PVC, Pod requeued",
			pod:              createPod(testPodName, testNamespace, []v1.Volume{volUnbound}, podLabels, true),
			initialObjects:   []runtime.Object{pvcUnbound},
			expectRequeueErr: true,
		},
		{
			name:             "Pod with missing PVC, Pod requeued",
			pod:              createPod(testPodName, testNamespace, []v1.Volume{volMissingPVC}, podLabels, true),
			initialObjects:   []runtime.Object{},
			expectRequeueErr: true,
		},
		{
			name:           "Pod not found, no error",
			pod:            createPod("other-pod", testNamespace, nil, podLabels, false),
			initialObjects: []runtime.Object{},
		},
		{
			name:             "PVC found, but PV not found",
			pod:              createPod(testPodName, testNamespace, []v1.Volume{volRelevant}, podLabels, true),
			initialObjects:   []runtime.Object{pvcBoundRelevant},
			expectRequeueErr: true,
		},
		{
			name:             "PVC / PV found, but SC not found",
			pod:              createPod(testPodName, testNamespace, []v1.Volume{volRelevant}, podLabels, true),
			initialObjects:   []runtime.Object{pvcEmptySC},
			expectRequeueErr: true,
		},
		{
			name:              "Pod with Override PV - Should remove gate",
			pod:               createPod(testPodName, testNamespace, []v1.Volume{volOverride}, podLabels, true),
			initialObjects:    []runtime.Object{pvOverride, scValid, pvcBoundOverride},
			expectGateRemoved: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			allObjects := tc.initialObjects
			// Add the pod to initialObjects only if it's the one being tested
			if tc.pod.Name == testPodName {
				allObjects = append(allObjects, tc.pod)
			}
			f := newTestFixture(t, allObjects...)

			key, _ := cache.MetaNamespaceKeyFunc(tc.pod)
			ctx := context.Background()
			err := f.scanner.syncPV(ctx, pvOverride.Name)
			if err != nil {
				t.Errorf("syncPV(%q) returned error: %v", key, err)
			}
			err = f.scanner.syncPod(ctx, key)

			hasErr := err != nil
			if hasErr != (tc.wantErr || tc.expectRequeueErr) {
				t.Errorf("syncPod(%q) returned error: %v, wantErr: %v, wantRequeueErr: %v", key, err, tc.wantErr, tc.expectRequeueErr)
			}

			if err != nil {
				isRequeueErr := errors.Is(err, errRequeueNeeded)
				if isRequeueErr != tc.expectRequeueErr {
					t.Errorf("syncPod(%q) error type: got requeue %v, want requeue %v (err: %v)", key, isRequeueErr, tc.expectRequeueErr, err)
				}
			}

			if tc.expectGateRemoved {
				updatedPod, err := f.kubeClient.CoreV1().Pods(testNamespace).Get(context.Background(), testPodName, metav1.GetOptions{})
				if err != nil {
					t.Fatalf("Failed to get Pod: %v", err)
				}
				gateExists := false
				for _, gate := range updatedPod.Spec.SchedulingGates {
					if gate.Name == schedulingGateName {
						gateExists = true
						break
					}
				}
				if gateExists {
					t.Errorf("Scheduling gate %q was not removed from Pod %q", schedulingGateName, key)
				}
			}
		})
	}
}

// TestProcessNextWorkItem tests the processNextWorkItem function.
func TestProcessNextWorkItem(t *testing.T) {
	pvName := testPVName
	podName := testPodName
	namespace := testNamespace

	pvKey, _ := cache.MetaNamespaceKeyFunc(createPV(pvName, "", "", "", nil, nil, nil))
	podKey, _ := cache.MetaNamespaceKeyFunc(createPod(podName, namespace, nil, nil, false))
	pvQueueKey := pvPrefix + pvKey
	podQueueKey := podPrefix + podKey

	// PV related objects
	pvRelevant := createPV(pvName, testSCName, testBucketName, csiDriverName, nil, nil, nil)
	scValid := createStorageClass(testSCName, validSCParams)

	// Pod related objects
	pvcForPod := createPVC(testPVCName, namespace, pvName, testSCName)
	volForPod := v1.Volume{Name: "vol1", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: testPVCName}}}
	podRelevant := createPod(podName, namespace, []v1.Volume{volForPod}, podLabels, true)

	attrs := &storage.BucketAttrs{
		Name:          testBucketName,
		ProjectNumber: 111,
	}
	attrsFunc := fakebucketAttrsFunc{attrs: attrs}
	bucketAttrs = attrsFunc.getBucketAttributes

	t.Cleanup(func() {
		bucketAttrs = origbucketAttrs
	})

	testCases := []struct {
		name                 string
		keyToAdd             string
		initialObjects       []runtime.Object
		bucketI              *bucketInfo // For PV sync
		scanErr              error       // For PV sync
		patchErr             error       // For PV or Pod sync
		expectRequeue        bool
		expectAnnotate       bool   // For PV sync
		expectedStatus       string // For PV sync
		expectGateRemoved    bool   // For Pod sync
		expectSyncPVCounter  map[string]int
		expectSyncPodCounter map[string]int
	}{
		// PV Key Tests
		{
			name:                 "PV Sync Successful",
			keyToAdd:             pvQueueKey,
			initialObjects:       []runtime.Object{pvRelevant, scValid},
			bucketI:              scanResult,
			expectRequeue:        false,
			expectAnnotate:       true,
			expectedStatus:       scanCompleted,
			expectSyncPVCounter:  map[string]int{codes.OK.String(): 1},
			expectSyncPodCounter: map[string]int{},
		},
		{
			name:                 "PV Sync Scan Error",
			keyToAdd:             pvQueueKey,
			initialObjects:       []runtime.Object{pvRelevant, scValid},
			scanErr:              status.Errorf(codes.Internal, "scan failed"),
			expectRequeue:        true,
			expectSyncPVCounter:  map[string]int{codes.Internal.String(): 1},
			expectSyncPodCounter: map[string]int{},
		},
		{
			name:                 "PV Sync Patch Error",
			keyToAdd:             pvQueueKey,
			initialObjects:       []runtime.Object{pvRelevant, scValid},
			bucketI:              scanResult,
			patchErr:             fmt.Errorf("patch failed"),
			expectRequeue:        true,
			expectSyncPVCounter:  map[string]int{codes.Unknown.String(): 1},
			expectSyncPodCounter: map[string]int{},
		},
		// Pod Key Tests
		{
			name:                 "Pod Sync Successful - Gate Removed",
			keyToAdd:             podQueueKey,
			initialObjects:       []runtime.Object{createPod(podName, namespace, nil, podLabels, true)}, // No volumes, gate should be removed
			expectRequeue:        false,
			expectGateRemoved:    true,
			expectSyncPVCounter:  map[string]int{},
			expectSyncPodCounter: map[string]int{codes.OK.String(): 1},
		},
		{
			name:                 "Pod Sync Requeue - Relevant PV",
			keyToAdd:             podQueueKey,
			initialObjects:       []runtime.Object{podRelevant, pvcForPod, pvRelevant, scValid},
			expectRequeue:        true, // Pod requeued because PV needs scan
			expectSyncPVCounter:  map[string]int{},
			expectSyncPodCounter: map[string]int{codes.OK.String(): 1},
		},
		{
			name:                 "Unknown Key Prefix",
			keyToAdd:             "unknown/" + pvKey,
			expectRequeue:        false,
			expectSyncPVCounter:  map[string]int{},
			expectSyncPodCounter: map[string]int{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFixture(t, tc.initialObjects...)

			// Setup for PV sync parts
			f.scanBucketFn.bucketI = tc.bucketI
			f.scanBucketFn.err = tc.scanErr

			if tc.patchErr != nil {
				f.kubeClient.PrependReactor("patch", "*", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, tc.patchErr
				})
			}

			f.scanner.queue.Add(tc.keyToAdd)

			if !f.scanner.processNextWorkItem(context.Background()) {
				t.Fatalf("processNextWorkItem returned false, expected true")
			}

			// Check requeue status
			numRequeues := f.scanner.queue.NumRequeues(tc.keyToAdd)
			wasRequeued := numRequeues > 0
			if tc.expectRequeue != wasRequeued {
				t.Errorf("Key %q requeue status: got %v (requeues: %d), want %v", tc.keyToAdd, wasRequeued, numRequeues, tc.expectRequeue)
			}

			// Check PV annotations if expected
			if tc.expectAnnotate {
				updatedPV, err := f.kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), pvName, metav1.GetOptions{})
				if err != nil {
					t.Fatalf("Failed to get PV: %v", err)
				}
				if status := updatedPV.Annotations[putil.AnnotationStatus]; status != tc.expectedStatus {
					t.Errorf("Annotation %q: got %v, want %v", putil.AnnotationStatus, status, tc.expectedStatus)
				}
			}

			// Check Pod gate removal if expected
			if tc.expectGateRemoved {
				updatedPod, err := f.kubeClient.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
				if err != nil {
					if apierrors.IsNotFound(err) {
						t.Fatalf("Pod %q not found, but expected to exist for gate check", podName)
					}
					t.Fatalf("Failed to get Pod: %v", err)
				}
				gateExists := false
				for _, gate := range updatedPod.Spec.SchedulingGates {
					if gate.Name == schedulingGateName {
						gateExists = true
						break
					}
				}
				if gateExists {
					t.Errorf("Scheduling gate %q was not removed from Pod %q", schedulingGateName, podName)
				}
			}
		})
	}
}

// TestAddPV tests the addPV function, which handles new PV additions.
func TestAddPV(t *testing.T) {
	pvRelevant := createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil)
	scValid := createStorageClass(testSCName, validSCParams)
	f := newTestFixture(t, pvRelevant, scValid)

	f.scanner.addPV(pvRelevant)

	// Expect the PV to be added to the queue and tracked.
	if f.scanner.queue.Len() != 1 {
		t.Errorf("Queue length: got %d, want 1", f.scanner.queue.Len())
	}

	// Adding the same PV again should not change the queue length (it's a set).
	f.scanner.addPV(pvRelevant)
	if f.scanner.queue.Len() != 1 {
		t.Errorf("Queue length after duplicate add: got %d, want 1", f.scanner.queue.Len())
	}
}

// TestDeletePV tests the deletePV function.
func TestDeletePV(t *testing.T) {
	pvName := testPVName
	key := pvName
	pv := createPV(pvName, testSCName, testBucketName, csiDriverName, nil, nil, nil)
	queueKey := pvPrefix + key

	f := newTestFixture(t)
	f.scanner.queue.Add(queueKey)
	f.scanner.trackedPVs[pvName] = syncInfo{}

	f.scanner.deletePV(pv)

	// PV should no longer be in the tracked set.
	if _, exists := f.scanner.trackedPVs[pvName]; exists {
		t.Errorf("PV %q still in lastSuccessfulScan map after deletePV() call", pvName)
	}

	// Check if the key was forgotten
	if f.scanner.queue.NumRequeues(queueKey) != 0 {
		t.Errorf("NumRequeues for %q: got %d, want 0 after deletePV/Forget", key, f.scanner.queue.NumRequeues(queueKey))
	}
}

// TestAddPod tests the addPod function.
func TestAddPod(t *testing.T) {
	pod := createPod(testPodName, testNamespace, nil, podLabels, false)
	f := newTestFixture(t, pod)

	f.scanner.addPod(pod)

	if f.scanner.queue.Len() != 1 {
		t.Errorf("Queue length: got %d, want 1", f.scanner.queue.Len())
	}
	// We can't easily assert *which* key is in the queue, but length 1 after adding one item is a good sign.
}

// TestDeletePod tests the deletePod function.
func TestDeletePod(t *testing.T) {
	pod := createPod(testPodName, testNamespace, nil, podLabels, false)
	key, _ := cache.MetaNamespaceKeyFunc(pod)
	queueKey := podPrefix + key

	f := newTestFixture(t)

	// Add the key to simulate it being in the queue, possibly with a history of failures.
	f.scanner.queue.AddRateLimited(queueKey)
	// NumRequeues check can be flaky with fake rate limiters, so we don't assert on it.

	f.scanner.deletePod(pod)

	// After deletePod, the key should be "forgotten", resetting its rate limiting.
	if f.scanner.queue.NumRequeues(queueKey) != 0 {
		t.Errorf("NumRequeues for %q: got %d, want 0 after deletePod/Forget", key, f.scanner.queue.NumRequeues(queueKey))
	}
}

// TestGetScanTimeout tests the getDurationAttribute function.
func TestGetDurationAttribute(t *testing.T) {
	customScanTimeoutVal := "42m"
	customScanResyncPeriodVal := "5m"
	testCases := []struct {
		name            string
		attributeKey    string
		attributes      map[string]string
		defaultDuration string
		wantTimeout     string
		wantErr         bool
	}{
		{
			name:            "No bucket scan timeout attribute - Use default",
			attributeKey:    scanTimeoutKey,
			defaultDuration: defaultScanTimeoutVal,
			wantTimeout:     defaultScanTimeoutVal,
			wantErr:         false,
		},
		{
			name:            "No bucket scan resync period attribute - Use default",
			attributeKey:    scanResyncPeriodKey,
			defaultDuration: defaultScanResyncPeriodVal,
			wantTimeout:     defaultScanResyncPeriodVal,
			wantErr:         false,
		},
		{
			name:            "Valid bucket scan timeout attribute - Override",
			attributes:      map[string]string{scanTimeoutKey: "42m"},
			defaultDuration: defaultScanTimeoutVal,
			attributeKey:    scanTimeoutKey,
			wantTimeout:     customScanTimeoutVal,
			wantErr:         false,
		},
		{
			name:            "Valid bucket scan resync period attribute - Override",
			attributes:      map[string]string{scanResyncPeriodKey: "5m"},
			defaultDuration: defaultScanResyncPeriodVal,
			attributeKey:    scanResyncPeriodKey,
			wantTimeout:     customScanResyncPeriodVal,
			wantErr:         false,
		},
		{
			name:            "Invalid duration - Error",
			attributes:      map[string]string{scanTimeoutKey: "5min"},
			attributeKey:    scanTimeoutKey,
			defaultDuration: defaultScanTimeoutVal,
			wantErr:         true,
		},
		{
			name:            "Non-positive duration - Error",
			attributes:      map[string]string{scanTimeoutKey: "0s"},
			attributeKey:    scanTimeoutKey,
			defaultDuration: defaultScanTimeoutVal,
			wantErr:         true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pv := createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, tc.attributes)
			f := newTestFixture(t)
			timeout, err := f.scanner.getDurationAttribute(pv, nil, tc.attributeKey, tc.defaultDuration)

			if tc.wantErr {
				if err == nil {
					t.Errorf("getDurationAttribute() error = %v, wantErr %v", err, tc.wantErr)
				}
			} else {
				wantTimeout, err := time.ParseDuration(tc.wantTimeout)
				if err != nil {
					t.Fatalf("parse duration error: %v", err)
				}
				if timeout != wantTimeout {
					t.Errorf("getDurationAttribute() = %v, want %v", timeout, tc.wantTimeout)
				}
			}
		})
	}
}

// TestUpdatePVScanResult tests the updatePVScanResult function.
// This function updates the PV annotations with the scan results.
func TestUpdatePVScanResult(t *testing.T) {
	pv := createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil)
	f := newTestFixture(t, pv)
	now := time.Date(2025, 9, 1, 10, 0, 0, 0, time.UTC)
	scanResyncPeriod, err := time.ParseDuration(defaultScanResyncPeriodVal)
	if err != nil {
		t.Fatalf("parse duration error: %v", err)
	}
	future := now.Add(scanResyncPeriod)
	f.mockTimeImpl.currentTime = now

	bucketI := &bucketInfo{
		numObjects:     999,
		totalSizeBytes: 8888,
	}
	status := scanCompleted

	// Call the function to update annotations.
	if err := f.scanner.updatePVScanResult(context.Background(), pv, nil, bucketI, status); err != nil {
		t.Fatalf("updatePVScanResult() returned error: %v", err)
	}

	// Get the updated PV from the fake client.
	updatedPV, err := f.kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), testPVName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get PV: %v", err)
	}

	annotations := updatedPV.Annotations
	if annotations == nil {
		t.Fatalf("Annotations are nil")
	}

	// Define the expected annotations.
	expectedAnnotations := map[string]string{
		putil.AnnotationStatus:          status,
		putil.AnnotationNumObjects:      "999",
		putil.AnnotationTotalSize:       "8888",
		putil.AnnotationLastUpdatedTime: now.UTC().Format(time.RFC3339),
	}

	// Verify each expected annotation.
	for key, want := range expectedAnnotations {
		got := annotations[key]
		if got != want {
			t.Errorf("Annotation %q: got %v, want %v", key, got, want)
		}
	}

	// Verify in-memory map is updated
	if syncInfo, ok := f.scanner.trackedPVs[testPVName]; !ok || !syncInfo.lastSuccessfulScan.Equal(now) || !syncInfo.nextScan.Equal(future) {
		t.Errorf("lastSuccessfulScan map not updated correctly: got last successful scan: %v, next scan: %v, want last successful scan: %v, next scan: %v", syncInfo.lastSuccessfulScan, syncInfo.nextScan, now, future)
	}
}

// TestPatchPVAnnotations tests the patchPVAnnotations function.
// This function patches specific annotations on a PV.
func TestPatchPVAnnotations(t *testing.T) {
	pv := createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{oldAnnotationKey: "val"}, nil)
	f := newTestFixture(t, pv)

	// Annotations to update: "new" to add, "old" to remove (by setting value to nil).
	annotationsToUpdate := map[string]*string{
		newAnnotationKey: stringPtr(newAnnotationVal),
		oldAnnotationKey: nil,
	}

	if err := f.scanner.patchPVAnnotations(context.Background(), testPVName, annotationsToUpdate); err != nil {
		t.Fatalf("patchPVAnnotations() returned error: %v", err)
	}

	// Get the updated PV.
	updatedPV, err := f.kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), testPVName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get PV: %v", err)
	}
	// Expected annotations after the patch.
	expectedAnnotations := map[string]string{newAnnotationKey: newAnnotationVal}
	if !reflect.DeepEqual(updatedPV.Annotations, expectedAnnotations) {
		t.Errorf("Annotations: got %v, want %v", updatedPV.Annotations, expectedAnnotations)
	}
}

// TestGetOnlyDirValue tests the getOnlyDirValue function.
// This function extracts the directory name from a mount option string.
func TestGetOnlyDirValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{
			name:  "Valid mount option in key:val format with dir name ends with '/'",
			input: onlyDirMountOptPrefix + ":" + testDirName + "/",
			want:  testDirName,
			ok:    true,
		},
		{
			name:  "Valid mount option in key=val format with dir name ends by '/'",
			input: onlyDirMountOptPrefix + "=" + testDirName + "/",
			want:  testDirName,
			ok:    true,
		},
		{
			name:  "Valid mount option without dir name",
			input: onlyDirMountOptPrefix + ":",
			want:  "",
			ok:    true,
		},
		{
			name:  "Invalid mount option",
			input: "blah=" + testDirName,
			want:  "",
			ok:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := onlyDirValue(tc.input)
			if ok != tc.ok || got != tc.want {
				t.Errorf("getOnlyDirValue(%q) = %q, %v, want %q, %v", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDefaultScanBucket(t *testing.T) {
	testPV := createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil)
	testPVRequestBucketMetrics := createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, map[string]string{useBucketMetricsKey: "true"})
	testSC := createStorageClass(testSCName, nil)
	testSCRequestBucketMetrics := createStorageClass(testSCName, map[string]string{useBucketMetricsKey: "true"})
	datafluxConfig := &DatafluxConfig{Parallelism: 1, BatchSize: 10}

	tests := []struct {
		name string
		// Input to defaultScanBucket
		inputBucketI *bucketInfo
		// Mock setup
		fakeMetrics  fakeScanFunc
		fakeDataflux fakeScanFunc
		// Expectations
		wantBucketI        *bucketInfo
		wantErr            bool
		expectMetricsCall  bool
		expectDatafluxCall bool
		useBucketMetrics   bool
		scFallback         bool
	}{
		{
			name:         "Directory specified - Dataflux success",
			inputBucketI: &bucketInfo{name: testBucketName, dir: testDirName},
			fakeDataflux: fakeScanFunc{
				numObjects:     200,
				totalSizeBytes: 2000,
			},
			wantBucketI: &bucketInfo{
				name: testBucketName, dir: testDirName,
				numObjects: 200, totalSizeBytes: 2000,
			},
			expectMetricsCall:  false,
			expectDatafluxCall: true,
		},
		{
			name:               "Directory specified - Dataflux error",
			inputBucketI:       &bucketInfo{name: testBucketName, dir: testDirName},
			fakeDataflux:       fakeScanFunc{err: errors.New("dataflux failed")},
			wantErr:            true,
			expectMetricsCall:  false,
			expectDatafluxCall: true,
		},
		{
			name:         "Directory specified - useBucketMetrics true - Skip bucket metrics and use Dataflux",
			inputBucketI: &bucketInfo{name: testBucketName, dir: testDirName},
			fakeDataflux: fakeScanFunc{
				numObjects:     200,
				totalSizeBytes: 2000,
			},
			wantBucketI: &bucketInfo{
				name: testBucketName, dir: testDirName,
				numObjects: 200, totalSizeBytes: 2000,
			},
			wantErr:            false,
			expectMetricsCall:  false,
			expectDatafluxCall: true,
			useBucketMetrics:   true,
		},
		{
			name:         "No directory specified - useBucketMetrics true - Metrics success",
			inputBucketI: &bucketInfo{name: testBucketName},
			fakeMetrics: fakeScanFunc{
				numObjects:     100,
				totalSizeBytes: 1000,
			},
			wantBucketI: &bucketInfo{
				name: testBucketName, numObjects: 100, totalSizeBytes: 1000,
			},
			expectMetricsCall:  true,
			expectDatafluxCall: false,
			useBucketMetrics:   true,
		},
		{
			name:         "No directory specified - SC useBucketMetrics fallback - Metrics success",
			inputBucketI: &bucketInfo{name: testBucketName},
			fakeMetrics: fakeScanFunc{
				numObjects:     100,
				totalSizeBytes: 1000,
			},
			wantBucketI: &bucketInfo{
				name: testBucketName, numObjects: 100, totalSizeBytes: 1000,
			},
			expectMetricsCall:  true,
			expectDatafluxCall: false,
			useBucketMetrics:   true,
			scFallback:         true,
		},
		{
			name:         "No directory specified - useBucketMetrics true - Metrics error, Dataflux success (Fallback)",
			inputBucketI: &bucketInfo{name: testBucketName},
			fakeMetrics: fakeScanFunc{
				numObjects:     100,
				totalSizeBytes: 1000,
				err:            errors.New("metrics failed"),
			},
			fakeDataflux: fakeScanFunc{
				numObjects:     200,
				totalSizeBytes: 2000,
			},
			wantBucketI: &bucketInfo{
				name: testBucketName, numObjects: 200, totalSizeBytes: 2000,
			},
			expectMetricsCall:  true,
			expectDatafluxCall: true,
			useBucketMetrics:   true,
		},
		{
			name:               "No directory specified - useBucketMetrics true - Metrics error, Dataflux error (Fallback Fails)",
			inputBucketI:       &bucketInfo{name: testBucketName},
			fakeMetrics:        fakeScanFunc{err: errors.New("metrics failed")},
			fakeDataflux:       fakeScanFunc{err: errors.New("dataflux failed")},
			wantErr:            true,
			expectMetricsCall:  true,
			expectDatafluxCall: true,
			useBucketMetrics:   true,
		},
		{
			name: "No directory specified - useBucketMetrics false - Dataflux success",
			fakeDataflux: fakeScanFunc{
				numObjects:     200,
				totalSizeBytes: 2000,
			},
			inputBucketI: &bucketInfo{name: testBucketName},
			wantBucketI: &bucketInfo{
				name: testBucketName, numObjects: 200, totalSizeBytes: 2000,
			},
			expectMetricsCall:  false,
			expectDatafluxCall: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFixture(t)
			f.scanner.datafluxConfig = datafluxConfig

			// Setup fakes
			scanBucketWithMetrics = tc.fakeMetrics.scanBucketWithMetrics
			scanBucketWithDataflux = tc.fakeDataflux.scanBucketWithDataflux

			t.Cleanup(func() {
				scanBucketWithMetrics = origScanBucketWithMetrics
				scanBucketWithDataflux = origScanBucketWithDataflux
			})

			bucketICopy := *tc.inputBucketI
			gotBucketI := &bucketICopy
			pv := testPV
			sc := testSC
			if tc.useBucketMetrics {
				if tc.scFallback {
					sc = testSCRequestBucketMetrics
				} else {
					pv = testPVRequestBucketMetrics
				}
			}

			err := defaultScanBucket(f.scanner, context.Background(), gotBucketI, time.Minute, pv, sc)

			if (err != nil) != tc.wantErr {
				t.Fatalf("defaultScanBucket() error = %v, wantErr %v", err, tc.wantErr)
			}

			if !tc.wantErr {
				if diff := cmp.Diff(*tc.wantBucketI, *gotBucketI, cmp.AllowUnexported(bucketInfo{})); diff != "" {
					t.Errorf("defaultScanBucket() info diff (-want +got):\n%q", diff)
				}
			}

			// Check if fakes were called as expected
			if tc.fakeMetrics.called != tc.expectMetricsCall {
				t.Errorf("scanBucketWithMetrics called = %v, want %v", tc.fakeMetrics.called, tc.expectMetricsCall)
			}

			if tc.fakeDataflux.called != tc.expectDatafluxCall {
				t.Errorf("scanBucketWithDataflux called = %v, want %v", tc.fakeDataflux.called, tc.expectDatafluxCall)
			}
		})
	}
}

func TestGetAnywhereCacheAdmissionPolicyFromPV(t *testing.T) {
	// Define test cases
	testCases := []struct {
		name           string
		pv             *v1.PersistentVolume
		expectedPolicy string
		expectError    bool
		expectedError  string
	}{
		{
			name: "should return 'admit-on-first-miss' when explicitly set",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "pv-test-1"},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeAttributes: map[string]string{
								"anywhereCacheAdmissionPolicy": "admit-on-first-miss",
							},
						},
					},
				},
			},
			expectedPolicy: "admit-on-first-miss",
			expectError:    false,
		},
		{
			name: "should return 'admit-on-second-miss' when explicitly set",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "pv-test-2"},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeAttributes: map[string]string{
								"anywhereCacheAdmissionPolicy": "admit-on-second-miss",
							},
						},
					},
				},
			},
			expectedPolicy: "admit-on-second-miss",
			expectError:    false,
		},
		{
			name: "should return default 'admit-on-first-miss' when attribute is not set",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "pv-test-3"},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeAttributes: map[string]string{}, // Empty attributes
						},
					},
				},
			},
			expectedPolicy: "admit-on-first-miss",
			expectError:    false,
		},
		{
			name: "should return an error for an invalid admission policy",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "pv-test-4"},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeAttributes: map[string]string{
								"anywhereCacheAdmissionPolicy": "invalid-policy",
							},
						},
					},
				},
			},
			expectedPolicy: "",
			expectError:    true,
			expectedError:  "invalid anywhere cache admission policy provided provided: invalid-policy, valid values are \"admit-on-first-miss\" or \"admit-on-second-miss\"",
		},
	}

	// Run tests
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := anywhereCacheAdmissionPolicyVal(tc.pv, nil)

			if tc.expectError {
				if err == nil {
					t.Errorf("Expected an error, but got nil")
				}
				if err != nil && err.Error() != tc.expectedError {
					t.Errorf("Expected error message '%s', but got '%s'", tc.expectedError, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, but got: %v", err)
				}
			}

			if policy != tc.expectedPolicy {
				t.Errorf("Expected policy '%s', but got '%s'", tc.expectedPolicy, policy)
			}
		})
	}
}

func TestGetAnywhereCacheTtlFromPV(t *testing.T) {
	testCases := []struct {
		name             string
		pv               *v1.PersistentVolume
		expectedDuration *durationpb.Duration
		expectError      bool
	}{
		{
			name: "should return correct duration when ttl is explicitly set",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "pv-test-1"},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeAttributes: map[string]string{
								"anywhereCacheTTL": "15m",
							},
						},
					},
				},
			},
			expectedDuration: durationpb.New(15 * time.Minute),
			expectError:      false,
		},
		{
			name: "should return default 1h duration when attribute is not set",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "pv-test-2"},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeAttributes: map[string]string{}, // Empty attributes
						},
					},
				},
			},
			expectedDuration: durationpb.New(1 * time.Hour),
			expectError:      false,
		},
		{
			name: "should return an error for an invalid duration string",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "pv-test-3"},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeAttributes: map[string]string{
								"anywhereCacheTTL": "invalid-duration",
							},
						},
					},
				},
			},
			expectedDuration: nil,
			expectError:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			duration, err := anywhereCacheTTLVal(tc.pv, nil)

			if tc.expectError {
				if err == nil {
					t.Errorf("Expected an error, but got nil")
				}
				return // End test here if error was expected
			}

			if err != nil {
				t.Errorf("Expected no error, but got: %v", err)
			}

			if duration.GetSeconds() != tc.expectedDuration.GetSeconds() || duration.GetNanos() != tc.expectedDuration.GetNanos() {
				t.Errorf("Expected duration %v, but got %v", tc.expectedDuration, duration)
			}
		})
	}
}

// ====================================================================================
// Anywherecache create testing: Mocks and Test Infrastructure
// ====================================================================================

// mockStorageControlClient is our fake storage client for tests.
type mockStorageControlClient struct {
	CreateAnywhereCacheFunc func(context.Context, *controlpb.CreateAnywhereCacheRequest, ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error)
	GetAnywhereCacheFunc    func(context.Context, *controlpb.GetAnywhereCacheRequest, ...gax.CallOption) (*controlpb.AnywhereCache, error)
	UpdateAnywhereCacheFunc func(context.Context, *controlpb.UpdateAnywhereCacheRequest, ...gax.CallOption) (*control.UpdateAnywhereCacheOperation, error)
}

func (m mockStorageControlClient) CreateAnywhereCache(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
	if m.CreateAnywhereCacheFunc != nil {
		return m.CreateAnywhereCacheFunc(ctx, req, opts...)
	}
	// Default success response if no specific behavior is set
	return &control.CreateAnywhereCacheOperation{}, nil
}

func (m mockStorageControlClient) GetAnywhereCache(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
	if m.GetAnywhereCacheFunc != nil {
		return m.GetAnywhereCacheFunc(ctx, req, opts...)
	}
	// Default success response if no specific behavior is set
	return &controlpb.AnywhereCache{}, nil
}

func (m mockStorageControlClient) UpdateAnywhereCache(ctx context.Context, req *controlpb.UpdateAnywhereCacheRequest, opts ...gax.CallOption) (*control.UpdateAnywhereCacheOperation, error) {
	if m.UpdateAnywhereCacheFunc != nil {
		return m.UpdateAnywhereCacheFunc(ctx, req, opts...)
	}
	// Default success response if no specific behavior is set
	return &control.UpdateAnywhereCacheOperation{}, nil
}

func (m mockStorageControlClient) Close() error {
	// No-op for the mock
	return nil
}

// ====================================================================================
// Anywherecache create testing: Tests
// ====================================================================================
func TestCreateAnywhereCache(t *testing.T) {
	// --- Shared Test Variables ---
	ctx := context.Background()
	validpv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			// Add annotations needed by the real buildCreateAnywhereCacheRequest
			Annotations: map[string]string{"gcs.csi.storage.gke.io/bucket-name": "my-bucket"},
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					VolumeHandle: "my-bucket",
				},
			},
		},
	}

	testCases := []struct {
		name                 string
		setupMocks           func()
		wantResults          map[string]*anywhereCacheSyncResult
		wantErr              bool
		pv                   *v1.PersistentVolume
		storageControlClient storageControlClient
	}{
		{
			name: "AnyC not found - create it - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-aib"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					err: errRequeueNeeded,
				},
				"zone-b": {
					err: errRequeueNeeded,
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					return &control.CreateAnywhereCacheOperation{}, nil
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return nil, status.Errorf(codes.NotFound, "failed")
				},
			},
		},
		{
			name: "AnyC not found - user provided zones - create it - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					err: errRequeueNeeded,
				},
				"zone-b": {
					err: errRequeueNeeded,
				},
			},
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					// Add annotations needed by the real buildCreateAnywhereCacheRequest
					Annotations: map[string]string{"gcs.csi.storage.gke.io/bucket-name": "my-bucket"},
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheZones": "zone-a,zone-b",
							},
						},
					},
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					return &control.CreateAnywhereCacheOperation{}, nil
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return nil, status.Errorf(codes.NotFound, "failed")
				},
			},
		},
		{
			name: "AnyC not found - user provided zones wildcard - create all - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					err: errRequeueNeeded,
				},
				"zone-b": {
					err: errRequeueNeeded,
				},
				"zone-c": {
					err: errRequeueNeeded,
				},
				"zone-d": {
					err: errRequeueNeeded,
				},
			},
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					// Add annotations needed by the real buildCreateAnywhereCacheRequest
					Annotations: map[string]string{"gcs.csi.storage.gke.io/bucket-name": "my-bucket"},
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheZones": "*",
							},
						},
					},
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					return &control.CreateAnywhereCacheOperation{}, nil
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return nil, status.Errorf(codes.NotFound, "failed")
				},
			},
		},
		{
			name: "AnyC not found - user provided zones empty string - create all - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					err: errRequeueNeeded,
				},
				"zone-b": {
					err: errRequeueNeeded,
				},
				"zone-c": {
					err: errRequeueNeeded,
				},
				"zone-d": {
					err: errRequeueNeeded,
				},
			},
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					// Add annotations needed by the real buildCreateAnywhereCacheRequest
					Annotations: map[string]string{"gcs.csi.storage.gke.io/bucket-name": "my-bucket"},
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheZones": "*",
							},
						},
					},
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					return &control.CreateAnywhereCacheOperation{}, nil
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return nil, status.Errorf(codes.NotFound, "failed")
				},
			},
		},
		{
			name: "AnyC not found - user provided zones none - create none - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{},
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					// Add annotations needed by the real buildCreateAnywhereCacheRequest
					Annotations: map[string]string{"gcs.csi.storage.gke.io/bucket-name": "my-bucket"},
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheZones": "none",
							},
						},
					},
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					return &control.CreateAnywhereCacheOperation{}, nil
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return nil, status.Errorf(codes.NotFound, "failed")
				},
			},
		},
		{
			name: "user provided zones not in available zones - create it - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantErr: true,
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					// Add annotations needed by the real buildCreateAnywhereCacheRequest
					Annotations: map[string]string{"gcs.csi.storage.gke.io/bucket-name": "my-bucket"},
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheZones": "zone-e,zone-f",
							},
						},
					},
				},
			},
			storageControlClient: mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					return &control.CreateAnywhereCacheOperation{}, nil
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return nil, status.Errorf(codes.NotFound, "failed")
				},
			},
		},
		{
			name: "user provided zones with inalid form - create it - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantErr: true,
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					// Add annotations needed by the real buildCreateAnywhereCacheRequest
					Annotations: map[string]string{"gcs.csi.storage.gke.io/bucket-name": "my-bucket"},
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheZones": "zone-e zone-f",
							},
						},
					},
				},
			},
			storageControlClient: mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					return &control.CreateAnywhereCacheOperation{}, nil
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return nil, status.Errorf(codes.NotFound, "failed")
				},
			},
		},
		{
			name: "AnyC found - state creating - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					state: "creating",
					err:   errRequeueNeeded,
				},
				"zone-b": {
					state: "creating",
					err:   errRequeueNeeded,
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return &controlpb.AnywhereCache{
						State: "creating",
					}, nil
				},
			},
		},
		{
			name: "AnyC found - state PAUSED - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					state: "paused",
					err:   nil,
				},
				"zone-b": {
					state: "paused",
					err:   nil,
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return &controlpb.AnywhereCache{
						State: "paused",
					}, nil
				},
			},
		},
		{
			name: "AnyC found - state disabled - return nil error",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					state: "disabled",
					err:   nil,
				},
				"zone-b": {
					state: "disabled",
					err:   nil,
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return &controlpb.AnywhereCache{
						State: "disabled",
					}, nil
				},
			},
		},
		{
			name: "AnyC found - state running - update in progress - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					state: "running",
					err:   errRequeueNeeded,
				},
				"zone-b": {
					state: "running",
					err:   errRequeueNeeded,
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return &controlpb.AnywhereCache{
						State:         "running",
						PendingUpdate: true,
					}, nil
				},
			},
		},
		{
			name: "AnyC found - state running - no update in progress - same params - success",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					state: "running",
					err:   nil,
				},
				"zone-b": {
					state: "running",
					err:   nil,
				},
			},
			wantErr: false,
			storageControlClient: mockStorageControlClient{
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return &controlpb.AnywhereCache{
						State:           "running",
						Ttl:             durationpb.New(1 * time.Hour),
						AdmissionPolicy: "admit-on-first-miss",
					}, nil
				},
			},
		},
		{
			name: "Failure - GetZonesForClusterRegion fails",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return nil, fmt.Errorf("mock region error")
				}
			},
			wantErr: true,
		},
		{
			name: "Failure - NoStorageControlClient fails",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a"}, nil
				}
			},
			wantErr: true,
		},
		{
			name: "Failure - Invalid TTL format",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a"}, nil
				}
			},
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					// Missing required annotation to trigger build error
					Annotations: map[string]string{},
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheTTL": "invalid-duration",
							},
						},
					},
				},
			},
			storageControlClient: &mockStorageControlClient{},
			wantErr:              true,
		},
		{
			name: "One zone fails API Failure - Should return error",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					err: fmt.Errorf("failed"),
				},
				"zone-b": {
					err: errRequeueNeeded,
				},
			},
			storageControlClient: &mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					if req.AnywhereCache.Zone != "zone-a" {
						return &control.CreateAnywhereCacheOperation{}, nil
					}
					return nil, status.Errorf(codes.Internal, "failed")
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return nil, status.Errorf(codes.NotFound, "failed")
				},
			},
		},
		{
			name: "One zone fails - already exists with different TTL - should update - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					state: "running",
					err:   errRequeueNeeded,
				},
				"zone-b": {
					state: "running",
					err:   errRequeueNeeded,
				},
			},
			wantErr: false,
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheTTL": "1h",
								"admission_policy": "admin-on-first-miss",
							},
						},
					},
				},
			},
			storageControlClient: &mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					if req.AnywhereCache.Zone != "zone-a" {
						return &control.CreateAnywhereCacheOperation{}, nil
					}
					return nil, status.Errorf(codes.AlreadyExists, "failed to build anywhere cache request: Already Exists")
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return &controlpb.AnywhereCache{
						Ttl:             durationpb.New(time.Duration(42)),
						AdmissionPolicy: "admit-on-first-miss",
						State:           "running",
					}, nil
				},
				UpdateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.UpdateAnywhereCacheRequest, opts ...gax.CallOption) (*control.UpdateAnywhereCacheOperation, error) {
					return &control.UpdateAnywhereCacheOperation{}, nil
				},
			},
		},
		{
			name: "One zone fails - already exists with different admission policy - should update - requeue needed",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(ctx context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{"zone-a", "zone-b", "zone-c", "zone-d"}, nil
				}
			},
			wantErr: false,
			wantResults: map[string]*anywhereCacheSyncResult{
				"zone-a": {
					state: "running",
					err:   errRequeueNeeded,
				},
				"zone-b": {
					state: "running",
					err:   errRequeueNeeded,
				},
			},
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
				Spec: v1.PersistentVolumeSpec{
					PersistentVolumeSource: v1.PersistentVolumeSource{
						CSI: &v1.CSIPersistentVolumeSource{
							VolumeHandle: "my-bucket",
							VolumeAttributes: map[string]string{
								"anywhereCacheTTL": "1h",
								"admission_policy": "admin-on-first-miss",
							},
						},
					},
				},
			},
			storageControlClient: &mockStorageControlClient{
				CreateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.CreateAnywhereCacheRequest, opts ...gax.CallOption) (*control.CreateAnywhereCacheOperation, error) {
					if req.AnywhereCache.Zone != "zone-a" {
						return &control.CreateAnywhereCacheOperation{}, nil
					}
					return nil, status.Errorf(codes.AlreadyExists, "failed to build anywhere cache request: Already Exists")
				},
				GetAnywhereCacheFunc: func(ctx context.Context, req *controlpb.GetAnywhereCacheRequest, opts ...gax.CallOption) (*controlpb.AnywhereCache, error) {
					return &controlpb.AnywhereCache{
						Ttl:             durationpb.New(1 * time.Hour),
						AdmissionPolicy: "admit-on-second-miss",
						State:           "running",
					}, nil
				},
				UpdateAnywhereCacheFunc: func(ctx context.Context, req *controlpb.UpdateAnywhereCacheRequest, opts ...gax.CallOption) (*control.UpdateAnywhereCacheOperation, error) {
					return &control.UpdateAnywhereCacheOperation{}, nil
				},
			},
		},
		{
			name: "Edge Case - No zones returned",
			setupMocks: func() {
				utilGetZonesForClusterLocation = func(_ context.Context, projNumber string, service *compute.Service, location string) ([]string, error) {
					return []string{}, nil
				}
			},
			storageControlClient: &mockStorageControlClient{},
			wantErr:              true,
		},
	}

	// --- Test Runner ---
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			klog.Infof("\n\nStarting test: %s", tc.name)
			if tc.pv == nil {
				tc.pv = validpv
			}

			fakeRecorder := record.NewFakeRecorder(10)
			tc.setupMocks()

			s := &Scanner{eventRecorder: fakeRecorder, config: &ScannerConfig{ClusterLocation: "us-central1", ProjectNumber: "123"}, storageControlClient: tc.storageControlClient}

			anywhereCacheProvidedZones, _ := anywhereCacheZonesVal(tc.pv, nil)
			gotResults, err := s.syncAnywhereCache(ctx, tc.pv, nil, anywhereCacheProvidedZones)

			if tc.wantErr != (err != nil) {
				t.Fatalf("wantErr: %t, err: %v", tc.wantErr, err)
			}

			for zone, wantResult := range tc.wantResults {
				gotResult, ok := gotResults[zone]
				if !ok {
					t.Fatalf("zone %q not found in gotResults", zone)
				}
				if wantResult.state != gotResult.state {
					t.Fatalf("zone %q want state: %q, got state: %q", zone, wantResult.state, gotResult.state)
				}
				if errors.Is(wantResult.err, errRequeueNeeded) != errors.Is(gotResult.err, errRequeueNeeded) {
					t.Fatalf("zone %q want requeueNeeded: %t, got requeueNeeded: %t", zone, errors.Is(wantResult.err, errRequeueNeeded), errors.Is(gotResult.err, errRequeueNeeded))
				}
			}
		})
	}
}
