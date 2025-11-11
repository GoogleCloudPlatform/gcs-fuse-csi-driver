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

type FakeMetricsManager struct{}

func (*FakeMetricsManager) InitializeHTTPHandler() {}

func (*FakeMetricsManager) RegisterMetricsCollector(_, _, _, _ string) {}

func (*FakeMetricsManager) UnregisterMetricsCollector(_ string) {}

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
