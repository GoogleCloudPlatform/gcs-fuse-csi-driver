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
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
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
			manager := NewMetricsManager(tc.endpoint, tc.socketDir, tc.maxCollectors, clientset, false, false, tc.enableSchema).(*manager)

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
	mm := NewMetricsManager(":9920", fuseSocketDir, 5, clientset, false, false, true).(*manager)

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
	isController := true
	podUID := "test-pod-uid"
	volumeName := "test-volume-name"

	pluralMetricsData := `# HELP fs_ops_count The cumulative number of ops processed.
# TYPE fs_ops_count counter
fs_ops_count{fs_op="Read"} 42
# HELP fs_ops_latencies The cumulative distribution of file system operation latencies.
# TYPE fs_ops_latencies histogram
fs_ops_latencies_bucket{fs_op="Read",le="100"} 5
fs_ops_latencies_sum{fs_op="Read"} 250
fs_ops_latencies_count{fs_op="Read"} 5
`

	singularMetricsData := `# HELP fs_ops_count The cumulative number of ops processed.
# TYPE fs_ops_count counter
fs_ops_count{fs_op="Read"} 42
# HELP fs_ops_latency The cumulative distribution of file system operation latencies.
# TYPE fs_ops_latency histogram
fs_ops_latency_bucket{fs_op="Read",le="100"} 5
fs_ops_latency_sum{fs_op="Read"} 250
fs_ops_latency_count{fs_op="Read"} 5
`

	testCases := []struct {
		name               string
		enableSchema       bool
		metricsPayload     string
		pod                *corev1.Pod
		expectedLabels     map[string]string
		forbiddenLabelKeys []string
		expectedMetricName string
		forbiddenMetric    string
	}{
		{
			name:           "schema enabled + plural sidecar payload (JobSet pod)",
			enableSchema:   true,
			metricsPayload: pluralMetricsData,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-name",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
					Labels: map[string]string{
						jobSetNameLabel: "test-jobset-name",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":            "test-pod-name",
				"namespace_name":      "test-namespace",
				"volume_name":         volumeName,
				"bucket_name":         "test-bucket-name",
				"k8s_controller_type": controllerTypeJobSet,
				"k8s_controller_name": "test-jobset-name",
			},
			forbiddenLabelKeys: []string{"pod_uid"},
			expectedMetricName: "volume_fs_ops_latencies",
			forbiddenMetric:    "fs_ops_latency",
		},
		{
			name:           "schema enabled + legacy singular sidecar payload (Deployment pod via ReplicaSet owner)",
			enableSchema:   true,
			metricsPayload: singularMetricsData,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-name",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
					Labels: map[string]string{
						podTemplateHashLabel: "7b98f6d9c5",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "my-deployment-7b98f6d9c5",
							Controller: &isController,
						},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":            "test-pod-name",
				"namespace_name":      "test-namespace",
				"volume_name":         volumeName,
				"bucket_name":         "test-bucket-name",
				"k8s_controller_type": controllerTypeDeployment,
				"k8s_controller_name": "my-deployment",
			},
			forbiddenLabelKeys: []string{"pod_uid"},
			expectedMetricName: "volume_fs_ops_latencies",
			forbiddenMetric:    "fs_ops_latency",
		},
		{
			name:           "schema enabled with StatefulSet owner reference pod",
			enableSchema:   true,
			metricsPayload: pluralMetricsData,
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
				"pod_name":            "test-ss-0",
				"namespace_name":      "test-namespace",
				"volume_name":         volumeName,
				"bucket_name":         "test-bucket-name",
				"k8s_controller_type": controllerTypeStatefulSet,
				"k8s_controller_name": "test-ss-name",
			},
			forbiddenLabelKeys: []string{"pod_uid"},
			expectedMetricName: "volume_fs_ops_latencies",
			forbiddenMetric:    "fs_ops_latency",
		},
		{
			name:           "schema enabled with standalone pod (no controller info)",
			enableSchema:   true,
			metricsPayload: pluralMetricsData,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "standalone-pod",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":            "standalone-pod",
				"namespace_name":      "test-namespace",
				"volume_name":         volumeName,
				"bucket_name":         "test-bucket-name",
				"k8s_controller_type": "",
				"k8s_controller_name": "",
			},
			forbiddenLabelKeys: []string{"pod_uid"},
			expectedMetricName: "volume_fs_ops_latencies",
			forbiddenMetric:    "fs_ops_latency",
		},
		{
			name:           "schema disabled (legacy k8s_pod rollback) + plural sidecar payload",
			enableSchema:   false,
			metricsPayload: pluralMetricsData,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-name",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":       "test-pod-name",
				"namespace_name": "test-namespace",
				"volume_name":    volumeName,
				"bucket_name":    "test-bucket-name",
				"pod_uid":        "",
			},
			forbiddenLabelKeys: []string{"k8s_controller_type", "k8s_controller_name"},
			expectedMetricName: "fs_ops_latency",
			forbiddenMetric:    "volume_fs_ops_latencies",
		},
		{
			name:           "schema disabled (legacy k8s_pod rollback) + singular sidecar payload",
			enableSchema:   false,
			metricsPayload: singularMetricsData,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-name",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":       "test-pod-name",
				"namespace_name": "test-namespace",
				"volume_name":    volumeName,
				"bucket_name":    "test-bucket-name",
				"pod_uid":        "",
			},
			forbiddenLabelKeys: []string{"k8s_controller_type", "k8s_controller_name"},
			expectedMetricName: "fs_ops_latency",
			forbiddenMetric:    "volume_fs_ops_latencies",
		},
		{
			name:         "schema enabled keeps grpc metrics on legacy k8s_pod schema without volume_ prefix",
			enableSchema: true,
			metricsPayload: `# HELP grpc_client_attempt_started Total number of RPC attempts started.
# TYPE grpc_client_attempt_started counter
grpc_client_attempt_started{grpc_method="google.storage.v2.Storage/ReadObject",grpc_target="dns:///storage.googleapis.com:443"} 10
`,
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-name",
					Namespace: "test-namespace",
					UID:       types.UID(podUID),
					Labels: map[string]string{
						jobSetNameLabel: "test-jobset-name",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedLabels: map[string]string{
				"pod_name":       "test-pod-name",
				"namespace_name": "test-namespace",
				"volume_name":    volumeName,
				"bucket_name":    "test-bucket-name",
				"pod_uid":        "",
			},
			forbiddenLabelKeys: []string{"k8s_controller_type", "k8s_controller_name"},
			expectedMetricName: "grpc_client_attempt_started",
			forbiddenMetric:    "volume_grpc_client_attempt_started",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fuseSocketDir, err := os.MkdirTemp("/tmp", "s")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(fuseSocketDir)

			emptyDirBasePath := filepath.Join(t.TempDir(), "emptyDir")
			if err := os.MkdirAll(emptyDirBasePath, 0755); err != nil {
				t.Fatalf("failed to create emptyDirBasePath: %v", err)
			}

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

			payload := tc.metricsPayload
			server := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(payload))
				}),
			}
			go func() {
				_ = server.Serve(listener)
			}()
			defer server.Close()

			fakeClientset := clientset.NewFakeClientset()
			if tc.pod != nil {
				fakeClientset.CreatePod(clientset.FakePodConfig{
					Name:      tc.pod.Name,
					Namespace: tc.pod.Namespace,
					UID:       tc.pod.UID,
					PodStatus: &tc.pod.Status,
				})
			}

			mm := NewMetricsManager(":9920", fuseSocketDir, 5, fakeClientset, false, false, tc.enableSchema).(*manager)
			mountPath := "/mnt/test-volume"
			bucketName := "test-bucket-name"
			nodeName := "test-node-name"

			controllerType, controllerName := ExtractK8sController(tc.pod)
			mm.RegisterMetricsCollector(mountPath, tc.pod.Namespace, tc.pod.Name, bucketName, nodeName, emptyDirBasePath, podUID, volumeName, controllerType, controllerName)

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

			if !hasMetric(tc.expectedMetricName) {
				t.Errorf("expected metric %s, but it was missing", tc.expectedMetricName)
			}
			if tc.forbiddenMetric != "" && hasMetric(tc.forbiddenMetric) {
				t.Errorf("forbidden metric %s was unexpectedly emitted", tc.forbiddenMetric)
			}
		})
	}
}

func TestExtractK8sController(t *testing.T) {
	isController := true
	notController := false
	testCases := []struct {
		name         string
		pod          *corev1.Pod
		expectedType string
		expectedName string
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
						jobSetNameLabel: "my-jobset",
					},
				},
			},
			expectedType: controllerTypeJobSet,
			expectedName: "my-jobset",
		},
		{
			name: "jobset label takes precedence over child Job controller OwnerReference",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						jobSetNameLabel: "my-jobset",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "Job",
							Name:       "my-jobset-worker-0",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: controllerTypeJobSet,
			expectedName: "my-jobset",
		},
		{
			name: "leaderworkerset label leaderworkerset.sigs.k8s.io/name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						leaderWorkerSetNameLabel: "my-lws",
					},
				},
			},
			expectedType: controllerTypeLeaderWorkerSet,
			expectedName: "my-lws",
		},
		{
			name: "leaderworkerset label takes precedence over child worker StatefulSet controller OwnerReference",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						leaderWorkerSetNameLabel: "deepseek",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "StatefulSet",
							Name:       "deepseek-0",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: controllerTypeLeaderWorkerSet,
			expectedName: "deepseek",
		},
		{
			name: "non-standard jobset_name label does not spoof jobset controller",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"jobset_name": "custom-user-label",
					},
				},
			},
			expectedType: "",
			expectedName: "",
		},
		{
			name: "deployment pod via ReplicaSet owner reference and pod-template-hash label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						podTemplateHashLabel: "7b98f6d9c5",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "my-app-7b98f6d9c5",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: controllerTypeDeployment,
			expectedName: "my-app",
		},
		{
			name: "ReplicaSet with name equal to -hash falls back to replicaset instead of empty deployment name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						podTemplateHashLabel: "7b98f6d9c5",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "-7b98f6d9c5",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: controllerTypeReplicaSet,
			expectedName: "-7b98f6d9c5",
		},
		{
			name: "Argo Rollout ReplicaSet with rollouts-pod-template-hash label is left blank instead of mislabeling as replicaset",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						rolloutsPodTemplateHashLabel: "6c8f7d9b54",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "my-rollout-6c8f7d9b54",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: "",
			expectedName: "",
		},
		{
			name: "owner reference with nil Controller pointer is ignored",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "my-rs",
							Controller: nil,
						},
					},
				},
			},
			expectedType: "",
			expectedName: "",
		},
		{
			name: "owner reference controller (StatefulSet)",
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
			expectedType: controllerTypeStatefulSet,
			expectedName: "my-statefulset",
		},
		{
			name: "owner reference controller (DaemonSet)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "DaemonSet",
							Name:       "my-daemonset",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: controllerTypeDaemonSet,
			expectedName: "my-daemonset",
		},
		{
			name: "standalone ReplicaSet without pod-template-hash label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "my-replicaset-123",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: controllerTypeReplicaSet,
			expectedName: "my-replicaset-123",
		},
		{
			name: "Job controller owner reference without jobset label is unsupported and left blank (could be CronJob child)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "Job",
							Name:       "my-cronjob-28839120",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: "",
			expectedName: "",
		},
		{
			name: "unsupported controller kind is left blank",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "CustomController",
							Name:       "my-custom-controller",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: "",
			expectedName: "",
		},
		{
			name: "supported controller kind with empty name is left blank",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "StatefulSet",
							Name:       "",
							Controller: &isController,
						},
					},
				},
			},
			expectedType: "",
			expectedName: "",
		},
		{
			name: "non-controller owner reference is ignored",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ConfigMap",
							Name:       "some-config",
							Controller: &notController,
						},
					},
				},
			},
			expectedType: "",
			expectedName: "",
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
	mm := NewMetricsManager(":9920", t.TempDir(), 5, fakeClientset, false, false, false).(*manager)

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

func TestStreamMetricsHandlerSequentialMounts(t *testing.T) {
	t.Parallel()

	for _, enableSchema := range []bool{false, true} {
		t.Run(fmt.Sprintf("enableSchema=%v", enableSchema), func(t *testing.T) {
			t.Parallel()

			fuseSocketDir, err := os.MkdirTemp("/tmp", "seq")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(fuseSocketDir)

			fakeClientset := clientset.NewFakeClientset()
			mm := NewMetricsManager(":0", fuseSocketDir, 10, fakeClientset, false, true, enableSchema).(*manager)
			mm.streamStartIndex = func(int) int { return 0 }

			var inFlight atomic.Int32
			var maxInFlight atomic.Int32
			var scrapeOrderMu sync.Mutex
			var scrapeOrder []string

			mountConfigs := []struct {
				podUID     string
				podName    string
				volumeName string
				bucketName string
				opValue    int
			}{
				{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", bucketName: "bucket-1", opValue: 10},
				{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", bucketName: "bucket-2", opValue: 20},
				{podUID: "uid-3", podName: "pod-3", volumeName: "vol-3", bucketName: "bucket-3", opValue: 30},
			}

			for i, mc := range mountConfigs {
				emptyDirBasePath := filepath.Join(t.TempDir(), fmt.Sprintf("emptyDir-%d", i))
				if err := os.MkdirAll(emptyDirBasePath, 0755); err != nil {
					t.Fatalf("failed to create emptyDirBasePath: %v", err)
				}

				socketPath := filepath.Join(emptyDirBasePath, SocketName)
				listener, err := net.Listen("unix", socketPath)
				if err != nil {
					t.Fatalf("failed to listen on socket %s: %v", socketPath, err)
				}
				defer listener.Close()

				volName := mc.volumeName
				opVal := mc.opValue
				payload := fmt.Sprintf(`# HELP fs_ops_count The cumulative number of ops processed.
# TYPE fs_ops_count counter
fs_ops_count{fs_op="Read"} %d
# HELP fs_ops_latencies The cumulative distribution of file system operation latencies.
# TYPE fs_ops_latencies histogram
fs_ops_latencies_bucket{fs_op="Read",le="100"} %d
fs_ops_latencies_sum{fs_op="Read"} %d
fs_ops_latencies_count{fs_op="Read"} %d
# HELP grpc_client_attempt_started Total number of RPC attempts started.
# TYPE grpc_client_attempt_started counter
grpc_client_attempt_started{grpc_method="ReadObject"} %d
`, opVal, opVal, opVal*50, opVal, opVal)

				srv := &http.Server{
					Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						cur := inFlight.Add(1)
						for {
							prev := maxInFlight.Load()
							if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
								break
							}
						}
						time.Sleep(15 * time.Millisecond)
						scrapeOrderMu.Lock()
						scrapeOrder = append(scrapeOrder, volName)
						scrapeOrderMu.Unlock()
						inFlight.Add(-1)

						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(payload))
					}),
				}
				go func() {
					_ = srv.Serve(listener)
				}()
				defer srv.Close()

				fakeClientset.CreatePod(clientset.FakePodConfig{
					Name:      mc.podName,
					Namespace: "default",
					UID:       types.UID(mc.podUID),
					PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
				})

				mountPath := fmt.Sprintf("/mnt/%s", mc.volumeName)
				mm.RegisterMetricsCollector(mountPath, "default", mc.podName, mc.bucketName, "node-1", emptyDirBasePath, mc.podUID, mc.volumeName, "deployment", "app")
			}

			req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
			rec := httptest.NewRecorder()
			mm.metricsHandler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected HTTP status code: got %d, want %d", rec.Code, http.StatusOK)
			}

			if gotMax := maxInFlight.Load(); gotMax != 1 {
				t.Errorf("expected mounts to be scraped sequentially (maxInFlight=1), got %d", gotMax)
			}

			wantOrder := []string{"vol-1", "vol-2", "vol-3"}
			scrapeOrderMu.Lock()
			gotOrder := slices.Clone(scrapeOrder)
			scrapeOrderMu.Unlock()
			if diff := cmp.Diff(wantOrder, gotOrder); diff != "" {
				t.Errorf("scrape order mismatch (-want +got):\n%s", diff)
			}

			body := rec.Body.String()
			expectedCounterFamily := "fs_ops_count"
			expectedHistFamily := "fs_ops_latencies"
			if enableSchema {
				expectedCounterFamily = "volume_fs_ops_count"
				expectedHistFamily = "volume_fs_ops_latencies"
			} else {
				expectedHistFamily = "fs_ops_latency"
			}

			// Verify that # HELP and # TYPE appear exactly once per metric family across all 3 mounts.
			for _, famName := range []string{expectedCounterFamily, expectedHistFamily, "grpc_client_attempt_started"} {
				helpPrefix := "# HELP " + famName + " "
				typePrefix := "# TYPE " + famName + " "
				if c := strings.Count(body, helpPrefix); c != 1 {
					t.Errorf("expected exactly 1 %q line across 3 mounts, got %d\nbody:\n%s", helpPrefix, c, body)
				}
				if c := strings.Count(body, typePrefix); c != 1 {
					t.Errorf("expected exactly 1 %q line across 3 mounts, got %d\nbody:\n%s", typePrefix, c, body)
				}
			}

			// Verify standard Prometheus TextParser (ProcessMetricsData) parses the multi-mount output without error.
			families, err := ProcessMetricsData(strings.NewReader(body))
			if err != nil {
				t.Fatalf("ProcessMetricsData failed on multi-mount streamed output: %v\nbody:\n%s", err, body)
			}

			counterMF, ok := families[expectedCounterFamily]
			if !ok {
				t.Fatalf("missing expected counter family %q in parsed families", expectedCounterFamily)
			}
			if len(counterMF.GetMetric()) != 3 {
				t.Fatalf("expected 3 metrics in %q, got %d", expectedCounterFamily, len(counterMF.GetMetric()))
			}

			histMF, ok := families[expectedHistFamily]
			if !ok {
				t.Fatalf("missing expected histogram family %q in parsed families", expectedHistFamily)
			}
			if len(histMF.GetMetric()) != 3 {
				t.Fatalf("expected 3 metrics in %q, got %d", expectedHistFamily, len(histMF.GetMetric()))
			}

			grpcMF, ok := families["grpc_client_attempt_started"]
			if !ok {
				t.Fatalf("missing expected gRPC family grpc_client_attempt_started in parsed families")
			}
			if len(grpcMF.GetMetric()) != 3 {
				t.Fatalf("expected 3 metrics in grpc_client_attempt_started, got %d", len(grpcMF.GetMetric()))
			}
		})
	}
}

func TestStreamMetricsHandlerGzipAndFaultTolerance(t *testing.T) {
	t.Parallel()

	fuseSocketDir, err := os.MkdirTemp("/tmp", "gz")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(fuseSocketDir)

	fakeClientset := clientset.NewFakeClientset()
	mm := NewMetricsManager(":0", fuseSocketDir, 10, fakeClientset, false, true, true).(*manager)
	mm.streamStartIndex = func(int) int { return 0 }

	// Mount 1: Failing sidecar returning HTTP 500
	emptyDir1 := filepath.Join(t.TempDir(), "ed1")
	_ = os.MkdirAll(emptyDir1, 0755)
	ln1, err := net.Listen("unix", filepath.Join(emptyDir1, SocketName))
	if err != nil {
		t.Fatalf("failed to listen on socket 1: %v", err)
	}
	defer ln1.Close()
	srv1 := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}),
	}
	go func() { _ = srv1.Serve(ln1) }()
	defer srv1.Close()

	fakeClientset.CreatePod(clientset.FakePodConfig{
		Name:      "pod-fail",
		Namespace: "default",
		UID:       types.UID("uid-fail"),
		PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
	})
	mm.RegisterMetricsCollector("/mnt/fail", "default", "pod-fail", "b1", "node-1", emptyDir1, "uid-fail", "vol-fail", "", "")

	// Mount 2: Healthy sidecar
	emptyDir2 := filepath.Join(t.TempDir(), "ed2")
	_ = os.MkdirAll(emptyDir2, 0755)
	ln2, err := net.Listen("unix", filepath.Join(emptyDir2, SocketName))
	if err != nil {
		t.Fatalf("failed to listen on socket 2: %v", err)
	}
	defer ln2.Close()
	srv2 := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# HELP fs_ops_count Ops.\n# TYPE fs_ops_count counter\nfs_ops_count{fs_op=\"Read\"} 77\n"))
		}),
	}
	go func() { _ = srv2.Serve(ln2) }()
	defer srv2.Close()

	fakeClientset.CreatePod(clientset.FakePodConfig{
		Name:      "pod-ok",
		Namespace: "default",
		UID:       types.UID("uid-ok"),
		PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
	})
	mm.RegisterMetricsCollector("/mnt/ok", "default", "pod-ok", "b2", "node-1", emptyDir2, "uid-ok", "vol-ok", "statefulset", "sts-1")

	// Request with Accept-Encoding: gzip
	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	req.Header.Set("Accept-Encoding", "deflate, gzip;q=0.9")
	rec := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected Content-Encoding=gzip, got %q", got)
	}

	gzReader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gzReader.Close()
	decompressed, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("failed to decompress gzip body: %v", err)
	}

	if !strings.Contains(string(decompressed), `volume_fs_ops_count{`) || !strings.Contains(string(decompressed), `volume_name="vol-ok"`) {
		t.Errorf("expected healthy mount metrics in decompressed response, got:\n%s", string(decompressed))
	}

	// Unregister healthy mount and verify empty response on subsequent scrape
	mm.UnregisterMetricsCollector("/mnt/ok", "node-1", "uid-ok", "vol-ok")
	req2 := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	req2.Header.Set("Accept-Encoding", "identity")
	rec2 := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec2, req2)

	if got := rec2.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("expected no Content-Encoding when gzip is not accepted, got %q", got)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("expected empty metrics body after unregistering healthy mount, got %q", rec2.Body.String())
	}

	// Canceled request context should skip scraping immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req3 := httptest.NewRequestWithContext(ctx, http.MethodGet, metricsPath, nil)
	rec3 := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec3, req3)
	if rec3.Body.Len() != 0 {
		t.Errorf("expected empty body for canceled context, got %q", rec3.Body.String())
	}
}

func TestTransformMetricFamilyAndSkipFirstLineWriter(t *testing.T) {
	t.Parallel()

	c := &metricsCollector{
		constLabels: map[string]string{
			"pod_name":       "pod-a",
			"namespace_name": "ns-a",
			"volume_name":    "vol-a",
			"bucket_name":    "bucket-a",
			"pod_uid":        "",
		},
		enableGcsFuseVolumeMetricsSchema: false,
	}

	if c.transformMetricFamily(nil) {
		t.Errorf("expected nil MetricFamily to return false")
	}

	name := "volume_fs_ops_latencies"
	gaugeHistType := dto.MetricType_GAUGE_HISTOGRAM
	sampleCount := uint64(4)
	sampleSum := float64(12.5)
	ub := float64(10)
	cumCount := uint64(4)
	conflictKey := "pod_name"
	conflictVal := "spoofed-pod"
	opKey := "fs_op"
	opVal := "Open"

	mf := &dto.MetricFamily{
		Name: &name,
		Type: &gaugeHistType,
		Metric: []*dto.Metric{
			nil,
			{
				// Missing Histogram field, should be filtered out by hasValidMetricValue
				Label: []*dto.LabelPair{{Name: &opKey, Value: &opVal}},
			},
			{
				Label: []*dto.LabelPair{
					{Name: &conflictKey, Value: &conflictVal},
					{Name: &opKey, Value: &opVal},
				},
				Histogram: &dto.Histogram{
					SampleCount: &sampleCount,
					SampleSum:   &sampleSum,
					Bucket: []*dto.Bucket{
						{UpperBound: &ub, CumulativeCount: &cumCount},
					},
				},
			},
		},
	}

	if !c.transformMetricFamily(mf) {
		t.Fatalf("expected transformMetricFamily to return true")
	}
	if mf.GetName() != "fs_ops_latency" {
		t.Errorf("expected normalized name fs_ops_latency, got %q", mf.GetName())
	}
	if mf.GetType() != dto.MetricType_HISTOGRAM {
		t.Errorf("expected GAUGE_HISTOGRAM to be normalized to HISTOGRAM, got %v", mf.GetType())
	}
	if len(mf.GetMetric()) != 1 {
		t.Fatalf("expected 1 valid metric after filtering, got %d", len(mf.GetMetric()))
	}

	gotLabels := make(map[string]string)
	for _, lp := range mf.GetMetric()[0].GetLabel() {
		gotLabels[lp.GetName()] = lp.GetValue()
	}
	if gotLabels["pod_name"] != "pod-a" {
		t.Errorf("expected conflicting pod_name to be overwritten with pod-a, got %q", gotLabels["pod_name"])
	}

	// Test skipFirstLineWriter across chunked writes
	var buf bytes.Buffer
	sw := &skipFirstLineWriter{w: &buf}
	_, _ = sw.Write([]byte("# TYPE foo"))
	_, _ = sw.Write([]byte(" counter\nfoo_total 1\n"))
	_, _ = sw.Write([]byte("foo_total 2\n"))
	if got, want := buf.String(), "foo_total 1\nfoo_total 2\n"; got != want {
		t.Errorf("skipFirstLineWriter output = %q, want %q", got, want)
	}
}

// fakeMount describes a fake gcsfuse sidecar metrics endpoint registered with the metrics manager.
type fakeMount struct {
	podUID     string
	podName    string
	volumeName string
	handler    http.HandlerFunc
}

// registerFakeMounts starts a unix-socket HTTP server per mount, creates a running pod for it,
// and registers the corresponding metrics collector with mm in the given order.
func registerFakeMounts(t *testing.T, mm *manager, fakeClientset *clientset.FakeClientset, mounts []fakeMount) {
	t.Helper()

	for _, m := range mounts {
		emptyDirBasePath, err := os.MkdirTemp("/tmp", "ed")
		if err != nil {
			t.Fatalf("failed to create emptyDir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(emptyDirBasePath) })

		ln, err := net.Listen("unix", filepath.Join(emptyDirBasePath, SocketName))
		if err != nil {
			t.Fatalf("failed to listen on socket for %s: %v", m.volumeName, err)
		}
		srv := &http.Server{Handler: m.handler}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { srv.Close() })

		fakeClientset.CreatePod(clientset.FakePodConfig{
			Name:      m.podName,
			Namespace: "default",
			UID:       types.UID(m.podUID),
			PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
		})
		mm.RegisterMetricsCollector("/mnt/"+m.volumeName, "default", m.podName, "bucket-"+m.volumeName, "node-1", emptyDirBasePath, m.podUID, m.volumeName, "deployment", "app")
	}
}

func newStreamTestManager(t *testing.T, enableSchema bool) (*manager, *clientset.FakeClientset) {
	t.Helper()

	fuseSocketDir, err := os.MkdirTemp("/tmp", "sm")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(fuseSocketDir) })

	fakeClientset := clientset.NewFakeClientset()
	mm := NewMetricsManager(":0", fuseSocketDir, 10, fakeClientset, false, true, enableSchema).(*manager)
	// Scrape mounts in registration order by default, so that tests are deterministic.
	mm.streamStartIndex = func(int) int { return 0 }
	return mm, fakeClientset
}

// cyclingStartIndex returns a streamStartIndex function that starts the k-th request at mount k mod n,
// so that tests can deterministically exercise every starting mount.
func cyclingStartIndex() func(n int) int {
	var calls atomic.Int64
	return func(n int) int {
		return int((calls.Add(1) - 1) % int64(n))
	}
}

func staticPayloadHandler(payload string, scrapes *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if scrapes != nil {
			scrapes.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}
}

func mountPayload(opValue int) string {
	return fmt.Sprintf(`# HELP fs_ops_count The cumulative number of ops processed.
# TYPE fs_ops_count counter
fs_ops_count{fs_op="Read"} %d
# HELP fs_ops_latencies The cumulative distribution of file system operation latencies.
# TYPE fs_ops_latencies histogram
fs_ops_latencies_bucket{fs_op="Read",le="100"} %d
fs_ops_latencies_sum{fs_op="Read"} %d
fs_ops_latencies_count{fs_op="Read"} %d
`, opValue, opValue, opValue*50, opValue)
}

func TestStreamMetricsHandlerTimeBudget(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	// A hung sidecar uses up the whole (short) budget, so the remaining mounts of a scrape are skipped.
	mm.streamMetricsBudget = 300 * time.Millisecond
	mm.streamStartIndex = cyclingStartIndex()

	// Sidecars that hang until the scrape request is canceled (or for much longer than the server WriteTimeout).
	hungHandler := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}
	var vol5Scrapes atomic.Int32
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(mountPayload(10), nil)},
		{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: hungHandler},
		{podUID: "uid-3", podName: "pod-3", volumeName: "vol-3", handler: hungHandler},
		{podUID: "uid-4", podName: "pod-4", volumeName: "vol-4", handler: hungHandler},
		{podUID: "uid-5", podName: "pod-5", volumeName: "vol-5", handler: staticPayloadHandler(mountPayload(50), &vol5Scrapes)},
	})

	const writeTimeout = 2 * time.Second
	srv := httptest.NewUnstartedServer(mm.metricsHandler())
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	defer srv.Close()

	// Across consecutive scrapes, the starting mount varies, so every healthy mount is exported at least once.
	seen := map[string]int{}
	for i := range 5 {
		acceptEncoding := "identity"
		if i%2 == 1 {
			acceptEncoding = "gzip"
		}
		req, err := http.NewRequest(http.MethodGet, srv.URL+metricsPath, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Accept-Encoding", acceptEncoding)

		start := time.Now()
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("scrape %d: metrics request failed: %v", i, err)
		}

		var bodyReader io.Reader = resp.Body
		if resp.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(resp.Body)
			if err != nil {
				resp.Body.Close()
				t.Fatalf("scrape %d: failed to create gzip reader: %v", i, err)
			}
			bodyReader = gz
		}
		body, err := io.ReadAll(bodyReader)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("scrape %d: failed to read full response body: %v", i, err)
		}
		if elapsed := time.Since(start); elapsed >= writeTimeout {
			t.Errorf("scrape %d: response took %v, want less than server WriteTimeout %v", i, elapsed, writeTimeout)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("scrape %d: unexpected status code: %d", i, resp.StatusCode)
		}

		if _, err := ProcessMetricsData(bytes.NewReader(body)); err != nil {
			t.Fatalf("scrape %d: failed to parse partial response: %v\nbody:\n%s", i, err, body)
		}
		for _, vol := range []string{"vol-1", "vol-2", "vol-3", "vol-4", "vol-5"} {
			if strings.Contains(string(body), `volume_name="`+vol+`"`) {
				seen[vol]++
			}
		}
	}

	for _, vol := range []string{"vol-1", "vol-5"} {
		if seen[vol] == 0 {
			t.Errorf("expected metrics of healthy mount %s in at least one scrape, seen counts: %v", vol, seen)
		}
	}
	for _, vol := range []string{"vol-2", "vol-3", "vol-4"} {
		if seen[vol] != 0 {
			t.Errorf("expected no metrics of hung mount %s, seen counts: %v", vol, seen)
		}
	}
	if vol5Scrapes.Load() == 0 {
		t.Errorf("expected vol-5 to be scraped at least once across consecutive scrapes")
	}
}

func TestStreamMountScrapeTimeout(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		remaining time.Duration
		want      time.Duration
	}{
		{name: "capped at mount scrape timeout", remaining: 8 * time.Second, want: mountScrapeTimeout},
		{name: "capped at remaining budget", remaining: 2 * time.Second, want: 2 * time.Second},
		{name: "budget exhausted", remaining: -time.Second, want: -time.Second},
	}
	for _, tc := range testCases {
		if got := streamMountScrapeTimeout(mountScrapeTimeout, tc.remaining); got != tc.want {
			t.Errorf("%s: streamMountScrapeTimeout(%v) = %v, want %v", tc.name, tc.remaining, got, tc.want)
		}
	}
}

func TestStreamMetricsHandlerScrapeOrder(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		startIndex func(n int) int
		want       []string
	}{
		{
			name:       "start at first mount",
			startIndex: func(int) int { return 0 },
			want:       []string{"vol-1", "vol-2", "vol-3", "vol-4"},
		},
		{
			// Mounts are scraped in registration order starting at the chosen mount, wrapping around.
			name:       "start at third mount",
			startIndex: func(int) int { return 2 },
			want:       []string{"vol-3", "vol-4", "vol-1", "vol-2"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mm, fakeClientset := newStreamTestManager(t, true)
			mm.streamStartIndex = tc.startIndex
			order, mounts := registerRecordingMounts(t, mm, fakeClientset, 4)

			rec := httptest.NewRecorder()
			mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
			body := rec.Body.String()
			if _, err := ProcessMetricsData(strings.NewReader(body)); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}
			for _, m := range mounts {
				if !strings.Contains(body, `volume_name="`+m.volumeName+`"`) {
					t.Errorf("expected metrics of mount %s, got:\n%s", m.volumeName, body)
				}
			}
			if diff := cmp.Diff(tc.want, order()); diff != "" {
				t.Errorf("scrape order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStreamMetricsHandlerRandomStartingMount(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	// Use the production default, which picks a random starting mount for every request.
	mm.streamStartIndex = nil
	const numMounts = 4
	order, _ := registerRecordingMounts(t, mm, fakeClientset, numMounts)

	// The probability that some mount is never picked as the starting mount in 100 requests is about
	// 4 * (3/4)^100, i.e. negligible.
	firstMounts := map[string]bool{}
	for range 100 {
		rec := httptest.NewRecorder()
		mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
		got := order()
		if len(got) != numMounts {
			t.Fatalf("expected every mount to be scraped exactly once per request, got order %v", got)
		}
		firstMounts[got[0]] = true
	}
	if len(firstMounts) != numMounts {
		t.Errorf("expected every mount to be picked as the starting mount, got %v", firstMounts)
	}
}

// registerRecordingMounts registers numMounts healthy fake mounts vol-1..vol-n, and returns a function
// that returns (and resets) the order in which the mounts' sidecars were scraped.
func registerRecordingMounts(t *testing.T, mm *manager, fakeClientset *clientset.FakeClientset, numMounts int) (func() []string, []fakeMount) {
	t.Helper()

	var orderMu sync.Mutex
	var order []string
	recordingHandler := func(vol string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			orderMu.Lock()
			order = append(order, vol)
			orderMu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(mountPayload(1)))
		}
	}
	var mounts []fakeMount
	for i := 1; i <= numMounts; i++ {
		vol := fmt.Sprintf("vol-%d", i)
		mounts = append(mounts, fakeMount{podUID: fmt.Sprintf("uid-%d", i), podName: fmt.Sprintf("pod-%d", i), volumeName: vol, handler: recordingHandler(vol)})
	}
	registerFakeMounts(t, mm, fakeClientset, mounts)

	return func() []string {
		orderMu.Lock()
		defer orderMu.Unlock()
		got := order
		order = nil
		return got
	}, mounts
}

func TestStreamMetricsHandlerDropsTruncatedMount(t *testing.T) {
	t.Parallel()

	// The body is cut off in the middle of a histogram, after the complete counter family.
	const partialPayload = `# HELP truncated_ops_count The cumulative number of ops processed.
# TYPE truncated_ops_count counter
truncated_ops_count{fs_op="Read"} 7
# HELP truncated_ops_latencies The cumulative distribution of file system operation latencies.
# TYPE truncated_ops_latencies histogram
truncated_ops_latencies_bucket{fs_op="Read",le="1"} 3
truncated_ops_latencies_bucket{fs_op="Read",le="10"} 5
`
	writePartial := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(partialPayload))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	testCases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "connection aborted mid-body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writePartial(w)
				panic(http.ErrAbortHandler)
			},
		},
		{
			name: "per-mount timeout mid-body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writePartial(w)
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mm, fakeClientset := newStreamTestManager(t, true)
			mm.streamMetricsBudget = 300 * time.Millisecond
			// The healthy mount is scraped first, since a hung mount uses up the remaining budget.
			registerFakeMounts(t, mm, fakeClientset, []fakeMount{
				{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: staticPayloadHandler(mountPayload(20), nil)},
				{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: tc.handler},
			})

			rec := httptest.NewRecorder()
			mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
			body := rec.Body.String()

			if _, err := ProcessMetricsData(strings.NewReader(body)); err != nil {
				t.Fatalf("failed to parse response: %v\nbody:\n%s", err, body)
			}
			if strings.Contains(body, "truncated_ops") || strings.Contains(body, `volume_name="vol-1"`) {
				t.Errorf("expected no families of the truncated mount vol-1, got:\n%s", body)
			}
			if !strings.Contains(body, `volume_name="vol-2"`) {
				t.Errorf("expected metrics of healthy mount vol-2, got:\n%s", body)
			}
		})
	}
}

func TestStreamMetricsBudgetFor(t *testing.T) {
	t.Parallel()

	mm := &manager{streamMetricsBudget: 8 * time.Second}
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "no header", want: 8 * time.Second},
		{name: "larger scraper timeout", header: "30", want: 8 * time.Second},
		{name: "smaller scraper timeout", header: "5", want: 4500 * time.Millisecond},
		{name: "invalid header", header: "abc", want: 8 * time.Second},
		{name: "non-positive header", header: "0", want: 8 * time.Second},
	}
	for _, tc := range tests {
		h := http.Header{}
		if tc.header != "" {
			h.Set(scrapeTimeoutHeader, tc.header)
		}
		if got := mm.streamMetricsBudgetFor(h); got != tc.want {
			t.Errorf("%s: streamMetricsBudgetFor() = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := (&manager{}).streamMetricsBudgetFor(http.Header{}); got != defaultStreamMetricsBudget {
		t.Errorf("unset budget: got %v, want %v", got, defaultStreamMetricsBudget)
	}
}

func TestStreamMetricsHandlerProtobufDelimited(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(mountPayload(10), nil)},
		{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: staticPayloadHandler(mountPayload(20), nil)},
	})

	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	req.Header.Set("Accept", "application/vnd.google.protobuf; proto=io.prometheus.client.MetricFamily; encoding=delimited")
	rec := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if got := expfmt.Format(ct).FormatType(); got != expfmt.TypeProtoDelim {
		t.Fatalf("expected delimited protobuf Content-Type, got %q", ct)
	}

	type sample struct {
		metricType dto.MetricType
		value      float64
	}
	// family name -> volume_name -> sample
	got := map[string]map[string]sample{}
	dec := expfmt.NewDecoder(rec.Body, expfmt.NewFormat(expfmt.TypeProtoDelim))
	for {
		var mf dto.MetricFamily
		if err := dec.Decode(&mf); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("failed to decode delimited protobuf body: %v", err)
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["fs_op"] != "Read" || labels["pod_name"] == "" || labels["bucket_name"] != "bucket-"+labels["volume_name"] {
				t.Errorf("unexpected labels for %q: %v", mf.GetName(), labels)
			}
			value := m.GetCounter().GetValue()
			if mf.GetType() == dto.MetricType_HISTOGRAM {
				value = float64(m.GetHistogram().GetSampleCount())
			}
			if got[mf.GetName()] == nil {
				got[mf.GetName()] = map[string]sample{}
			}
			got[mf.GetName()][labels["volume_name"]] = sample{metricType: mf.GetType(), value: value}
		}
	}

	want := map[string]map[string]sample{
		"volume_fs_ops_count": {
			"vol-1": {metricType: dto.MetricType_COUNTER, value: 10},
			"vol-2": {metricType: dto.MetricType_COUNTER, value: 20},
		},
		"volume_fs_ops_latencies": {
			"vol-1": {metricType: dto.MetricType_HISTOGRAM, value: 10},
			"vol-2": {metricType: dto.MetricType_HISTOGRAM, value: 20},
		},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(sample{})); diff != "" {
		t.Errorf("decoded protobuf samples mismatch (-want +got):\n%s", diff)
	}
}

func TestStreamMetricsHandlerConflictingTypes(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, false)
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(`# HELP conflicting_metric A metric.
# TYPE conflicting_metric counter
conflicting_metric{op="a"} 1
`, nil)},
		{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: staticPayloadHandler(`# HELP conflicting_metric A metric.
# TYPE conflicting_metric gauge
conflicting_metric{op="a"} 2
# HELP other_metric Another metric.
# TYPE other_metric gauge
other_metric{op="a"} 3
`, nil)},
	})

	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	rec := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec, req)

	body := rec.Body.String()
	families, err := ProcessMetricsData(strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to parse response with conflicting types: %v\nbody:\n%s", err, body)
	}

	conflicting, ok := families["conflicting_metric"]
	if !ok {
		t.Fatalf("missing conflicting_metric in response:\n%s", body)
	}
	if conflicting.GetType() != dto.MetricType_COUNTER {
		t.Errorf("expected first emitted type COUNTER to win, got %v", conflicting.GetType())
	}
	if len(conflicting.GetMetric()) != 1 || conflicting.GetMetric()[0].GetCounter().GetValue() != 1 {
		t.Errorf("expected only vol-1's conflicting_metric sample, got:\n%s", body)
	}

	other, ok := families["other_metric"]
	if !ok || len(other.GetMetric()) != 1 || other.GetMetric()[0].GetGauge().GetValue() != 3 {
		t.Errorf("expected vol-2's other_metric to still be emitted, got:\n%s", body)
	}
}

// failingResponseWriter wraps an http.ResponseWriter and fails every Write after the first allowedWrites.
type failingResponseWriter struct {
	http.ResponseWriter
	allowedWrites int
	writes        int
}

func (f *failingResponseWriter) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > f.allowedWrites {
		return 0, fmt.Errorf("simulated write failure")
	}
	return f.ResponseWriter.Write(p)
}

func TestStreamMetricsHandlerStopsOnWriteError(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	var scrapes1, scrapes2, scrapes3 atomic.Int32
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(mountPayload(10), &scrapes1)},
		{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: staticPayloadHandler(mountPayload(20), &scrapes2)},
		{podUID: "uid-3", podName: "pod-3", volumeName: "vol-3", handler: staticPayloadHandler(mountPayload(30), &scrapes3)},
	})

	rec := httptest.NewRecorder()
	w := &failingResponseWriter{ResponseWriter: rec, allowedWrites: 1}
	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	mm.metricsHandler().ServeHTTP(w, req)

	if w.writes < 2 {
		t.Fatalf("expected the handler to hit the simulated write failure, got %d writes", w.writes)
	}
	if got := scrapes1.Load(); got != 1 {
		t.Errorf("expected first mount to be scraped once, got %d", got)
	}
	if got := scrapes2.Load() + scrapes3.Load(); got != 0 {
		t.Errorf("expected later mounts not to be scraped after a write error, got %d scrapes", got)
	}
}

func TestStreamMetricsHandlerSlowMountAmongManyFastMounts(t *testing.T) {
	t.Parallel()

	const numFastMounts = 100
	mm, fakeClientset := newStreamTestManager(t, true)
	mm.maximumNumberOfCollectors = numFastMounts + 1

	// A healthy but slightly slow sidecar is scraped first, while many mounts remain to be scraped.
	slowHandler := func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mountPayload(7)))
	}
	mounts := []fakeMount{{podUID: "uid-slow", podName: "pod-slow", volumeName: "vol-slow", handler: slowHandler}}
	for i := range numFastMounts {
		mounts = append(mounts, fakeMount{
			podUID:     fmt.Sprintf("uid-%d", i),
			podName:    fmt.Sprintf("pod-%d", i),
			volumeName: fmt.Sprintf("vol-%d", i),
			handler:    staticPayloadHandler(mountPayload(i+1), nil),
		})
	}
	registerFakeMounts(t, mm, fakeClientset, mounts)

	rec := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
	body := rec.Body.String()

	families, err := ProcessMetricsData(strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if !strings.Contains(body, `volume_name="vol-slow"`) {
		t.Errorf("expected metrics of the slow but healthy mount vol-slow in the streamed output")
	}
	if got := len(families["volume_fs_ops_count"].GetMetric()); got != numFastMounts+1 {
		t.Errorf("expected %d volume_fs_ops_count samples, got %d", numFastMounts+1, got)
	}
}

func TestStreamMetricsHandlerDropsTimestamps(t *testing.T) {
	t.Parallel()

	const timestampMs = "1700000000000"
	mm, fakeClientset := newStreamTestManager(t, true)
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(`# HELP fs_ops_count The cumulative number of ops processed.
# TYPE fs_ops_count counter
fs_ops_count{fs_op="Read"} 5 `+timestampMs+`
`, nil)},
	})

	for _, accept := range []string{"text/plain", `application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited`} {
		req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
		req.Header.Set("Accept", accept)
		rec := httptest.NewRecorder()
		mm.metricsHandler().ServeHTTP(rec, req)

		format := expfmt.ResponseFormat(rec.Header())
		dec := expfmt.NewDecoder(rec.Body, format)
		var found bool
		for {
			mf := &dto.MetricFamily{}
			if err := dec.Decode(mf); err != nil {
				if err != io.EOF {
					t.Fatalf("%s: failed to decode response: %v", accept, err)
				}
				break
			}
			for _, m := range mf.GetMetric() {
				found = true
				if m.TimestampMs != nil {
					t.Errorf("%s: expected no timestamp on %q, got %d", accept, mf.GetName(), m.GetTimestampMs())
				}
			}
		}
		if !found {
			t.Errorf("%s: expected at least one sample in the streamed output", accept)
		}
	}
}

func TestStreamMetricsHandlerMultiMountTextTypes(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(mountPayload(1), nil)},
		{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: staticPayloadHandler(mountPayload(2), nil)},
		{podUID: "uid-3", podName: "pod-3", volumeName: "vol-3", handler: staticPayloadHandler(mountPayload(3), nil)},
	})

	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	req.Header.Set("Accept", "text/plain")
	rec := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec, req)
	body := rec.Body.String()

	// The streamed text output repeats a sample group per mount for each family, so the samples of a
	// family are not contiguous. Verify the Prometheus text parser still accepts it with the correct types.
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to parse multi-mount streamed text output: %v\nbody:\n%s", err, body)
	}
	wantTypes := map[string]dto.MetricType{
		"volume_fs_ops_count":     dto.MetricType_COUNTER,
		"volume_fs_ops_latencies": dto.MetricType_HISTOGRAM,
	}
	for name, wantType := range wantTypes {
		mf, ok := families[name]
		if !ok {
			t.Fatalf("missing family %q in parsed output:\n%s", name, body)
		}
		if mf.GetType() != wantType {
			t.Errorf("family %q: got type %v, want %v", name, mf.GetType(), wantType)
		}
		if got := len(mf.GetMetric()); got != 3 {
			t.Errorf("family %q: got %d samples, want 3", name, got)
		}
	}
}

func TestStreamMetricsHandlerUniformlySlowMountsAllExported(t *testing.T) {
	t.Parallel()

	// With uniformly slow but healthy sidecars, each request attempts about the same number of mounts k
	// before the budget runs out. With a fixed starting mount, the same mounts would be skipped on every
	// request; varying the starting mount exports every mount across requests.
	for _, numMounts := range []int{3, 6} {
		t.Run(fmt.Sprintf("%d mounts", numMounts), func(t *testing.T) {
			t.Parallel()

			mm, fakeClientset := newStreamTestManager(t, true)
			mm.streamMetricsBudget = 300 * time.Millisecond
			mm.streamMountScrapeTimeout = 200 * time.Millisecond
			mm.streamStartIndex = cyclingStartIndex()

			slowHandler := func(opValue int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(120 * time.Millisecond):
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(mountPayload(opValue)))
				}
			}
			var mounts []fakeMount
			for i := range numMounts {
				mounts = append(mounts, fakeMount{
					podUID:     fmt.Sprintf("uid-%d", i),
					podName:    fmt.Sprintf("pod-%d", i),
					volumeName: fmt.Sprintf("vol-%d", i),
					handler:    slowHandler(i + 1),
				})
			}
			registerFakeMounts(t, mm, fakeClientset, mounts)

			seen := map[string]int{}
			for i := range 4 * numMounts {
				rec := httptest.NewRecorder()
				mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
				body := rec.Body.String()
				if _, err := ProcessMetricsData(strings.NewReader(body)); err != nil {
					t.Fatalf("scrape %d: failed to parse response: %v\nbody:\n%s", i, err, body)
				}
				for _, m := range mounts {
					if strings.Contains(body, `volume_name="`+m.volumeName+`"`) {
						seen[m.volumeName]++
					}
				}
			}

			for _, m := range mounts {
				if seen[m.volumeName] == 0 {
					t.Errorf("expected metrics of healthy mount %s in at least one scrape, seen counts: %v", m.volumeName, seen)
				}
			}
		})
	}
}

// slowVolumeResponseWriter wraps an http.ResponseWriter and sleeps once, on the first Write containing
// the samples of volumeName, to simulate the budget running out while that mount is being encoded.
type slowVolumeResponseWriter struct {
	http.ResponseWriter
	volumeName string
	delay      time.Duration
	slept      bool
}

func (s *slowVolumeResponseWriter) Write(p []byte) (int, error) {
	if !s.slept && bytes.Contains(p, []byte(`volume_name="`+s.volumeName+`"`)) {
		s.slept = true
		time.Sleep(s.delay)
	}
	return s.ResponseWriter.Write(p)
}

func TestStreamMetricsHandlerLastMountCompletedWhenBudgetRunsOut(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	// The budget is longer than the per-mount timeout, and the first mount is slow, so the last mount
	// is given less than the per-mount timeout.
	mm.streamMetricsBudget = 300 * time.Millisecond
	mm.streamMountScrapeTimeout = 200 * time.Millisecond

	slowHandler := func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mountPayload(1)))
	}
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: slowHandler},
		{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: staticPayloadHandler(mountPayload(2), nil)},
	})

	// The last mount's scrape finishes in time, but the budget runs out while it is being encoded.
	rec := httptest.NewRecorder()
	w := &slowVolumeResponseWriter{ResponseWriter: rec, volumeName: "vol-2", delay: 300 * time.Millisecond}
	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	req.Header.Set("Accept", "text/plain")
	mm.metricsHandler().ServeHTTP(w, req)
	body := rec.Body.String()

	if !w.slept {
		t.Fatalf("expected the budget to run out while encoding vol-2, body:\n%s", body)
	}
	families, err := ProcessMetricsData(strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to parse response: %v\nbody:\n%s", err, body)
	}
	for _, vol := range []string{"vol-1", "vol-2"} {
		if !strings.Contains(body, `volume_name="`+vol+`"`) {
			t.Errorf("expected metrics of %s in the streamed output, body:\n%s", vol, body)
		}
	}
	if got := len(families["volume_fs_ops_count"].GetMetric()); got != 2 {
		t.Errorf("expected 2 volume_fs_ops_count samples, got %d", got)
	}
}

func TestStreamMetricsHandlerHungMountWithShortBudget(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	// The budget is shorter than the per-mount timeout, e.g. a Prometheus scrape_timeout of about 5s, so
	// a hung mount always uses up the rest of the budget.
	mm.streamMetricsBudget = 300 * time.Millisecond
	if mm.streamMetricsBudget >= mm.perMountScrapeTimeout() {
		t.Fatalf("test requires a budget shorter than the per-mount timeout %v", mm.perMountScrapeTimeout())
	}
	mm.streamStartIndex = cyclingStartIndex()

	hungHandler := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(mountPayload(1), nil)},
		{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: staticPayloadHandler(mountPayload(2), nil)},
		{podUID: "uid-3", podName: "pod-3", volumeName: "vol-3", handler: hungHandler},
		{podUID: "uid-4", podName: "pod-4", volumeName: "vol-4", handler: staticPayloadHandler(mountPayload(4), nil)},
	})

	// Requests start at vol-1, vol-2, vol-3 and vol-4 in turn. Each request exports the healthy mounts
	// before the hung mount vol-3, which then uses up the rest of the budget.
	wantExported := [][]string{
		{"vol-1", "vol-2"},
		{"vol-2"},
		{},
		{"vol-4", "vol-1", "vol-2"},
	}
	for i, want := range wantExported {
		rec := httptest.NewRecorder()
		mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
		body := rec.Body.String()
		if _, err := ProcessMetricsData(strings.NewReader(body)); err != nil {
			t.Fatalf("scrape %d: failed to parse response: %v\nbody:\n%s", i, err, body)
		}
		var got []string
		for _, vol := range []string{"vol-1", "vol-2", "vol-3", "vol-4"} {
			if strings.Contains(body, `volume_name="`+vol+`"`) {
				got = append(got, vol)
			}
		}
		if diff := cmp.Diff(want, got, cmpopts.SortSlices(func(a, b string) bool { return a < b }), cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("scrape %d: exported mounts mismatch (-want +got):\n%s", i, diff)
		}
	}
}

func TestStreamMetricsHandlerExceedsServerWriteTimeout(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	// As in production, the streaming budget is longer than the server WriteTimeout, and the sequential
	// scrape takes longer than the WriteTimeout but fits within the budget. The response must still be
	// delivered in full, which relies on the handler extending the write deadline to match the budget.
	const writeTimeout = 1 * time.Second
	mm.streamMetricsBudget = 5 * time.Second

	slowHandler := func(opValue int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(700 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(mountPayload(opValue)))
		}
	}
	var mounts []fakeMount
	for i := 1; i <= 4; i++ {
		mounts = append(mounts, fakeMount{
			podUID:     fmt.Sprintf("uid-%d", i),
			podName:    fmt.Sprintf("pod-%d", i),
			volumeName: fmt.Sprintf("vol-%d", i),
			handler:    slowHandler(i),
		})
	}
	registerFakeMounts(t, mm, fakeClientset, mounts)

	srv := httptest.NewUnstartedServer(mm.metricsHandler())
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	defer srv.Close()

	for _, acceptEncoding := range []string{"identity", "gzip"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+metricsPath, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Accept-Encoding", acceptEncoding)

		start := time.Now()
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: metrics request failed: %v", acceptEncoding, err)
		}
		var bodyReader io.Reader = resp.Body
		if resp.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(resp.Body)
			if err != nil {
				resp.Body.Close()
				t.Fatalf("%s: failed to create gzip reader: %v", acceptEncoding, err)
			}
			bodyReader = gz
		}
		body, err := io.ReadAll(bodyReader)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: failed to read full response body: %v", acceptEncoding, err)
		}
		if elapsed := time.Since(start); elapsed <= writeTimeout {
			t.Fatalf("%s: response took %v, want longer than server WriteTimeout %v for this test to be meaningful", acceptEncoding, elapsed, writeTimeout)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: unexpected status code: %d", acceptEncoding, resp.StatusCode)
		}
		if _, err := ProcessMetricsData(bytes.NewReader(body)); err != nil {
			t.Fatalf("%s: failed to parse response: %v\nbody:\n%s", acceptEncoding, err, body)
		}
		for _, m := range mounts {
			if !strings.Contains(string(body), `volume_name="`+m.volumeName+`"`) {
				t.Errorf("%s: expected metrics of mount %s, got:\n%s", acceptEncoding, m.volumeName, body)
			}
		}
	}
}

func TestStreamMetricsHandlerOneScrapeAtATime(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	var inFlight, maxInFlight atomic.Int32
	slowHandler := func(opValue int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			cur := inFlight.Add(1)
			for {
				prev := maxInFlight.Load()
				if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
			inFlight.Add(-1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(mountPayload(opValue)))
		}
	}
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: slowHandler(1)},
		{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: slowHandler(2)},
	})

	const numRequests = 3
	bodies := make([]string, numRequests)
	codes := make([]int, numRequests)
	var wg sync.WaitGroup
	for i := range numRequests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}()
	}
	wg.Wait()

	if got := maxInFlight.Load(); got != 1 {
		t.Errorf("expected at most 1 sidecar scrape in flight across concurrent metrics requests, got %d", got)
	}
	for i := range numRequests {
		if codes[i] != http.StatusOK {
			t.Errorf("request %d: unexpected status code %d", i, codes[i])
		}
		for _, vol := range []string{"vol-1", "vol-2"} {
			if !strings.Contains(bodies[i], `volume_name="`+vol+`"`) {
				t.Errorf("request %d: expected metrics of %s, got:\n%s", i, vol, bodies[i])
			}
		}
	}
}

func TestStreamMetricsHandlerBudgetExhaustedWhileWaiting(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	mm.streamMetricsBudget = 200 * time.Millisecond
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(mountPayload(1), nil)},
	})

	// Simulate another streaming request holding the scrape slot for longer than the budget.
	mm.streamScrapeSlot <- struct{}{}
	defer func() { <-mm.streamScrapeSlot }()

	rec := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
	if strings.Contains(rec.Body.String(), `volume_name="vol-1"`) {
		t.Errorf("expected no metrics when the budget is exhausted while waiting, got:\n%s", rec.Body.String())
	}
}

// lockedFakeClientset serializes GetPod calls on a FakeClientset, whose GetPod mutates shared state.
type lockedFakeClientset struct {
	*clientset.FakeClientset
	mu sync.Mutex
}

func (c *lockedFakeClientset) GetPod(namespace, name string) (*corev1.Pod, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.FakeClientset.GetPod(namespace, name)
}

func TestAcceptsGzip(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		acceptEncoding string
		want           bool
	}{
		{acceptEncoding: "", want: false},
		{acceptEncoding: "identity", want: false},
		{acceptEncoding: "deflate, br", want: false},
		{acceptEncoding: "gzip", want: true},
		{acceptEncoding: "deflate, gzip", want: true},
		{acceptEncoding: "deflate, gzip;q=0.9", want: true},
		// q-values are ignored, as in older promhttp versions.
		{acceptEncoding: "gzip;q=0", want: true},
		{acceptEncoding: "x-gzip", want: false},
	}
	for _, tc := range testCases {
		header := http.Header{}
		if tc.acceptEncoding != "" {
			header.Set("Accept-Encoding", tc.acceptEncoding)
		}
		if got := acceptsGzip(header); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.acceptEncoding, got, tc.want)
		}
	}
}

func TestMetricsHandlerFlagSelection(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name              string
		streamMetrics     bool
		sequentialMetrics bool
		wantMaxInFlight   int32
	}{
		// Without --sequential-metrics-export, prometheus.Registry.Gather() scrapes mounts concurrently,
		// regardless of --stream-metrics-export.
		{name: "default", wantMaxInFlight: 2},
		{name: "stream-metrics-export only", streamMetrics: true, wantMaxInFlight: 2},
		// With --sequential-metrics-export, mounts are scraped one at a time, regardless of --stream-metrics-export.
		{name: "sequential-metrics-export only", sequentialMetrics: true, wantMaxInFlight: 1},
		{name: "both flags", streamMetrics: true, sequentialMetrics: true, wantMaxInFlight: 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fuseSocketDir, err := os.MkdirTemp("/tmp", "fs")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			t.Cleanup(func() { os.RemoveAll(fuseSocketDir) })
			fakeClientset := clientset.NewFakeClientset()
			// FakeClientset.GetPod is not safe for concurrent use, and Registry.Gather() calls it concurrently.
			mm := NewMetricsManager(":0", fuseSocketDir, 10, &lockedFakeClientset{FakeClientset: fakeClientset}, tc.streamMetrics, tc.sequentialMetrics, true).(*manager)

			var inFlight, maxInFlight atomic.Int32
			slowHandler := func(opValue int) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					cur := inFlight.Add(1)
					for {
						prev := maxInFlight.Load()
						if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
							break
						}
					}
					time.Sleep(200 * time.Millisecond)
					inFlight.Add(-1)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(mountPayload(opValue)))
				}
			}
			registerFakeMounts(t, mm, fakeClientset, []fakeMount{
				{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: slowHandler(1)},
				{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: slowHandler(2)},
			})

			rec := httptest.NewRecorder()
			mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status code %d", rec.Code)
			}
			for _, vol := range []string{"vol-1", "vol-2"} {
				if !strings.Contains(body, `volume_name="`+vol+`"`) {
					t.Errorf("expected metrics of %s, got:\n%s", vol, body)
				}
			}
			if got := maxInFlight.Load(); got != tc.wantMaxInFlight {
				t.Errorf("max concurrent sidecar scrapes = %d, want %d", got, tc.wantMaxInFlight)
			}
		})
	}
}
