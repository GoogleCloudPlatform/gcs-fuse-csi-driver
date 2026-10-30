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
	"errors"
	"fmt"
	"io"
	"maps"
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
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/proto"
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
	cDummy := newMetricsCollector("", "", "", podUID, volumeName, nil, nil, false, false)
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
			fuseSocketDir := newSocketTempDir(t)

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
	// NewPrometheusMetricManager registers its counters with the global registry, so unregister them to
	// allow the test to run more than once (e.g. with -count).
	t.Cleanup(func() {
		prometheus.Unregister(pmm.syncPVCounter)
		prometheus.Unregister(pmm.syncPodCounter)
	})

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

func TestEmitMetricFamilySkipsInvalidMetricWithoutPanic(t *testing.T) {
	t.Parallel()

	strPtr := func(s string) *string { return &s }
	floatPtr := func(f float64) *float64 { return &f }
	counterType := dto.MetricType_COUNTER
	c := &metricsCollector{
		constLabels:                      map[string]string{"volume_name": "vol-1"},
		enableGcsFuseVolumeMetricsSchema: true,
	}
	mf := &dto.MetricFamily{
		Name: strPtr("fs_ops_count"),
		Help: strPtr("ops count"),
		Type: &counterType,
		Metric: []*dto.Metric{
			nil,
			// Missing Counter value sub-message: skipped without nil dereference.
			{Label: []*dto.LabelPair{{Name: strPtr("fs_op"), Value: strPtr("Read")}}},
			// Duplicate variable label name rejected by NewConstMetric: logged and skipped without panic.
			{
				Label: []*dto.LabelPair{
					{Name: strPtr("fs_op"), Value: strPtr("Read")},
					{Name: strPtr("fs_op"), Value: strPtr("Write")},
				},
				Counter: &dto.Counter{Value: floatPtr(1)},
			},
			// Valid sample with a nil label is still emitted.
			{
				Label:   []*dto.LabelPair{nil, {Name: strPtr("fs_op"), Value: strPtr("Open")}},
				Counter: &dto.Counter{Value: floatPtr(5)},
			},
		},
	}

	ch := make(chan prometheus.Metric, 4)
	c.emitMetricFamily(mf, ch)
	c.emitMetricFamily(nil, ch)
	close(ch)

	if got := len(ch); got != 1 {
		t.Fatalf("expected 1 valid metric emitted, got %d", got)
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
		for _, protoSidecar := range []bool{false, true} {
			t.Run(fmt.Sprintf("enableSchema=%v/protoSidecar=%v", enableSchema, protoSidecar), func(t *testing.T) {
				t.Parallel()

				mm, fakeClientset := newStreamTestManager(t, enableSchema)
				var tr inFlightTracker
				var scrapeOrderMu sync.Mutex
				var scrapeOrder []string

				var mounts []fakeMount
				for i, opVal := range []int{10, 20, 30} {
					volName := fmt.Sprintf("vol-%d", i+1)
					payload := mountPayload(opVal) + fmt.Sprintf("# HELP grpc_client_attempt_started Total number of RPC attempts started.\n# TYPE grpc_client_attempt_started counter\ngrpc_client_attempt_started{grpc_method=\"ReadObject\"} %d\n", opVal)
					serve := staticPayloadHandler(payload, nil)
					if protoSidecar {
						serve = negotiatingPayloadHandler(t, payload)
					}
					mounts = append(mounts, fakeMount{
						podUID:     fmt.Sprintf("uid-%d", i+1),
						podName:    fmt.Sprintf("pod-%d", i+1),
						volumeName: volName,
						handler: func(w http.ResponseWriter, r *http.Request) {
							done := tr.enter()
							time.Sleep(15 * time.Millisecond)
							scrapeOrderMu.Lock()
							scrapeOrder = append(scrapeOrder, volName)
							scrapeOrderMu.Unlock()
							done()
							serve(w, r)
						},
					})
				}
				registerFakeMounts(t, mm, fakeClientset, mounts)

				req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
				rec := httptest.NewRecorder()
				mm.metricsHandler().ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("unexpected HTTP status code: got %d, want %d", rec.Code, http.StatusOK)
				}

				if gotMax := tr.maxInFlight.Load(); gotMax != 1 {
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
				if counterMF.GetType() != dto.MetricType_COUNTER {
					t.Errorf("expected %q type COUNTER, got %v", expectedCounterFamily, counterMF.GetType())
				}
				if len(counterMF.GetMetric()) != 3 {
					t.Fatalf("expected 3 metrics in %q, got %d", expectedCounterFamily, len(counterMF.GetMetric()))
				}

				histMF, ok := families[expectedHistFamily]
				if !ok {
					t.Fatalf("missing expected histogram family %q in parsed families", expectedHistFamily)
				}
				if histMF.GetType() != dto.MetricType_HISTOGRAM {
					t.Errorf("expected %q type HISTOGRAM, got %v", expectedHistFamily, histMF.GetType())
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
}

func TestStreamMetricsHandlerGzipAndFaultTolerance(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{
			podUID: "uid-fail", podName: "pod-fail", volumeName: "vol-fail",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			podUID: "uid-ok", podName: "pod-ok", volumeName: "vol-ok",
			handler: staticPayloadHandler(mountPayload(77), nil),
		},
	})

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
	mm.UnregisterMetricsCollector("/mnt/vol-ok", "node-1", "uid-ok", "vol-ok")
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
	sw := &skipFirstLineWriter{w: &buf, skipNextLine: true}
	_, _ = sw.Write([]byte("# TYPE foo"))
	_, _ = sw.Write([]byte(" counter\nfoo_total 1\n"))
	_, _ = sw.Write([]byte("foo_total 2\n"))
	if got, want := buf.String(), "foo_total 1\nfoo_total 2\n"; got != want {
		t.Errorf("skipFirstLineWriter output = %q, want %q", got, want)
	}
	if sw.skipNextLine {
		t.Errorf("expected skipNextLine to be cleared after skipping a line")
	}

	// The zero value passes everything through.
	buf.Reset()
	sw = &skipFirstLineWriter{w: &buf}
	_, _ = sw.Write([]byte("# TYPE foo counter\n"))
	if got, want := buf.String(), "# TYPE foo counter\n"; got != want {
		t.Errorf("zero-value skipFirstLineWriter output = %q, want %q", got, want)
	}
}

// fakeMount describes a fake gcsfuse sidecar metrics endpoint registered with the metrics manager.
type fakeMount struct {
	podUID     string
	podName    string
	volumeName string
	handler    http.HandlerFunc
}

// registerFakeMounts starts a unix-socket HTTP server per mount, creates a running pod for the mounts,
// and registers the corresponding metrics collector with mm in the given order.
func registerFakeMounts(t *testing.T, mm *manager, fakeClientset *clientset.FakeClientset, mounts []fakeMount) {
	t.Helper()

	// FakeClientset holds a single pod, whose name and namespace GetPod overwrites with the requested
	// ones, so one running pod serves all mounts.
	fakeClientset.CreatePod(clientset.FakePodConfig{
		Name:      "pod",
		Namespace: "default",
		PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
	})
	for _, m := range mounts {
		emptyDirBasePath := newSocketTempDir(t)
		ln, err := net.Listen("unix", filepath.Join(emptyDirBasePath, SocketName))
		if err != nil {
			t.Fatalf("failed to listen on socket for %s: %v", m.volumeName, err)
		}
		srv := &http.Server{Handler: m.handler}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { srv.Close() })

		mm.RegisterMetricsCollector("/mnt/"+m.volumeName, "default", m.podName, "bucket-"+m.volumeName, "node-1", emptyDirBasePath, m.podUID, m.volumeName, "deployment", "app")
	}
}

// newSocketTempDir returns a new directory under /tmp that is removed when the test ends.
// Why: t.TempDir() paths can exceed the 108-byte sun_path limit of unix domain socket addresses.
func newSocketTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newStreamTestManager returns a manager with sequential metrics export and per-family streaming
// (--sequential-metrics-export and --stream-metrics-export) enabled, as deployed by default.
func newStreamTestManager(t *testing.T, enableSchema bool) (*manager, *clientset.FakeClientset) {
	t.Helper()

	return newStreamTestManagerWithOptions(t, enableSchema, true)
}

// newStreamTestManagerWithOptions returns a manager with sequential metrics export enabled, and
// per-family streaming (--stream-metrics-export) enabled according to streamMetrics.
func newStreamTestManagerWithOptions(t *testing.T, enableSchema, streamMetrics bool) (*manager, *clientset.FakeClientset) {
	t.Helper()

	fakeClientset := clientset.NewFakeClientset()
	mm := NewMetricsManager(":0", newSocketTempDir(t), 10, fakeClientset, streamMetrics, true, enableSchema).(*manager)
	// Scrape mounts in registration order by default, so that tests are deterministic.
	mm.sequentialStartIndex = func(int) int { return 0 }
	return mm, fakeClientset
}

// cyclingStartIndex returns a sequentialStartIndex function that starts the k-th request at mount k mod n,
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

// encodeFamilies encodes the families of the text-format payload, in name order, in the given format.
func encodeFamilies(t *testing.T, textPayload string, format expfmt.Format) []byte {
	t.Helper()
	families, err := ProcessMetricsData(strings.NewReader(textPayload))
	if err != nil {
		t.Fatalf("failed to parse payload: %v", err)
	}
	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, format)
	for _, name := range slices.Sorted(maps.Keys(families)) {
		if err := enc.Encode(families[name]); err != nil {
			t.Fatalf("failed to encode family %q: %v", name, err)
		}
	}
	if closer, ok := enc.(expfmt.Closer); ok {
		_ = closer.Close()
	}
	return buf.Bytes()
}

// negotiatingPayloadHandler mimics a sidecar that forwards the driver's Accept header to gcsfuse
// (promhttp) and gcsfuse's Content-Type back: the text-format payload is served in the protobuf
// delimited format if the Accept header negotiates it, and in the text format otherwise. The payload
// is encoded up front, so that encoding errors fail the test on the test goroutine.
func negotiatingPayloadHandler(t *testing.T, payload string) http.HandlerFunc {
	t.Helper()
	textFormat, protoFormat := expfmt.NewFormat(expfmt.TypeTextPlain), protoDelimFormat()
	textBody, protoBody := encodeFamilies(t, payload, textFormat), encodeFamilies(t, payload, protoFormat)
	return func(w http.ResponseWriter, r *http.Request) {
		format, body := textFormat, textBody
		if expfmt.Negotiate(r.Header).FormatType() == expfmt.TypeProtoDelim {
			format, body = protoFormat, protoBody
		}
		w.Header().Set(contentTypeHeader, string(format))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

// protoDelimFormat is the format a promhttp-backed sidecar replies with when the driver asks for the
// protobuf delimited format.
func protoDelimFormat() expfmt.Format {
	h := http.Header{}
	h.Set(acceptHeader, metricsAcceptHeader)
	return expfmt.Negotiate(h)
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

func hungHandler(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
	w.WriteHeader(http.StatusOK)
}

type inFlightTracker struct {
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func (tr *inFlightTracker) enter() func() {
	cur := tr.inFlight.Add(1)
	for {
		prev := tr.maxInFlight.Load()
		if cur <= prev || tr.maxInFlight.CompareAndSwap(prev, cur) {
			break
		}
	}
	return func() { tr.inFlight.Add(-1) }
}

func scrapeServerMetrics(t *testing.T, srv *httptest.Server, acceptEncoding string) (string, time.Duration) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+metricsPath, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Accept-Encoding", acceptEncoding)

	start := time.Now()
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("metrics request (%s) failed: %v", acceptEncoding, err)
	}
	defer resp.Body.Close()

	var bodyReader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("failed to create gzip reader: %v", err)
		}
		defer gz.Close()
		bodyReader = gz
	}
	body, err := io.ReadAll(bodyReader)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", resp.StatusCode)
	}
	if _, err := ProcessMetricsData(bytes.NewReader(body)); err != nil {
		t.Fatalf("failed to parse response: %v\nbody:\n%s", err, body)
	}
	return string(body), elapsed
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
			mm.sequentialStartIndex = tc.startIndex
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

	// The production default picks a random starting mount for every request. The probability that
	// some mount is never picked in 1000 requests is about 4 * (3/4)^1000, i.e. negligible.
	mm := NewMetricsManager(":0", "", 10, nil, false, true, true).(*manager)
	const numMounts = 4
	seen := map[int]bool{}
	for range 1000 {
		i := mm.sequentialStartIndex(numMounts)
		if i < 0 || i >= numMounts {
			t.Fatalf("sequentialStartIndex(%d) = %d, want a value in [0, %d)", numMounts, i, numMounts)
		}
		seen[i] = true
	}
	if len(seen) != numMounts {
		t.Errorf("expected every mount to be picked as the starting mount, got %v", seen)
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

// TestTruncatedMountScrape verifies that a mount whose response is cut off, by the sidecar aborting
// the connection or by the per-mount scrape timeout, never exports the family that was cut off, on
// both the sequential and the prometheus.Registry path, while the other mounts are still exported.
// Why: A text body and, without --stream-metrics-export, a protobuf body are parsed as a whole, so the
// whole mount is dropped. With --stream-metrics-export, the complete protobuf families decoded before
// the cut-off are exported. expfmt's text decoder, which the removed ProcessMetricsDataAsStream was
// built on, yielded the cut-off family (e.g. a histogram with missing buckets) before the error.
func TestTruncatedMountScrape(t *testing.T) {
	t.Parallel()

	const payload = `# HELP truncated_ops_count The cumulative number of ops processed.
# TYPE truncated_ops_count counter
truncated_ops_count{fs_op="Read"} 7
# HELP truncated_ops_latencies The cumulative distribution of file system operation latencies.
# TYPE truncated_ops_latencies histogram
truncated_ops_latencies_bucket{fs_op="Read",le="1"} 3
truncated_ops_latencies_bucket{fs_op="Read",le="+Inf"} 5
truncated_ops_latencies_sum{fs_op="Read"} 10
truncated_ops_latencies_count{fs_op="Read"} 5
`
	// The text body is cut off in the middle of the histogram, after the complete counter family.
	textFormat := expfmt.NewFormat(expfmt.TypeTextPlain)
	textPartial := []byte(payload[:strings.Index(payload, `truncated_ops_latencies_bucket{fs_op="Read",le="+Inf"}`)])
	// The protobuf body is cut off in the middle of the second MetricFamily message.
	protoFormat := protoDelimFormat()
	first := encodeFamilies(t, payload[:strings.Index(payload, "# HELP truncated_ops_latencies")], protoFormat)
	full := encodeFamilies(t, payload, protoFormat)
	if !bytes.HasPrefix(full, first) || len(full) <= len(first)+8 {
		t.Fatalf("unexpected encoding: first message is not a prefix of the full payload")
	}
	protoPartial := full[:len(first)+(len(full)-len(first))/2]

	writePartial := func(w http.ResponseWriter, format expfmt.Format, body []byte) {
		w.Header().Set(contentTypeHeader, string(format))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	abortAfter := func(format expfmt.Format, body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			writePartial(w, format, body)
			panic(http.ErrAbortHandler)
		}
	}
	hangAfter := func(format expfmt.Format, body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			writePartial(w, format, body)
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
	}

	testCases := []struct {
		name          string
		handler       http.HandlerFunc
		streamMetrics bool
		// sequentialOnly is set for cases that rely on the configurable per-mount scrape timeout of the
		// sequential path; the registry path always waits for the fixed mountScrapeTimeout.
		sequentialOnly bool
		// wantCounter reports whether the complete counter family before the cut-off is exported.
		wantCounter bool
	}{
		{name: "text aborted", handler: abortAfter(textFormat, textPartial), streamMetrics: true},
		{name: "text per-mount timeout", handler: hangAfter(textFormat, textPartial), streamMetrics: true, sequentialOnly: true},
		{name: "proto aborted without streaming", handler: abortAfter(protoFormat, protoPartial)},
		{name: "proto aborted with streaming", handler: abortAfter(protoFormat, protoPartial), streamMetrics: true, wantCounter: true},
	}
	for _, tc := range testCases {
		for _, sequential := range []bool{true, false} {
			if tc.sequentialOnly && !sequential {
				continue
			}
			t.Run(fmt.Sprintf("%s/sequential=%v", tc.name, sequential), func(t *testing.T) {
				t.Parallel()

				fakeClientset := clientset.NewFakeClientset()
				// FakeClientset.GetPod is not safe for concurrent use, and Registry.Gather() calls it concurrently.
				mm := NewMetricsManager(":0", newSocketTempDir(t), 10, &lockedFakeClientset{FakeClientset: fakeClientset}, tc.streamMetrics, sequential, true).(*manager)
				// The healthy mount is scraped first, since a hung mount uses up the remaining budget.
				mm.sequentialStartIndex = func(int) int { return 0 }
				mm.sequentialMountScrapeTimeout = 100 * time.Millisecond
				registerFakeMounts(t, mm, fakeClientset, []fakeMount{
					{podUID: "uid-2", podName: "pod-2", volumeName: "vol-2", handler: negotiatingPayloadHandler(t, mountPayload(20))},
					{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: tc.handler},
				})

				families := map[string]*dto.MetricFamily{}
				if sequential {
					rec := httptest.NewRecorder()
					mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
					var err error
					if families, err = ProcessMetricsData(strings.NewReader(rec.Body.String())); err != nil {
						t.Fatalf("failed to parse response: %v\nbody:\n%s", err, rec.Body.String())
					}
				} else {
					gathered, err := mm.registry.Gather()
					if err != nil {
						t.Fatalf("Gather failed: %v", err)
					}
					for _, mf := range gathered {
						families[mf.GetName()] = mf
					}
				}

				volumes := func(name string) []string {
					var vols []string
					for _, m := range families[name].GetMetric() {
						for _, lp := range m.GetLabel() {
							if lp.GetName() == "volume_name" {
								vols = append(vols, lp.GetValue())
							}
						}
					}
					return vols
				}
				if got := volumes("volume_fs_ops_count"); !slices.Equal(got, []string{"vol-2"}) {
					t.Errorf("expected metrics of healthy mount vol-2 only, got volumes %v", got)
				}
				if _, ok := families["volume_truncated_ops_latencies"]; ok {
					t.Errorf("expected the cut-off histogram family of mount vol-1 not to be exported, got %v", families["volume_truncated_ops_latencies"])
				}
				counter, ok := families["volume_truncated_ops_count"]
				if tc.wantCounter {
					if !ok || len(counter.GetMetric()) != 1 || counter.GetMetric()[0].GetCounter().GetValue() != 7 {
						t.Errorf("expected the complete counter family of mount vol-1 to be exported, got %v", counter)
					}
				} else if ok {
					t.Errorf("expected no families of the truncated mount vol-1, got %v", counter)
				}
			})
		}
	}
}

func TestCollectMetricFamiliesSkipsConflictingType(t *testing.T) {
	t.Parallel()

	family := func(metricType dto.MetricType, value float64) *dto.MetricFamily {
		return &dto.MetricFamily{
			Name:   proto.String("ops"),
			Type:   metricType.Enum(),
			Metric: []*dto.Metric{{Counter: &dto.Counter{Value: proto.Float64(value)}, Gauge: &dto.Gauge{Value: proto.Float64(value)}}},
		}
	}
	families := func(yield func(*dto.MetricFamily, error) bool) {
		for _, mf := range []*dto.MetricFamily{
			family(dto.MetricType_COUNTER, 1),
			family(dto.MetricType_GAUGE, 2),
			family(dto.MetricType_COUNTER, 3),
		} {
			if !yield(mf, nil) {
				return
			}
		}
	}

	collected, err := collectMetricFamilies(families)
	if err != nil {
		t.Fatalf("collectMetricFamilies failed: %v", err)
	}
	ops := collected["ops"]
	if ops.GetType() != dto.MetricType_COUNTER || len(ops.GetMetric()) != 2 {
		t.Fatalf("expected the 2 counter samples to be merged and the gauge skipped, got %v", ops)
	}
	if got := []float64{ops.GetMetric()[0].GetCounter().GetValue(), ops.GetMetric()[1].GetCounter().GetValue()}; !slices.Equal(got, []float64{1, 3}) {
		t.Errorf("expected counter values [1 3], got %v", got)
	}
}

// TestScrapeNegotiatesProtobuf verifies that the driver asks the sidecar for the protobuf delimited
// format, decodes it, and exports the same metrics as from a sidecar that only serves the text format.
func TestScrapeNegotiatesProtobuf(t *testing.T) {
	t.Parallel()

	payload := mountPayload(10) + "# HELP grpc_client_attempt_started Total number of RPC attempts started.\n# TYPE grpc_client_attempt_started counter\ngrpc_client_attempt_started{grpc_method=\"ReadObject\"} 10\n"

	for _, streamMetrics := range []bool{true, false} {
		t.Run(fmt.Sprintf("streamMetrics=%v", streamMetrics), func(t *testing.T) {
			t.Parallel()

			var gotAccept atomic.Pointer[string]
			var gotProto atomic.Int32
			negotiate := negotiatingPayloadHandler(t, payload)
			protoHandler := func(w http.ResponseWriter, r *http.Request) {
				accept := r.Header.Get(acceptHeader)
				gotAccept.Store(&accept)
				if expfmt.Negotiate(r.Header).FormatType() == expfmt.TypeProtoDelim {
					gotProto.Add(1)
				}
				negotiate(w, r)
			}

			scrape := func(t *testing.T, handler http.HandlerFunc) map[string]*dto.MetricFamily {
				t.Helper()
				mm, fakeClientset := newStreamTestManagerWithOptions(t, true, streamMetrics)
				registerFakeMounts(t, mm, fakeClientset, []fakeMount{
					{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: handler},
				})
				rec := httptest.NewRecorder()
				mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
				families, err := ProcessMetricsData(strings.NewReader(rec.Body.String()))
				if err != nil {
					t.Fatalf("failed to parse response: %v\nbody:\n%s", err, rec.Body.String())
				}
				return families
			}

			// A sidecar that ignores the Accept header replies with the text format.
			want := scrape(t, staticPayloadHandler(payload, nil))
			got := scrape(t, protoHandler)

			if accept := gotAccept.Load(); accept == nil || *accept != metricsAcceptHeader {
				t.Errorf("expected the sidecar to receive Accept %q, got %v", metricsAcceptHeader, accept)
			}
			if gotProto.Load() == 0 {
				t.Errorf("expected the Accept header to negotiate the protobuf delimited format")
			}
			if len(want) == 0 || len(got) != len(want) {
				t.Fatalf("expected %d families, got %d", len(want), len(got))
			}
			for name, wantMF := range want {
				if !proto.Equal(wantMF, got[name]) {
					t.Errorf("family %q mismatch:\nwant: %v\ngot:  %v", name, wantMF, got[name])
				}
			}
		})
	}
}

func TestStreamMetricsBudgetFor(t *testing.T) {
	t.Parallel()

	mm := &manager{sequentialMetricsBudget: 8 * time.Second}
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
		{name: "NaN header", header: "NaN", want: 8 * time.Second},
		{name: "infinite header", header: "Inf", want: 8 * time.Second},
		{name: "negative infinite header", header: "-Inf", want: 8 * time.Second},
		{name: "overflowing header", header: "1e20", want: 8 * time.Second},
		{name: "underflowing header", header: "1e-12", want: 8 * time.Second},
	}
	for _, tc := range tests {
		h := http.Header{}
		if tc.header != "" {
			h.Set(scrapeTimeoutHeader, tc.header)
		}
		if got := mm.sequentialMetricsBudgetFor(h); got != tc.want {
			t.Errorf("%s: sequentialMetricsBudgetFor() = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := (&manager{}).sequentialMetricsBudgetFor(http.Header{}); got != defaultSequentialMetricsBudget {
		t.Errorf("unset budget: got %v, want %v", got, defaultSequentialMetricsBudget)
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
	// The budget is shorter than the per-mount timeout, so the last mount's scrape is capped at the
	// remaining budget.
	mm.sequentialMetricsBudget = 250 * time.Millisecond
	if mm.sequentialMetricsBudget >= mm.perMountScrapeTimeout() {
		t.Fatalf("test requires a budget shorter than the per-mount timeout %v", mm.perMountScrapeTimeout())
	}
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(mountPayload(1), nil)},
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
	mm.sequentialMetricsBudget = 250 * time.Millisecond
	if mm.sequentialMetricsBudget >= mm.perMountScrapeTimeout() {
		t.Fatalf("test requires a budget shorter than the per-mount timeout %v", mm.perMountScrapeTimeout())
	}
	mm.sequentialStartIndex = cyclingStartIndex()

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
		start := time.Now()
		mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
		// The hung mount is cut off by the budget rather than by the longer per-mount timeout.
		if elapsed := time.Since(start); elapsed >= mm.perMountScrapeTimeout() {
			t.Errorf("scrape %d: took %v, want less than the per-mount timeout %v", i, elapsed, mm.perMountScrapeTimeout())
		}
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
	// As in production, the sequential budget is longer than the server WriteTimeout, and the sequential
	// scrape takes longer than the WriteTimeout but fits within the budget. The response must still be
	// delivered in full, which relies on the handler extending the write deadline to match the budget.
	const writeTimeout = 100 * time.Millisecond
	mm.sequentialMetricsBudget = 2 * time.Second

	slowHandler := func(opValue int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(80 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(mountPayload(opValue)))
		}
	}
	var mounts []fakeMount
	for i := 1; i <= 2; i++ {
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
		body, elapsed := scrapeServerMetrics(t, srv, acceptEncoding)
		if elapsed <= writeTimeout {
			t.Fatalf("%s: response took %v, want longer than server WriteTimeout %v for this test to be meaningful", acceptEncoding, elapsed, writeTimeout)
		}
		for _, m := range mounts {
			if !strings.Contains(body, `volume_name="`+m.volumeName+`"`) {
				t.Errorf("%s: expected metrics of mount %s, got:\n%s", acceptEncoding, m.volumeName, body)
			}
		}
	}
}

func TestStreamMetricsHandlerOneScrapeAtATime(t *testing.T) {
	t.Parallel()

	mm, fakeClientset := newStreamTestManager(t, true)
	var tr inFlightTracker
	slowHandler := func(opValue int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			done := tr.enter()
			time.Sleep(40 * time.Millisecond)
			done()
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

	if got := tr.maxInFlight.Load(); got != 1 {
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
	mm.sequentialMetricsBudget = 200 * time.Millisecond
	registerFakeMounts(t, mm, fakeClientset, []fakeMount{
		{podUID: "uid-1", podName: "pod-1", volumeName: "vol-1", handler: staticPayloadHandler(mountPayload(1), nil)},
	})

	// Simulate another sequential request holding the scrape slot for longer than the budget.
	mm.sequentialScrapeSlot <- struct{}{}
	defer func() { <-mm.sequentialScrapeSlot }()

	rec := httptest.NewRecorder()
	mm.metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, metricsPath, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
	if strings.Contains(rec.Body.String(), `volume_name="vol-1"`) {
		t.Errorf("expected no metrics when the budget is exhausted while waiting, got:\n%s", rec.Body.String())
	}
}

// lockedFakeClientset serializes GetPod calls on a FakeClientset, whose GetPod mutates and returns a
// shared pod, and returns a copy of the pod so that concurrent callers do not share it.
type lockedFakeClientset struct {
	*clientset.FakeClientset
	mu sync.Mutex
}

func (c *lockedFakeClientset) GetPod(namespace, name string) (*corev1.Pod, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pod, err := c.FakeClientset.GetPod(namespace, name)
	if err != nil || pod == nil {
		return pod, err
	}
	return pod.DeepCopy(), nil
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

			fakeClientset := clientset.NewFakeClientset()
			// FakeClientset.GetPod is not safe for concurrent use, and Registry.Gather() calls it concurrently.
			mm := NewMetricsManager(":0", newSocketTempDir(t), 10, &lockedFakeClientset{FakeClientset: fakeClientset}, tc.streamMetrics, tc.sequentialMetrics, true).(*manager)

			var tr inFlightTracker
			// Without --sequential-metrics-export, each scrape waits for the other one to start, which
			// only happens if the mounts are scraped concurrently. Otherwise, the first scrape runs into
			// the per-mount timeout and its mount is missing from the response.
			var arrivals atomic.Int32
			bothInFlight := make(chan struct{})
			slowHandler := func(opValue int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					done := tr.enter()
					if !tc.sequentialMetrics {
						if arrivals.Add(1) == 2 {
							close(bothInFlight)
						}
						select {
						case <-bothInFlight:
						case <-r.Context().Done():
						}
					}
					done()
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
			if got := tr.maxInFlight.Load(); got != tc.wantMaxInFlight {
				t.Errorf("max concurrent sidecar scrapes = %d, want %d", got, tc.wantMaxInFlight)
			}
		})
	}
}

func TestDecodeMetricFamiliesMaxSize(t *testing.T) {
	t.Parallel()

	largeHelp := strings.Repeat("a", maxMetricFamilyBytes+1024)
	mf := &dto.MetricFamily{
		Name: proto.String("fs_ops_count"),
		Help: proto.String(largeHelp),
		Type: dto.MetricType_COUNTER.Enum(),
		Metric: []*dto.Metric{
			{Counter: &dto.Counter{Value: proto.Float64(1)}},
		},
	}
	var buf bytes.Buffer
	if _, err := protodelim.MarshalTo(&buf, mf); err != nil {
		t.Fatalf("MarshalTo failed: %v", err)
	}

	var gotErr error
	for _, err := range decodeMetricFamilies(&buf) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if gotErr == nil {
		t.Fatal("expected oversized MetricFamily to fail decoding, got nil error")
	}
	var sizeErr *protodelim.SizeTooLargeError
	if !errors.As(gotErr, &sizeErr) {
		t.Errorf("expected *protodelim.SizeTooLargeError, got %v", gotErr)
	}
}
