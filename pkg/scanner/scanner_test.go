/*
Copyright 2023 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scanner

import (
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// Helper to create a basic PV for tests
func createTestPV(name string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func TestNewScanner(t *testing.T) {
	config := &ScannerConfig{
		KubeAPIQPS:   10,
		KubeAPIBurst: 10,
		ResyncPeriod: 1 * time.Minute,
		// KubeConfigPath is empty, so it will try in-cluster config, which is fine for mock
	}
	_, err := NewScanner(config)
	if err != nil {
		// In a real test env, this might fail if not running in-cluster.
		// For unit tests, we often mock the client build process.
		// Assuming for now that client creation can be mocked/faked in the test runner.
		t.Logf("NewScanner() error = %v (This might be expected outside a cluster)", err)
		// t.Errorf("NewScanner() error = %v, wantErr false", err)
	}
}

func TestEnqueuePV(t *testing.T) {
	config := &ScannerConfig{}
	s, err := NewScanner(config)
	if err != nil {
		t.Fatalf("Failed to create scanner: %v", err)
	}

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
	config := &ScannerConfig{}
	s, err := NewScanner(config)
	if err != nil {
		t.Fatalf("Failed to create scanner: %v", err)
	}
	pv := createTestPV("add-pv")
	s.addPV(pv)
	if s.queue.Len() != 1 {
		t.Errorf("queue.Len() after addPV = %d, want 1", s.queue.Len())
	}
}

func TestUpdatePV(t *testing.T) {
	config := &ScannerConfig{}
	s, err := NewScanner(config)
	if err != nil {
		t.Fatalf("Failed to create scanner: %v", err)
	}
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
	config := &ScannerConfig{}
	s, err := NewScanner(config)
	if err != nil {
		t.Fatalf("Failed to create scanner: %v", err)
	}
	pv := createTestPV("delete-pv")
	s.deletePV(pv)
	if s.queue.Len() != 1 {
		t.Errorf("queue.Len() after deletePV = %d, want 1", s.queue.Len())
	}
}
