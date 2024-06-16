/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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

package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestTokenSourceStore(t *testing.T) {
	t.Parallel()

	ttl := 100 * time.Millisecond
	cleanupFreq := 50 * time.Millisecond
	store := NewTokenSourceStore(ttl, cleanupFreq)

	// Test Set and Get
	store.Set("ns1", "name1", &GCPTokenSource{
		k8sSATokenStr: "token1",
	})
	value := store.Get("ns1", "name1")
	if value == nil || value.k8sSATokenStr != "token1" {
		t.Errorf("Get failed: expected token1, got %v", value.k8sSATokenStr)
	}

	// Test Expiration
	time.Sleep(cleanupFreq + ttl) // Wait for item to expire
	if store.Get("ns1", "name1") != nil {
		t.Errorf("Item should have expired but was still found")
	}

	// Test Last Access Update
	store.Set("ns2", "name2", &GCPTokenSource{
		k8sSATokenStr: "token2",
	})
	time.Sleep(ttl / 2)
	value = store.Get("ns2", "name2") // Access to update last access time
	time.Sleep(ttl / 2)
	if value == nil || value.k8sSATokenStr != "token2" {
		t.Errorf("Get failed: expected token2, got %v", value)
	}

	time.Sleep(cleanupFreq + ttl) // Wait for item to expire
	if store.Get("ns2", "name2") != nil {
		t.Errorf("Item should have expired but was still found")
	}

	// Test Concurrent Access
	numRoutines := 10
	var wg sync.WaitGroup
	wg.Add(numRoutines)
	for i := 0; i < numRoutines; i++ {
		go func(i int) {
			defer wg.Done()
			store.Set(fmt.Sprintf("ns%d", i), fmt.Sprintf("name%d", i), &GCPTokenSource{
				k8sSATokenStr: fmt.Sprintf("token%d", i),
			})
			value := store.Get(fmt.Sprintf("ns%d", i), fmt.Sprintf("name%d", i))
			if value == nil || value.k8sSATokenStr != fmt.Sprintf("token%d", i) {
				t.Errorf("Concurrent Get failed for ns%d, name%d", i, i)
			}
		}(i)
	}
	wg.Wait()
}
