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
	"strings"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
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
			manager := NewMetricsManager(tc.endpoint, tc.socketDir, tc.maxCollectors, clientset, false).(*manager)

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

func TestProcessMetricsDataAsStream(t *testing.T) {
	metricsData := `
# HELP fs_ops_count The cumulative number of ops processed by the file system.
# TYPE fs_ops_count counter
fs_ops_count{fs_op="GetInodeAttributes"} 26
fs_ops_count{fs_op="LookUpInode"} 6
# HELP gcs_request_count The cumulative number of GCS requests processed along with the GCS method.
# TYPE gcs_request_count counter
gcs_request_count{gcs_method="ListObjects"} 32
gcs_request_count{gcs_method="StatObject"} 6
`
	reader := strings.NewReader(metricsData)
	count := 0
	for mf, err := range ProcessMetricsDataAsStream(reader) {
		if err != nil {
			t.Fatalf("unexpected error parsing stream: %v", err)
		}
		if *mf.Name != "fs_ops_count" && *mf.Name != "gcs_request_count" {
			t.Errorf("unexpected metric name: %s", *mf.Name)
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 metric families, got %d", count)
	}
}

func BenchmarkEmitMetricFamily(b *testing.B) {
	name := "test_metric"
	help := "test help"
	metricType := dto.MetricType_COUNTER
	val := float64(42.0)

	var metrics []*dto.Metric
	for i := 0; i < 100; i++ {
		labelName := "foo"
		labelVal := fmt.Sprintf("bar_%d", i)
		metrics = append(metrics, &dto.Metric{
			Label: []*dto.LabelPair{
				{Name: &labelName, Value: &labelVal},
			},
			Counter: &dto.Counter{Value: &val},
		})
	}

	mf := &dto.MetricFamily{
		Name:   &name,
		Help:   &help,
		Type:   &metricType,
		Metric: metrics,
	}

	c := &metricsCollector{
		constLabels: map[string]string{
			"const_foo": "const_bar",
		},
	}

	ch := make(chan prometheus.Metric, 1000)
	done := make(chan struct{})

	go func() {
		for range ch {
		}
		close(done)
	}()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		c.emitMetricFamily(mf, ch)
	}

	b.StopTimer()
	close(ch)
	<-done
}

func TestJobSetNameExtraction(t *testing.T) {
	fakeClient := clientset.NewFakeClientset()
	fakeClient.CreatePod(clientset.FakePodConfig{
		Name:      "test-pod",
		Namespace: "default",
		PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
	})
	if pod, err := fakeClient.GetPod("default", "test-pod"); err == nil && pod != nil {
		pod.Labels = map[string]string{
			JobSetNameLabel: "my-jobset",
		}
	}

	targetPath := "/var/lib/kubelet/pods/d2013878-3d56-45f9-89ec-0826612c89b6/volumes/kubernetes.io~csi/test-volume/mount"
	socketDir := t.TempDir()
	m := NewMetricsManager(":9920", socketDir, 5, fakeClient, false).(*manager)
	m.RegisterMetricsCollector(targetPath, "default", "test-pod", "test-bucket", "test-node")

	if !m.volumePublishPathRegistered.Has(targetPath) {
		t.Errorf("expected collector to be registered for targetPath")
	}
}
