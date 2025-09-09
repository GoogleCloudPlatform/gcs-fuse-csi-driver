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
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

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
	onlyDirMountOptPrefix = "only-dir="
)

var (
	validSCParams = map[string]string{paramWorkloadTypeKey: paramWorkloadTypeInferenceKey}
	validSC       = createStorageClass(testSCName, validSCParams)
	podLabels     = map[string]string{profileManagedLabelKey: profileManagedLabelValue}
	scanResult    = &bucketInfo{
		name:           testBucketName,
		numObjects:     1234,
		totalSizeBytes: 567890,
		isHNSEnabled:   true,
	}
)

// mockTime allows controlling the time in tests.
// This is useful for testing time-dependent logic, such as TTLs.
type mockTime struct {
	currentTime time.Time
}

// Now returns the current mocked time.
func (m *mockTime) Now() time.Time {
	return m.currentTime
}

// fakeScanBucketFunc provides a mock implementation of the scanBucket function.
// It allows simulating different scan outcomes, including errors and timeouts.
type fakeScanBucketFunc struct {
	info *bucketInfo
	err  error
}

// Scan is the mock implementation of the scanBucket function.
// It checks for context cancellation (like timeouts) before returning the predefined info and error.
func (f *fakeScanBucketFunc) Scan(ctx context.Context, bucketInfo *bucketInfo, scanTimeout time.Duration) (*bucketInfo, error) {
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return f.info, context.DeadlineExceeded
		}
		return nil, ctx.Err()
	default:
		return f.info, f.err
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
	scanBucketFn *fakeScanBucketFunc
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

	fsb := &fakeScanBucketFunc{}
	origScanBucket := scanBucket
	scanBucket = fsb.Scan
	stopCh := make(chan struct{})

	t.Cleanup(func() {
		timeNow = origTimeNow
		scanBucket = origScanBucket
		close(stopCh)
	})

	// Create the Scanner instance with fake/mock components.
	s := &Scanner{
		kubeClient:         kubeClient,
		pvLister:           pvInformer.Lister(),
		pvcLister:          pvcInformer.Lister(),
		scLister:           scInformer.Lister(),
		podLister:          podInformer.Lister(),
		pvSynced:           pvInformer.Informer().HasSynced,
		pvcSynced:          pvcInformer.Informer().HasSynced,
		scSynced:           scInformer.Informer().HasSynced,
		podSynced:          podInformer.Informer().HasSynced,
		factory:            factory,
		podFactory:         podFactory,
		queue:              workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		eventRecorder:      recorder,
		trackedPVs:         make(map[string]struct{}),
		lastSuccessfulScan: make(map[string]time.Time),
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
		ObjectMeta:  metav1.ObjectMeta{Name: name},
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
	ttl := defaultScanTTLDuration
	lastUpdateTimeWithinTTL := now.Add(-ttl / 2).Format(time.RFC3339)
	lastUpdateTimeOutsideTTL := now.Add(-ttl * 2).Format(time.RFC3339)

	testCases := []struct {
		name         string
		pv           *v1.PersistentVolume
		scs          []*storagev1.StorageClass
		wantRelevant bool
		wantErr      bool
		wantBucket   string
		wantDir      string
	}{
		{
			name:         "Relevant PV",
			pv:           createPV(testPVName, testSCName, testBucketName, csiDriverName, []string{onlyDirMountOptPrefix + testDirName}, nil, nil),
			scs:          []*storagev1.StorageClass{validSC},
			wantRelevant: true,
			wantBucket:   testBucketName,
			wantDir:      testDirName,
		},
		{
			name: "Relevant PV - Training",
			pv:   createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{paramWorkloadTypeKey: paramWorkloadTypeTrainingKey}),
			},
			wantRelevant: true,
			wantBucket:   testBucketName,
		},
		{
			name: "Relevant PV - Inference",
			pv:   createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{paramWorkloadTypeKey: paramWorkloadTypeInferenceKey}),
			},
			wantRelevant: true,
			wantBucket:   testBucketName,
		},
		{
			name: "Relevant PV - Checkpointing",
			pv:   createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{paramWorkloadTypeKey: paramWorkloadTypeCheckpointingKey}),
			},
			wantRelevant: true,
			wantBucket:   testBucketName,
		},
		{
			name: "Irrelevant - Wrong Driver",
			pv:   createPV(testPVName, testSCName, testBucketName, "blah", nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{paramWorkloadTypeKey: paramWorkloadTypeCheckpointingKey}),
			},
			wantRelevant: false,
		},
		{
			name:         "Irrelevant - SC Not Found",
			pv:           createPV(testPVName, "blah", testBucketName, csiDriverName, nil, nil, nil),
			scs:          []*storagev1.StorageClass{validSC},
			wantRelevant: false,
		},
		{
			name: "Error - Invalid Workload Type",
			pv:   createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, nil),
			scs: []*storagev1.StorageClass{
				createStorageClass(testSCName, map[string]string{paramWorkloadTypeKey: "blah"}),
			},
			wantErr: false,
		},
		{
			name: "Irrelevant - Within TTL",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				annotationLastUpdatedTime: lastUpdateTimeWithinTTL,
			}, nil),
			scs:          []*storagev1.StorageClass{validSC},
			wantRelevant: false,
		},
		{
			name: "Relevant - Outside TTL",
			pv: createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, map[string]string{
				annotationLastUpdatedTime: lastUpdateTimeOutsideTTL,
			}, nil),
			scs:          []*storagev1.StorageClass{validSC},
			wantRelevant: true,
			wantBucket:   testBucketName,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			if tc.pv != nil {
				objs = append(objs, tc.pv)
			}
			for _, sc := range tc.scs {
				objs = append(objs, sc)
			}
			f := newTestFixture(t, objs...)
			f.mockTimeImpl.currentTime = now
			info, err := f.scanner.checkPVRelevance(tc.pv)

			if tc.wantErr {
				if err == nil {
					t.Errorf("checkPVRelevance(%v) = %v, nil, want error", tc.pv.Name, info)
				}
			} else {
				if err != nil {
					t.Errorf("checkPVRelevance(%v) = %v, %v, want no error", tc.pv.Name, info, err)
				}
			}

			isRelevant := info != nil
			if isRelevant != tc.wantRelevant {
				t.Errorf("checkPVRelevance(%v) relevant = %v, want %v", tc.pv.Name, isRelevant, tc.wantRelevant)
			}

			if tc.wantRelevant && info != nil {
				if info.name != tc.wantBucket {
					t.Errorf("checkPVRelevance(%v) bucket = %q, want %q", tc.pv.Name, info.name, tc.wantBucket)
				}
				if info.dir != tc.wantDir {
					t.Errorf("checkPVRelevance(%v) dir = %q, want %q", tc.pv.Name, info.dir, tc.wantDir)
				}
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
	pvIrrelevantSC := createPV("irrelevant-pv", "irrelevant-sc", bucketName, csiDriverName, nil, nil, nil)

	testCases := []struct {
		name           string
		key            string
		initialObjects []runtime.Object
		scanInfo       *bucketInfo
		scanErr        error
		patchErr       error
		wantErr        bool
		expectAnnotate bool
		expectedStatus string
	}{
		{
			name:           "Successful Sync",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			scanInfo:       scanResult,
			wantErr:        false,
			expectAnnotate: true,
			expectedStatus: scanCompleted,
		},
		{
			name:           "Scan Error",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			scanErr:        fmt.Errorf("scan failed"),
			wantErr:        true,
		},
		{
			name:           "Scan Timeout",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			scanInfo:       scanResult,
			scanErr:        context.DeadlineExceeded,
			wantErr:        false,
			expectAnnotate: true,
			expectedStatus: scanTimeout,
		},
		{
			name:           "Patch Error",
			key:            pvName,
			initialObjects: []runtime.Object{basePV.DeepCopy(), relevantSC},
			scanInfo:       scanResult,
			patchErr:       fmt.Errorf("patch failed"),
			wantErr:        true,
		},
		{
			name:           "PV Not Found",
			key:            pvName,
			initialObjects: []runtime.Object{relevantSC},
			wantErr:        false, // Not found is not an error for syncPV, just stops processing.
		},
		{
			name:           "PV Not Relevant",
			key:            "irrelevant-pv",
			initialObjects: []runtime.Object{pvIrrelevantSC, irrelevantSC},
			wantErr:        false,
			expectAnnotate: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFixture(t, tc.initialObjects...)

			f.scanBucketFn.info = tc.scanInfo
			f.scanBucketFn.err = tc.scanErr

			if tc.patchErr != nil {
				f.kubeClient.PrependReactor("patch", "persistentvolumes", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, tc.patchErr
				})
			}

			err := f.scanner.syncPV(context.Background(), tc.key)

			if tc.wantErr != (err != nil) {
				t.Errorf("syncPV(%s) returned error: %v, wantErr: %v", tc.key, err, tc.wantErr)
			}

			if tc.expectAnnotate {
				updatedPV, err := f.kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), tc.key, metav1.GetOptions{})
				if err != nil {
					t.Fatalf("Failed to get PV: %v", err)
				}
				if updatedPV.Annotations == nil {
					t.Errorf("Expected annotations to be set")
				}
				if status := updatedPV.Annotations[annotationStatus]; status != tc.expectedStatus {
					t.Errorf("Annotation %s: got %v, want %v", annotationStatus, status, tc.expectedStatus)
				}
				if _, exists := updatedPV.Annotations[annotationLastUpdatedTime]; !exists {
					t.Errorf("Annotation %s not found", annotationLastUpdatedTime)
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
	pvcBoundNotRelevant := createPVC("pvc-not-relevant", testNamespace, "pv-not-relevant", "sc-not-relevant")
	pvcUnbound := createPVC("pvc-unbound", testNamespace, "", testSCName)

	volRelevant := v1.Volume{Name: "vol1", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: testPVCName}}}
	volNotRelevant := v1.Volume{Name: "vol2", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: "pvc-not-relevant"}}}
	volUnbound := v1.Volume{Name: "vol3", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: "pvc-unbound"}}}
	volMissingPVC := v1.Volume{Name: "vol4", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: "pvc-missing"}}}

	testCases := []struct {
		name              string
		pod               *v1.Pod
		initialObjects    []runtime.Object
		expectRequeueErr  bool
		wantErr           bool
		expectGateRemoved bool
		expectPVEnqueued  string
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
			expectPVEnqueued: testPVName,
		},
		{
			name:              "Pod with irrelevant PV, gate removed",
			pod:               createPod(testPodName, testNamespace, []v1.Volume{volNotRelevant}, podLabels, true),
			initialObjects:    []runtime.Object{pvNotRelevant, scNotRelevant, pvcBoundNotRelevant},
			expectGateRemoved: true,
		},
		{
			name:             "Pod with mixed PVs (relevant and not), PV enqueued, Pod requeued",
			pod:              createPod(testPodName, testNamespace, []v1.Volume{volNotRelevant, volRelevant}, podLabels, true),
			initialObjects:   []runtime.Object{pvRelevant, scValid, pvcBoundRelevant, pvNotRelevant, scNotRelevant, pvcBoundNotRelevant},
			expectRequeueErr: true,
			expectPVEnqueued: testPVName,
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
			name:             "PVC found, but PV not found (error)",
			pod:              createPod(testPodName, testNamespace, []v1.Volume{volRelevant}, podLabels, true),
			initialObjects:   []runtime.Object{pvcBoundRelevant},
			wantErr:          true,
			expectRequeueErr: false,
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
			err := f.scanner.syncPod(context.Background(), key)

			hasErr := err != nil
			if hasErr != (tc.wantErr || tc.expectRequeueErr) {
				t.Errorf("syncPod(%s) returned error: %v, wantErr: %v, wantRequeueErr: %v", key, err, tc.wantErr, tc.expectRequeueErr)
			}

			if err != nil {
				isRequeueErr := errors.Is(err, errRequeuePod)
				if isRequeueErr != tc.expectRequeueErr {
					t.Errorf("syncPod(%s) error type: got requeue %v, want requeue %v (err: %v)", key, isRequeueErr, tc.expectRequeueErr, err)
				}
			}

			if tc.expectPVEnqueued != "" {
				pvKey := pvPrefix + tc.expectPVEnqueued
				found := false
				for i := 0; i < f.scanner.queue.Len(); i++ {
					item, _ := f.scanner.queue.Get()
					if item == pvKey {
						found = true
					}
					f.scanner.queue.Done(item)
				}
				if !found {
					t.Errorf("Expected PV %s to be enqueued, but not found in queue", tc.expectPVEnqueued)
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
					t.Errorf("Scheduling gate %s was not removed from Pod %s", schedulingGateName, key)
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

	testCases := []struct {
		name              string
		keyToAdd          string
		initialObjects    []runtime.Object
		scanInfo          *bucketInfo // For PV sync
		scanErr           error       // For PV sync
		patchErr          error       // For PV or Pod sync
		expectRequeue     bool
		expectAnnotate    bool   // For PV sync
		expectedStatus    string // For PV sync
		expectGateRemoved bool   // For Pod sync
	}{
		// PV Key Tests
		{
			name:           "PV Sync Successful",
			keyToAdd:       pvQueueKey,
			initialObjects: []runtime.Object{pvRelevant, scValid},
			scanInfo:       scanResult,
			expectRequeue:  false,
			expectAnnotate: true,
			expectedStatus: scanCompleted,
		},
		{
			name:           "PV Sync Scan Error",
			keyToAdd:       pvQueueKey,
			initialObjects: []runtime.Object{pvRelevant, scValid},
			scanErr:        fmt.Errorf("scan failed"),
			expectRequeue:  true,
		},
		{
			name:           "PV Sync Patch Error",
			keyToAdd:       pvQueueKey,
			initialObjects: []runtime.Object{pvRelevant, scValid},
			scanInfo:       scanResult,
			patchErr:       fmt.Errorf("patch failed"),
			expectRequeue:  true,
		},
		// Pod Key Tests
		{
			name:              "Pod Sync Successful - Gate Removed",
			keyToAdd:          podQueueKey,
			initialObjects:    []runtime.Object{createPod(podName, namespace, nil, podLabels, true)}, // No volumes, gate should be removed
			expectRequeue:     false,
			expectGateRemoved: true,
		},
		{
			name:           "Pod Sync Requeue - Relevant PV",
			keyToAdd:       podQueueKey,
			initialObjects: []runtime.Object{podRelevant, pvcForPod, pvRelevant, scValid},
			expectRequeue:  true, // Pod requeued because PV needs scan
		},
		{
			name:          "Unknown Key Prefix",
			keyToAdd:      "unknown/" + pvKey,
			expectRequeue: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFixture(t, tc.initialObjects...)

			// Setup for PV sync parts
			f.scanBucketFn.info = tc.scanInfo
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
				if status := updatedPV.Annotations[annotationStatus]; status != tc.expectedStatus {
					t.Errorf("Annotation %s: got %v, want %v", annotationStatus, status, tc.expectedStatus)
				}
			}

			// Check Pod gate removal if expected
			if tc.expectGateRemoved {
				updatedPod, err := f.kubeClient.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
				if err != nil {
					if apierrors.IsNotFound(err) {
						t.Fatalf("Pod %s not found, but expected to exist for gate check", podName)
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
					t.Errorf("Scheduling gate %s was not removed from Pod %s", schedulingGateName, podName)
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
	key, _ := cache.MetaNamespaceKeyFunc(pvRelevant)
	if _, exists := f.scanner.trackedPVs[key]; !exists {
		t.Errorf("PV %s not tracked after add", key)
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
	f.scanner.trackedPVs[pvName] = struct{}{}
	f.scanner.lastSuccessfulScan[pvName] = time.Now()

	f.scanner.deletePV(pv)

	// PV should no longer be in the tracked set.
	if _, exists := f.scanner.trackedPVs[pvName]; exists {
		t.Errorf("PV %s still tracked after deletePV() call", pvName)
	}
	if _, exists := f.scanner.lastSuccessfulScan[pvName]; exists {
		t.Errorf("PV %s still in lastSuccessfulScan map after deletePV() call", pvName)
	}

	// Check if the key was forgotten
	if f.scanner.queue.NumRequeues(queueKey) != 0 {
		t.Errorf("NumRequeues for %s: got %d, want 0 after deletePV/Forget", key, f.scanner.queue.NumRequeues(queueKey))
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
		t.Errorf("NumRequeues for %s: got %d, want 0 after deletePod/Forget", key, f.scanner.queue.NumRequeues(queueKey))
	}
}

// TestGetScanTimeout tests the getDurationAttribute function.
func TestGetDurationAttribute(t *testing.T) {
	customScanTimeoutDuration := time.Duration(42 * time.Minute)
	customScanTTLDuration := time.Duration(5 * time.Minute)
	testCases := []struct {
		name            string
		attributeKey    string
		attributes      map[string]string
		defaultDuration time.Duration
		wantTimeout     *time.Duration
		wantErr         bool
	}{
		{
			name:            "No bucket scan timeout attribute - Use default",
			attributeKey:    volumeAttributeScanTimeoutKey,
			defaultDuration: defaultScanTimeoutDuration,
			wantTimeout:     &defaultScanTimeoutDuration,
			wantErr:         false,
		},
		{
			name:            "No bucket scan TTL attribute - Use default",
			attributeKey:    volumeAttributeScanTTLKey,
			defaultDuration: defaultScanTTLDuration,
			wantTimeout:     &defaultScanTTLDuration,
			wantErr:         false,
		},
		{
			name:            "Valid bucket scan timeout attribute - Override",
			attributes:      map[string]string{volumeAttributeScanTimeoutKey: "42m"},
			defaultDuration: defaultScanTimeoutDuration,
			attributeKey:    volumeAttributeScanTimeoutKey,
			wantTimeout:     &customScanTimeoutDuration,
			wantErr:         false,
		},
		{
			name:            "Valid bucket scan TTL attribute - Override",
			attributes:      map[string]string{volumeAttributeScanTTLKey: "5m"},
			defaultDuration: defaultScanTTLDuration,
			attributeKey:    volumeAttributeScanTTLKey,
			wantTimeout:     &customScanTTLDuration,
			wantErr:         false,
		},
		{
			name:            "Invalid duration - Error",
			attributes:      map[string]string{volumeAttributeScanTimeoutKey: "5min"},
			attributeKey:    volumeAttributeScanTimeoutKey,
			defaultDuration: defaultScanTimeoutDuration,
			wantErr:         true,
		},
		{
			name:            "Non-positive duration - Error",
			attributes:      map[string]string{volumeAttributeScanTimeoutKey: "0s"},
			attributeKey:    volumeAttributeScanTimeoutKey,
			defaultDuration: defaultScanTimeoutDuration,
			wantErr:         true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pv := createPV(testPVName, testSCName, testBucketName, csiDriverName, nil, nil, tc.attributes)
			f := newTestFixture(t)
			timeout, err := f.scanner.getDurationAttribute(pv, tc.attributeKey, tc.defaultDuration)

			if tc.wantErr {
				if err == nil {
					t.Errorf("getDurationAttribute() error = %v, wantErr %v", err, tc.wantErr)
				}
			} else {
				if *timeout != *tc.wantTimeout {
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
	f.mockTimeImpl.currentTime = now

	info := &bucketInfo{
		numObjects:     999,
		totalSizeBytes: 8888,
		isHNSEnabled:   true,
	}
	status := scanCompleted

	// Call the function to update annotations.
	if err := f.scanner.updatePVScanResult(context.Background(), pv, info, status); err != nil {
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
		annotationStatus:          status,
		annotationNumObjects:      "999",
		annotationTotalSize:       "8888",
		annotationHNSEnabled:      "true",
		annotationLastUpdatedTime: now.UTC().Format(time.RFC3339),
	}

	// Verify each expected annotation.
	for key, want := range expectedAnnotations {
		got := annotations[key]
		if got != want {
			t.Errorf("Annotation %s: got %v, want %v", key, got, want)
		}
	}

	// Verify in-memory map is updated
	if lastScan, ok := f.scanner.lastSuccessfulScan[testPVName]; !ok || !lastScan.Equal(now) {
		t.Errorf("lastSuccessfulScan map not updated correctly: got %v, want %v", lastScan, now)
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
			name:  "Valid mount option with dir name",
			input: onlyDirMountOptPrefix + testDirName,
			want:  testDirName,
			ok:    true,
		},
		{
			name:  "Valid mount option without dir name",
			input: onlyDirMountOptPrefix,
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
		got, ok := getOnlyDirValue(tc.input)
		if ok != tc.ok || got != tc.want {
			t.Errorf("getOnlyDirValue(%q) = %q, %v, want %q, %v", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}
