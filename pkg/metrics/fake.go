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

package metrics

import "sync"

// FakeMetricsManager is a fake implementation of the MetricsManager interface for testing.
type FakeMetricsManager struct {
	collectors map[string]string
	mu         sync.Mutex
}

// NewFakeMetricsManager returns a new FakeMetricsManager.
func NewFakeMetricsManager() *FakeMetricsManager {
	return &FakeMetricsManager{
		collectors: make(map[string]string),
	}
}

// InitializeHTTPHandler is a no-op.
func (*FakeMetricsManager) InitializeHTTPHandler() {}

// RegisterMetricsCollector records the registration of a metrics collector.
func (f *FakeMetricsManager) RegisterMetricsCollector(targetPath, _, _, bucketName, nodeName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.collectors[targetPath] = bucketName
}

// UnregisterMetricsCollector records the unregistration of a metrics collector.
func (f *FakeMetricsManager) UnregisterMetricsCollector(targetPath, nodeName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.collectors, targetPath)
}

// GetCollectors returns the map of registered collectors.
func (f *FakeMetricsManager) GetCollectors() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Create a new map with the same capacity as the original
	cp := make(map[string]string, len(f.collectors))

	// Copy all key-value pairs into the new map
	for k, v := range f.collectors {
		cp[k] = v
	}

	return cp
}

type FakePrometheusMetricManager struct {
	syncPVCounter  map[string]int
	syncPodCounter map[string]int
}

func (*FakePrometheusMetricManager) InitializeHTTPHandler() {}

func (m *FakePrometheusMetricManager) RecordSyncPVMetric(err error) {
	m.syncPVCounter[GetErrorCode(err)]++
}

func (m *FakePrometheusMetricManager) RecordSyncPodMetric(err error) {
	m.syncPodCounter[GetErrorCode(err)]++
}

func NewFakePrometheusMetricsManager() PrometheusMetricManager {
	return &FakePrometheusMetricManager{
		syncPVCounter:  make(map[string]int),
		syncPodCounter: make(map[string]int),
	}
}
