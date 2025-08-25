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
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

// Helper to create a basic PV for tests
func createTestPV(name string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

// newTestScanner creates a Scanner instance with a fake Kubernetes client for testing.
func newTestScanner(t *testing.T) *Scanner {
	t.Helper()
	client := fake.NewSimpleClientset()
	factory := informers.NewSharedInformerFactory(client, 0)
	pvInformer := factory.Core().V1().PersistentVolumes()
	scInformer := factory.Storage().V1().StorageClasses()

	recorder := record.NewFakeRecorder(10) // Buffer size 10

	scanner := &Scanner{
		kubeClient:    client,
		factory:       factory,
		pvLister:      pvInformer.Lister(),
		scLister:      scInformer.Lister(),
		pvSynced:      func() bool { return true }, // Assume synced for tests
		scSynced:      func() bool { return true }, // Assume synced for tests
		queue:         workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		eventRecorder: recorder,
	}
	return scanner
}

func TestEnqueuePV(t *testing.T) {
	s := newTestScanner(t)
	pv := createTestPV("test-pv")
	s.enqueuePV(pv)

	if s.queue.Len() != 1 {
		t.Errorf("queue.Len() = %d, want 1", s.queue.Len())
	}

	key, _ := cache.MetaNamespaceKeyFunc(pv)
	item, quit := s.queue.Get()
	if quit {
		t.Errorf("s.queue.Get() quit = true, want false")
	}
	if item != key {
		t.Errorf("s.queue.Get() item = %v, want %v", item, key)
	}
	s.queue.Done(item)
}

func TestAddPV(t *testing.T) {
	s := newTestScanner(t)
	pv := createTestPV("add-pv")
	s.addPV(pv)
	if s.queue.Len() != 1 {
		t.Errorf("queue.Len() after addPV = %d, want 1", s.queue.Len())
	}
}

func TestUpdatePV(t *testing.T) {
	s := newTestScanner(t)
	oldPV := createTestPV("update-pv")
	newPV := oldPV.DeepCopy()
	newPV.ResourceVersion = "2"
	s.updatePV(oldPV, newPV)
	if s.queue.Len() != 1 {
		t.Errorf("queue.Len() after updatePV = %d, want 1", s.queue.Len())
	}

	// Test no enqueue if resource version is same
	s.updatePV(newPV, newPV)
	if s.queue.Len() != 1 {
		t.Errorf("queue.Len() after no-op updatePV = %d, want 1", s.queue.Len())
	}
}

func TestDeletePV(t *testing.T) {
	s := newTestScanner(t)
	pv := createTestPV("delete-pv")
	s.deletePV(pv)
	if s.queue.Len() != 1 {
		t.Errorf("queue.Len() after deletePV = %d, want 1", s.queue.Len())
	}
}
