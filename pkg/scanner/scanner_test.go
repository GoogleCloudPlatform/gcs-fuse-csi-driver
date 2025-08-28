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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testSCName            = "test-storage-class"
	testPVName            = "test-pv"
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
	scLister     storagelisters.StorageClassLister
	recorder     *record.FakeRecorder
	mockTimeImpl *mockTime
	scanBucketFn *fakeScanBucketFunc
	stopCh       chan struct{}
}

// newTestFixture initializes a new testFixture.
func newTestFixture(t *testing.T, initialObjects ...runtime.Object) *testFixture {
	t.Helper()

	kubeClient := fake.NewSimpleClientset(initialObjects...)
	factory := informers.NewSharedInformerFactory(kubeClient, 0 /* no resync period */)
	pvInformer := factory.Core().V1().PersistentVolumes()
	scInformer := factory.Storage().V1().StorageClasses()

	// Manually add initial objects to the informer indexers to ensure they are available in listers.
	for _, obj := range initialObjects {
		switch obj := obj.(type) {
		case *v1.PersistentVolume:
			if err := pvInformer.Informer().GetIndexer().Add(obj); err != nil {
				t.Fatalf("Failed to add PV to indexer: %v", err)
			}
		case *storagev1.StorageClass:
			if err := scInformer.Informer().GetIndexer().Add(obj); err != nil {
				t.Fatalf("Failed to add SC to indexer: %v", err)
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
		kubeClient:    kubeClient,
		pvLister:      pvInformer.Lister(),
		scLister:      scInformer.Lister(),
		pvSynced:      pvInformer.Informer().HasSynced,
		scSynced:      scInformer.Informer().HasSynced,
		factory:       factory,
		queue:         workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		eventRecorder: recorder,
		trackedPVs:    make(map[string]struct{}),
	}

	factory.Start(stopCh)
	if !cache.WaitForCacheSync(stopCh, s.pvSynced, s.scSynced) {
		t.Fatalf("Failed to sync caches")
	}

	return &testFixture{
		scanner:      s,
		kubeClient:   kubeClient,
		pvLister:     pvInformer.Lister(),
		scLister:     scInformer.Lister(),
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

// TestCheckPVRelevance tests the checkPVRelevance function.
// This function determines if a PersistentVolume is relevant for scanning based on its StorageClass,
// annotations, and other properties.
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

// TestProcessNextWorkItem tests the processNextWorkItem function.
// This function processes items from the work queue, triggering the scan and update logic.
func TestProcessNextWorkItem(t *testing.T) {
	pvName := testPVName
	scName := testSCName
	bucketName := testBucketName
	basePV := createPV(pvName, scName, bucketName, csiDriverName, nil, nil, nil)
	relevantSC := createStorageClass(scName, validSCParams)

	scanResult := &bucketInfo{
		name:           bucketName,
		numObjects:     1234,
		totalSizeBytes: 567890,
		isHNSEnabled:   true,
	}

	testCases := []struct {
		name           string
		initialPV      *v1.PersistentVolume
		scanInfo       *bucketInfo
		scanErr        error
		timeoutErr     bool
		patchErr       error
		expectRequeue  bool
		expectAnnotate bool
		expectedStatus string
	}{
		{
			name:           "Successful Sync",
			initialPV:      basePV.DeepCopy(),
			scanInfo:       scanResult,
			expectRequeue:  false,
			expectAnnotate: true,
			expectedStatus: scanCompleted,
		},
		{
			name:          "Sync Failure - Scan Error",
			initialPV:     basePV.DeepCopy(),
			scanErr:       fmt.Errorf("scan failed"),
			expectRequeue: true,
		},
		{
			name:           "Sync Failure - Scan Timeout",
			initialPV:      basePV.DeepCopy(),
			scanInfo:       scanResult,
			timeoutErr:     true,
			expectRequeue:  false,
			expectAnnotate: true,
			expectedStatus: scanTimeout,
		},
		{
			name:          "Sync Failure - Patch Error",
			initialPV:     basePV.DeepCopy(),
			scanInfo:      scanResult,
			patchErr:      fmt.Errorf("patch failed"),
			expectRequeue: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			objs = append(objs, relevantSC)
			if tc.initialPV != nil {
				objs = append(objs, tc.initialPV)
			}
			f := newTestFixture(t, objs...)

			f.scanBucketFn.info = tc.scanInfo
			if tc.timeoutErr {
				f.scanBucketFn.err = context.DeadlineExceeded
			} else {
				f.scanBucketFn.err = tc.scanErr
			}

			if tc.patchErr != nil {
				f.kubeClient.PrependReactor("patch", "persistentvolumes", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, tc.patchErr
				})
			}

			key, _ := cache.MetaNamespaceKeyFunc(tc.initialPV)
			f.scanner.enqueuePV(tc.initialPV)

			if !f.scanner.processNextWorkItem(context.Background()) {
				t.Errorf("processNextWorkItem returned false, expected true")
			}

			// Check if the item was requeued as expected.
			numRequeues := f.scanner.queue.NumRequeues(key)
			if tc.expectRequeue {
				if numRequeues == 0 {
					t.Errorf("Expected key %q to be requeued, got %d requeues", key, numRequeues)
				}
			} else {
				if numRequeues > 0 {
					t.Errorf("Expected key %q not to be requeued, got %d requeues", key, numRequeues)
				}
			}

			// Check if PV annotations were updated as expected.
			if tc.expectAnnotate {
				updatedPV, err := f.kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), pvName, metav1.GetOptions{})
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

	f := newTestFixture(t)
	f.scanner.queue.Add(key)
	f.scanner.trackedPVs[pvName] = struct{}{}
	f.scanner.deletePV(pv)

	// PV should no longer be in the tracked set.
	if _, exists := f.scanner.trackedPVs[pvName]; exists {
		t.Errorf("PV %s still tracked after deletePV() call", pvName)
	}

	// The item is still in the queue, deletePV doesn't remove it from the queue.
	if f.scanner.queue.Len() != 1 {
		t.Errorf("Queue length after deletePV(): got %d, want 1", f.scanner.queue.Len())
	}

	// Simulate the worker picking up the item.
	// processNextWorkItem will call syncPV.
	// Since the PV is not in the fake client, pvLister.Get() will return NotFound.
	if !f.scanner.processNextWorkItem(context.Background()) {
		t.Errorf("processNextWorkItem returned false unexpectedly")
	}

	// After processing, the queue should be empty and the item not re-queued
	// because the PV was not found, simulating a successful handle of the deletion.
	if f.scanner.queue.Len() != 0 {
		t.Errorf("Queue length after processNextWorkItem: got %d, want 0", f.scanner.queue.Len())
	}
	if f.scanner.queue.NumRequeues(key) > 0 {
		t.Errorf("Key %s was requeued unexpectedly, NumRequeues: %d", key, f.scanner.queue.NumRequeues(key))
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
