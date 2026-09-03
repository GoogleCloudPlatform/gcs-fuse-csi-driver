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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestNewMetricsManager(t *testing.T) {
	t.Run("test the metrics manager creation", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name          string
			endpoint      string
			socketDir     string
			maxCollectors int
			enableSchema  bool
		}{
			{
				name:          "basic options test with schema enabled",
				endpoint:      "/test/metrics",
				socketDir:     "/tmp/test-sockets",
				maxCollectors: 5,
				enableSchema:  true,
			},
			{
				name:          "no collectors test with schema disabled",
				endpoint:      ":9920",
				socketDir:     "/gcsfuse-tmp/socket",
				maxCollectors: 0,
				enableSchema:  false,
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)

			clientset := clientset.NewFakeClientset()
			manager := NewMetricsManager(tc.endpoint, tc.socketDir, tc.maxCollectors, clientset, false, tc.enableSchema).(*manager)

			if manager.metricsEndpoint != tc.endpoint {
				t.Errorf("NewMetricsManager did not set metricsEndpoint correctly. Got %q, want %q", manager.metricsEndpoint, tc.endpoint)
			}
			if manager.fuseSocketDir != tc.socketDir {
				t.Errorf("NewMetricsManager did not set fuseSocketDir correctly. Got %q, want %q", manager.fuseSocketDir, tc.socketDir)
			}
			if manager.maximumNumberOfCollectors != tc.maxCollectors {
				t.Errorf("NewMetricsManager did not set maximumNumberOfCollectors correctly. Got %d, want %d", manager.maximumNumberOfCollectors, tc.maxCollectors)
			}
			if manager.enableGcsFuseVolumeMetricsSchema != tc.enableSchema {
				t.Errorf("NewMetricsManager did not set enableGcsFuseVolumeMetricsSchema correctly. Got %v, want %v", manager.enableGcsFuseVolumeMetricsSchema, tc.enableSchema)
			}
			if manager.registry == nil {
				t.Errorf("NewMetricsManager did not initialize registry")
			}
			if manager.volumeMountPathRegistered == nil {
				t.Errorf("NewMetricsManager did not initialize volumeMountPathRegistered")
			}
		}
	})
}

func TestRegisterUnregisterMetricsCollector(t *testing.T) {
	tempDir := t.TempDir()
	fuseSocketDir := filepath.Join(tempDir, "sockets")
	emptyDirBasePath := filepath.Join(tempDir, "emptyDir")

	// Pre-create sockets directory as the symlink is created inside it.
	if err := os.MkdirAll(fuseSocketDir, 0755); err != nil {
		t.Fatalf("failed to create fuseSocketDir: %v", err)
	}
	if err := os.MkdirAll(emptyDirBasePath, 0755); err != nil {
		t.Fatalf("failed to create emptyDirBasePath: %v", err)
	}

	clientset := clientset.NewFakeClientset()
	mm := NewMetricsManager(":9920", fuseSocketDir, 5, clientset, false, true).(*manager)

	mountPath := "/mnt/test-volume"
	podNamespace := "default"
	podName := "test-pod"
	bucketName := "test-bucket"
	nodeName := "test-node"
	podUID := "test-pod-uid-123"
	volumeName := "test-volume-abc"

	// 1. Test Registration
	mm.RegisterMetricsCollector(mountPath, podNamespace, podName, bucketName, nodeName, emptyDirBasePath, podUID, volumeName, "", "")

	if !mm.volumeMountPathRegistered.Has(mountPath) {
		t.Errorf("expected mountPath %q to be registered in volumeMountPathRegistered", mountPath)
	}

	// Verify collector is actually in the Prometheus registry by trying to register a dummy with same descriptors
	cDummy := NewMetricsCollector("", "", "", "", podUID, volumeName, nil, nil, false, false)
	err := mm.registry.Register(cDummy)
	if err == nil {
		t.Errorf("expected duplicate registration to fail, but it succeeded")
		mm.registry.Unregister(cDummy) // Clean up
	} else if !strings.Contains(err.Error(), prometheus.AlreadyRegisteredError{}.Error()) {
		t.Errorf("expected AlreadyRegisteredError, but got: %v", err)
	}

	// 2. Test Unregistration with incorrect identifiers
	mm.UnregisterMetricsCollector(mountPath, nodeName, "wrong-uid", "wrong-volume")
	if !mm.volumeMountPathRegistered.Has(mountPath) {
		t.Errorf("expected mountPath %q to still be registered after incorrect unregistration identifiers", mountPath)
	}

	// 3. Test Unregistration with empty identifiers
	mm.UnregisterMetricsCollector(mountPath, nodeName, "", "")
	if !mm.volumeMountPathRegistered.Has(mountPath) {
		t.Errorf("expected mountPath %q to still be registered after empty unregistration identifiers", mountPath)
	}

	// 4. Test Unregistration with correct identifiers
	mm.UnregisterMetricsCollector(mountPath, nodeName, podUID, volumeName)
	if mm.volumeMountPathRegistered.Has(mountPath) {
		t.Errorf("expected mountPath %q to be unregistered from volumeMountPathRegistered", mountPath)
	}

	// Verify that the collector is no longer in the registry by attempting to register again
	err = mm.registry.Register(cDummy)
	if err != nil {
		t.Errorf("expected registration of collector to succeed after unregistration, but failed: %v", err)
	} else {
		mm.registry.Unregister(cDummy) // Clean up
	}
}

func TestEndToEndGcsFuseVolumeMetricsScraping(t *testing.T) {
	fuseSocketDir, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(fuseSocketDir)

	emptyDirBasePath := filepath.Join(t.TempDir(), "emptyDir")
	if err := os.MkdirAll(emptyDirBasePath, 0755); err != nil {
		t.Fatalf("failed to create emptyDirBasePath: %v", err)
	}

	podUID := "test-pod-uid"
	volumeName := "test-volume-name"
	socketBasePath := util.GetSocketBasePath(podUID, volumeName, fuseSocketDir)
	if err := os.MkdirAll(socketBasePath, 0755); err != nil {
		t.Fatalf("failed to create socketBasePath: %v", err)
	}

	socketPath := filepath.Join(socketBasePath, SocketName)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on socket %s: %v", socketPath, err)
	}
	defer listener.Close()

	metricsData := `# HELP fs_ops_count The cumulative number of ops processed.
# TYPE fs_ops_count counter
fs_ops_count{fs_op="Read"} 42
# HELP fs_ops_duration_microseconds The cumulative distribution of file system operation latencies.
# TYPE fs_ops_duration_microseconds histogram
fs_ops_duration_microseconds_bucket{fs_op="Read",le="100"} 5
fs_ops_duration_microseconds_sum{fs_op="Read"} 250
fs_ops_duration_microseconds_count{fs_op="Read"} 5
`

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(metricsData))
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()

	isController := true

	testCases := []struct {
		name                 string
		enableSchema         bool
		pod                  *corev1.Pod
		expectedLabels       map[string]string
		forbiddenLabelKeys   []string
	}{
		{
			name:         "schema enabled with JobSet pod",
			enableSchema: true,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-name",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
					Labels: map[string]string{
						"jobset.sigs.k8s.io/jobset-name": "test-jobset-name",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":            "test-pod-name",
				"namespace_name":       "test-namespace",
				"volume_name":          volumeName,
				"bucket_name":          "test-bucket-name",
				"k8s_controller_type": "jobset",
				"k8s_controller_name": "test-jobset-name",
			},
			forbiddenLabelKeys: []string{"pod_uid"},
		},
		{
			name:         "schema enabled with StatefulSet owner reference pod",
			enableSchema: true,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ss-0",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "StatefulSet",
							Name:       "test-ss-name",
							Controller: &isController,
						},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":            "test-pod-name",
				"namespace_name":       "test-namespace",
				"volume_name":          volumeName,
				"bucket_name":          "test-bucket-name",
				"k8s_controller_type": "statefulset",
				"k8s_controller_name": "test-ss-name",
			},
			forbiddenLabelKeys: []string{"pod_uid"},
		},
		{
			name:         "schema enabled with standalone pod (no controller info)",
			enableSchema: true,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "standalone-pod",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":            "test-pod-name",
				"namespace_name":       "test-namespace",
				"volume_name":          volumeName,
				"bucket_name":          "test-bucket-name",
				"k8s_controller_type": "",
				"k8s_controller_name": "",
			},
			forbiddenLabelKeys: []string{"pod_uid"},
		},
		{
			name:         "schema disabled (legacy schema)",
			enableSchema: false,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-name",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_uid":     podUID,
				"volume_name": volumeName,
			},
			forbiddenLabelKeys: []string{"pod_name", "namespace_name", "bucket_name", "k8s_controller_type", "k8s_controller_name"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClientset := clientset.NewFakeClientset()
			if tc.pod != nil {
				fakeClientset.CreatePod(clientset.FakePodConfig{
					Name:            tc.pod.Name,
					Namespace:       tc.pod.Namespace,
					UID:             tc.pod.UID,
					Labels:          tc.pod.Labels,
					OwnerReferences: tc.pod.OwnerReferences,
					PodStatus:       &tc.pod.Status,
				})
			}

			mm := NewMetricsManager(":9920", fuseSocketDir, 5, fakeClientset, false, tc.enableSchema).(*manager)
			mountPath := "/mnt/test-volume"
			bucketName := "test-bucket-name"
			nodeName := "test-node-name"

			controllerType, controllerName := ExtractK8sController(tc.pod)
			mm.RegisterMetricsCollector(mountPath, "test-namespace", "test-pod-name", bucketName, nodeName, emptyDirBasePath, podUID, volumeName, controllerType, controllerName)

			metricFamilies, err := mm.registry.Gather()
			if err != nil {
				t.Fatalf("failed to gather metrics: %v", err)
			}
			if len(metricFamilies) == 0 {
				t.Fatalf("expected gathered metric families, got 0")
			}

			foundMetrics := 0
			for _, mf := range metricFamilies {
				for _, m := range mf.GetMetric() {
					foundMetrics++
					labelsMap := make(map[string]string)
					for _, lp := range m.GetLabel() {
						labelsMap[lp.GetName()] = lp.GetValue()
					}
					for wantKey, wantVal := range tc.expectedLabels {
						gotVal, ok := labelsMap[wantKey]
						if !ok {
							t.Errorf("metric %s missing expected label %q", mf.GetName(), wantKey)
						} else if gotVal != wantVal {
							t.Errorf("metric %s label %q = %q, want %q", mf.GetName(), wantKey, gotVal, wantVal)
						}
					}
					for _, forbiddenKey := range tc.forbiddenLabelKeys {
						if val, ok := labelsMap[forbiddenKey]; ok {
							t.Errorf("metric %s should not have label %q, but got %q", mf.GetName(), forbiddenKey, val)
						}
					}
				}
			}
			if foundMetrics == 0 {
				t.Errorf("expected to find metrics, got 0")
			}

			hasMetric := func(metricName string) bool {
				for _, mf := range metricFamilies {
					if mf.GetName() == metricName {
						return true
					}
				}
				return false
			}

			if tc.enableSchema {
				if !hasMetric("fs_ops_duration_microseconds") {
					t.Errorf("expected metric fs_ops_duration_microseconds when schema is enabled, but it was missing")
				}
				if hasMetric("fs_ops_latency") {
					t.Errorf("unexpected legacy metric fs_ops_latency when schema is enabled")
				}
			} else {
				if !hasMetric("fs_ops_latency") {
					t.Errorf("expected legacy metric fs_ops_latency when schema is disabled, but it was missing")
				}
				if hasMetric("fs_ops_duration_microseconds") {
					t.Errorf("unexpected OTEL metric fs_ops_duration_microseconds when schema is disabled")
				}
			}
		})
	}
}

func TestExtractK8sController(t *testing.T) {
	isController := true
	testCases := []struct {
		name                 string
		pod                  *corev1.Pod
		expectedType         string
		expectedName         string
	}{
		{
			name:         "nil pod",
			pod:          nil,
			expectedType: "",
			expectedName: "",
		},
		{
			name: "jobset label jobset.sigs.k8s.io/jobset-name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"jobset.sigs.k8s.io/jobset-name": "my-jobset",
					},
				},
			},
			expectedType: "jobset",
			expectedName: "my-jobset",
		},
		{
			name: "jobset label jobset_name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"jobset_name": "jobset-custom",
					},
				},
			},
			expectedType: "jobset",
			expectedName: "jobset-custom",
		},
		{
			name: "owner reference controller",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "StatefulSet",
							Name:       "my-statefulset",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: "statefulset",
			expectedName: "my-statefulset",
		},
		{
			name: "owner reference fallback without explicit controller bool",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "my-replicaset-123",
						},
					},
				},
			},
			expectedType: "replicaset",
			expectedName: "my-replicaset-123",
		},
		{
			name: "pod without controller info",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "standalone-pod",
				},
			},
			expectedType: "",
			expectedName: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotName := ExtractK8sController(tc.pod)
			if gotType != tc.expectedType || gotName != tc.expectedName {
				t.Errorf("ExtractK8sController() = (%q, %q), want (%q, %q)", gotType, gotName, tc.expectedType, tc.expectedName)
			}
		})
	}
}

func TestRegisterMetricsCollector_FlagDisabled(t *testing.T) {
	fakeClientset := clientset.NewFakeClientset()
	mm := NewMetricsManager(":9920", t.TempDir(), 5, fakeClientset, false, false).(*manager)

	mountPath := "/mnt/test-volume"
	podNamespace := "test-ns"
	podName := "test-pod"
	bucketName := "test-bucket"
	nodeName := "test-node"
	podUID := "test-uid-123"
	volumeName := "test-vol-abc"
	emptyDir := t.TempDir()

	mm.RegisterMetricsCollector(mountPath, podNamespace, podName, bucketName, nodeName, emptyDir, podUID, volumeName, "", "")

	if !mm.volumeMountPathRegistered.Has(mountPath) {
		t.Fatalf("expected mountPath %q to be registered when feature flag is disabled", mountPath)
	}
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

func TestEmitMetricFamily(t *testing.T) {
	t.Parallel()

	counterType := dto.MetricType_COUNTER
	val := float64(42.0)
	name := "test_metric"
	help := "test help string"

	strPtr := func(s string) *string { return &s }

	defaultConstLabels := map[string]string{
		"pod_name":       "test-pod",
		"namespace_name": "default",
		"volume_name":    "vol-1",
		"bucket_name":    "bucket-1",
		"pod_uid":        "",
	}

	testCases := []struct {
		name           string
		constLabels    map[string]string
		metricFamily   *dto.MetricFamily
		expectedEmits  int
		expectedLabels map[string]string
	}{
		{
			name:        "counter without label conflicts",
			constLabels: defaultConstLabels,
			metricFamily: &dto.MetricFamily{
				Name: &name,
				Help: &help,
				Type: &counterType,
				Metric: []*dto.Metric{
					{
						Label: []*dto.LabelPair{
							{Name: strPtr("fs_op"), Value: strPtr("read")},
						},
						Counter: &dto.Counter{Value: &val},
					},
				},
			},
			expectedEmits: 1,
			expectedLabels: map[string]string{
				"fs_op":          "read",
				"pod_name":       "test-pod",
				"namespace_name": "default",
				"volume_name":    "vol-1",
				"bucket_name":    "bucket-1",
				"pod_uid":        "",
			},
		},
		{
			name:        "counter with conflicting labels matching constLabels",
			constLabels: defaultConstLabels,
			metricFamily: &dto.MetricFamily{
				Name: &name,
				Help: &help,
				Type: &counterType,
				Metric: []*dto.Metric{
					{
						Label: []*dto.LabelPair{
							{Name: strPtr("pod_name"), Value: strPtr("malicious_pod")},
							{Name: strPtr("fs_op"), Value: strPtr("write")},
							{Name: strPtr("pod_uid"), Value: strPtr("malicious_uid")},
						},
						Counter: &dto.Counter{Value: &val},
					},
				},
			},
			expectedEmits: 1,
			expectedLabels: map[string]string{
				"fs_op":          "write",
				"pod_name":       "test-pod",
				"namespace_name": "default",
				"volume_name":    "vol-1",
				"bucket_name":    "bucket-1",
				"pod_uid":        "",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := &metricsCollector{
				constLabels: tc.constLabels,
			}

			ch := make(chan prometheus.Metric, tc.expectedEmits+5)
			c.emitMetricFamily(tc.metricFamily, ch)
			close(ch)

			var emittedMetrics []prometheus.Metric
			for m := range ch {
				if m == nil {
					t.Errorf("received nil metric")
					continue
				}
				emittedMetrics = append(emittedMetrics, m)
			}

			if len(emittedMetrics) != tc.expectedEmits {
				t.Fatalf("emitted metrics count = %d, want %d", len(emittedMetrics), tc.expectedEmits)
			}

			if len(emittedMetrics) > 0 {
				var pb dto.Metric
				if err := emittedMetrics[0].Write(&pb); err != nil {
					t.Fatalf("failed to write metric to dto.Metric: %v", err)
				}

				gotLabels := make(map[string]string)
				for _, lp := range pb.GetLabel() {
					gotLabels[lp.GetName()] = lp.GetValue()
				}

				if diff := cmp.Diff(tc.expectedLabels, gotLabels); diff != "" {
					t.Errorf("labels mismatch (-want +got):\n%s", diff)
				}
			}
		})
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
