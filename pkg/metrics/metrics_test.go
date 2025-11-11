/*
Copyright 2025 Google LLC

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

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewMetricsManager(t *testing.T) {
	t.Run("test the metrics manager creation", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name          string
			endpoint      string
			socketDir     string
			maxCollectors int
		}{
			{
				name:          "basic options test",
				endpoint:      "/test/metrics",
				socketDir:     "/tmp/test-sockets",
				maxCollectors: 5,
			},
			{
				name:          "no collectors test",
				endpoint:      ":9920",
				socketDir:     "/gcsfuse-tmp/socket",
				maxCollectors: 0,
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)

			clientset := clientset.NewFakeClientset()
			manager := NewMetricsManager(tc.endpoint, tc.socketDir, tc.maxCollectors, clientset).(*manager)

			if manager.metricsEndpoint != tc.endpoint {
				t.Errorf("NewMetricsManager did not set metricsEndpoint correctly. Got %q, want %q", manager.metricsEndpoint, tc.endpoint)
			}
			if manager.fuseSocketDir != tc.socketDir {
				t.Errorf("NewMetricsManager did not set fuseSocketDir correctly. Got %q, want %q", manager.fuseSocketDir, tc.socketDir)
			}
			if manager.maximumNumberOfCollectors != tc.maxCollectors {
				t.Errorf("NewMetricsManager did not set maximumNumberOfCollectors correctly. Got %d, want %d", manager.maximumNumberOfCollectors, tc.maxCollectors)
			}
			if manager.registry == nil {
				t.Errorf("NewMetricsManager did not initialize registry")
			}
			if manager.volumePublishPathRegistered == nil {
				t.Errorf("NewMetricsManager did not initialize volumePublishPathRegistered")
			}
		}
	})
}

func TestPrometheusCounters(t *testing.T) {
	pmm := NewPrometheusMetricManager(":8080", http.NewServeMux())

	tests := []struct {
		name             string
		recordMetricFn   func()
		validateMetricFn func()
	}{
		{
			name: "sync pv count returns error - should record error with status code",
			recordMetricFn: func() {
				pmm.RecordSyncPVMetric(status.Errorf(codes.Internal, "test error"))
			},
			validateMetricFn: func() {
				validatePrometheusMetric(t, pmm.syncPVCounter.WithLabelValues(codes.Internal.String()), 1)
			},
		},
		{
			name: "sync pv count returns nil - should record success with OK status code",
			recordMetricFn: func() {
				pmm.RecordSyncPVMetric(nil)
			},
			validateMetricFn: func() {
				validatePrometheusMetric(t, pmm.syncPVCounter.WithLabelValues(codes.OK.String()), 1)
			},
		},
		{
			name: "sync pod count - should record error with status code",
			recordMetricFn: func() {
				pmm.RecordSyncPodMetric(status.Errorf(codes.Aborted, "test error"))
			},
			validateMetricFn: func() {
				validatePrometheusMetric(t, pmm.syncPodCounter.WithLabelValues(codes.Aborted.String()), 1)
			},
		},
		{
			name: "sync pod count returns nil - should record success with OK status code",
			recordMetricFn: func() {
				pmm.RecordSyncPodMetric(nil)
			},
			validateMetricFn: func() {
				validatePrometheusMetric(t, pmm.syncPodCounter.WithLabelValues(codes.OK.String()), 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetPrometheusMetrics(pmm)
			test.recordMetricFn()
			test.validateMetricFn()
		})
	}
}

func resetPrometheusMetrics(pmm *prometheusMetricManager) {
	pmm.syncPVCounter.Reset()
	pmm.syncPodCounter.Reset()
}

// getCountFromPrometheusCounterMetric gets the count value from a Prometheus counter metric
func getCountFromPrometheusCounterMetric(metric prometheus.Counter) (int, error) {
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(metric); err != nil {
		return 0, err
	}
	metrics, err := reg.Gather()
	if err != nil {
		return 0, err
	}

	// Should only be 1 metrics family with 1 metric.
	if len(metrics) != 1 || len(metrics[0].Metric) != 1 || metrics[0].Metric[0].Counter == nil || metrics[0].Metric[0].Counter.Value == nil {
		return 0, fmt.Errorf("Unexpected metrics state = %v", metrics)
	}
	return int(*metrics[0].Metric[0].Counter.Value), nil
}

// validatePrometheusMetrics is a helper function to validate a Prometheus metric counter with an expected count
func validatePrometheusMetric(t *testing.T, metric prometheus.Counter, expectedCount int) {
	t.Helper()
	count, err := getCountFromPrometheusCounterMetric(metric)
	if err != nil {
		t.Fatalf("failed getCountFromCounterMetric: %v", err)
	}
	if count != expectedCount {
		t.Fatalf("failed validating metric count, expected %v, got %v", expectedCount, count)
	}
}
