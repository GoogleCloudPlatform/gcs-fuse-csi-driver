/*
Copyright 2018 The Kubernetes Authors.
Copyright 2026 Google LLC

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

package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/mount-utils"

	"github.com/google/go-cmp/cmp"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

var (
	testTmpVolume = corev1.Volume{
		Name: util.SidecarContainerTmpVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}

	testBuffVolume = corev1.Volume{
		Name: webhook.SidecarContainerBufferVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}

	testCacheVolume = corev1.Volume{
		Name: webhook.SidecarContainerCacheVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
)

func TestSharedMount(t *testing.T) {
	testCases := []struct {
		name          string
		volumeContext map[string]string
		expected      bool
	}{
		{
			name:          "sharedMount true - should return true",
			volumeContext: map[string]string{"sharedMount": "true"},
			expected:      true,
		},
		{
			name:          "sharedMount false - should return false",
			volumeContext: map[string]string{"sharedMount": "false"},
			expected:      false,
		},
		{
			name:          "sharedMount missing - should return false",
			volumeContext: map[string]string{},
			expected:      false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vc := tc.volumeContext
			mounter := mount.NewFakeMounter([]mount.MountPoint{})
			driver := initTestDriver(t, mounter, clientset.NewFakeClientset())
			result := driver.sharedMount(vc)
			if result != tc.expected {
				t.Errorf("Expected %v, but got %v", tc.expected, result)
			}
		})
	}
}

func TestWaitForMounterServer(t *testing.T) {
	// Do not enable t.Parallel to avoid global data races.
	oldInterval := mounterPodPollInterval
	mounterPodPollInterval = 10 * time.Millisecond
	defer func() { mounterPodPollInterval = oldInterval }()

	// Create a temporary directory for kubelet dir
	tmpDir := t.TempDir()

	mounterSocketDirValid := filepath.Join(tmpDir, "pods", testPod, "volumes", "kubernetes.io~empty-dir", util.SidecarContainerTmpVolumeName)
	if err := os.MkdirAll(mounterSocketDirValid, 0755); err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	validSocketFile := filepath.Join(mounterSocketDirValid, mounterPodSocketFile)
	if file, err := os.Create(validSocketFile); err != nil {
		t.Fatalf("failed to create socket file: %v", err)
	} else {
		file.Close()
	}

	podUIDWithoutSocket := "test-pod-uid-missing"
	mounterSocketDirMissing := filepath.Join(tmpDir, "pods", podUIDWithoutSocket, "volumes", "kubernetes.io~empty-dir", util.SidecarContainerTmpVolumeName)
	if err := os.MkdirAll(mounterSocketDirMissing, 0755); err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	cases := []struct {
		name         string
		podStatus    *corev1.PodStatus
		getPodErr    error
		podUID       string
		timeout      time.Duration
		expectErr    bool
		expectedCode codes.Code
	}{
		{
			name:      "pod running and socket exists - should return success",
			podStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
			podUID:    testPod,
			timeout:   200 * time.Millisecond,
			expectErr: false,
		},
		{
			name:         "unexpected pod phase - should return failure",
			podStatus:    &corev1.PodStatus{Phase: corev1.PodFailed},
			podUID:       testPod,
			timeout:      200 * time.Millisecond,
			expectErr:    true,
			expectedCode: codes.Internal,
		},
		{
			name:         "pod get fails - should return failure",
			getPodErr:    fmt.Errorf("simulated get pod error"),
			podUID:       testPod,
			timeout:      200 * time.Millisecond,
			expectErr:    true,
			expectedCode: codes.Internal,
		},
		{
			name:         "context times out, pod pending - should return failure",
			podStatus:    &corev1.PodStatus{Phase: corev1.PodPending},
			podUID:       testPod,
			timeout:      200 * time.Millisecond,
			expectErr:    true,
			expectedCode: codes.DeadlineExceeded,
		},
		{
			name:         "context times out, pod running but socket missing - should return failure",
			podStatus:    &corev1.PodStatus{Phase: corev1.PodRunning},
			podUID:       podUIDWithoutSocket,
			timeout:      200 * time.Millisecond,
			expectErr:    true,
			expectedCode: codes.DeadlineExceeded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := clientset.NewFakeClientset()
			if tc.podStatus != nil {
				fc.CreatePod(clientset.FakePodConfig{PodStatus: tc.podStatus})
			}
			if tc.getPodErr != nil {
				fc.GetPodErr = tc.getPodErr
			}

			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()

			emptyDirBasePath := func(uid string) string {
				return filepath.Join(tmpDir, "pods", uid, "volumes", "kubernetes.io~empty-dir", util.SidecarContainerTmpVolumeName)
			}
			err := waitForMounterServer(ctx, fc, testNamespace, testPod, tc.podUID, emptyDirBasePath)

			if tc.expectErr {
				if err == nil {
					t.Fatalf("Expected error, but got nil")
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected grpc status error, bu got: %v", err)
				} else if st.Code() != tc.expectedCode {
					t.Errorf("Expected error code %v, but got %v (error: %v)", tc.expectedCode, st.Code(), err)
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, but got: %v", err)
				}
			}
		})
	}
}

func TestCreateMounterPodName(t *testing.T) {
	testCases := []struct {
		name     string
		nodeID   string
		volumeID string
		expected string
	}{
		{
			name:     "Basic test - should generate name correctly",
			nodeID:   testNodeID,
			volumeID: testVolumeID,
			expected: fmt.Sprintf("%s-f4d1ad31ce3ffcfcada13d5fe95e0f8ddc801bf7", util.MounterPodNamePrefix),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := createMounterPodName(tc.nodeID, tc.volumeID)
			// Verify that the generated name matches the expected name
			if actual != tc.expected {
				t.Errorf("Expected pod name %q, but got %q", tc.expected, actual)
			}
			// Verify that the generated name contains the prefix and the hash
			if len(actual) <= len(util.MounterPodNamePrefix) || actual[:len(util.MounterPodNamePrefix)] != util.MounterPodNamePrefix {
				t.Errorf("Expected pod name to start with %q, but got %q", util.MounterPodNamePrefix, actual)
			}
			if len(actual) != len(util.MounterPodNamePrefix+"-")+40 { // SHA1 hash is 40 characters long
				t.Errorf("Expected pod name to have a 40-character SHA1 hash, but got %q", actual)
			}
		})
	}
}

func TestMounterPodImage(t *testing.T) {
	testCases := []struct {
		name        string
		pod         *corev1.Pod
		expected    string
		expectError bool
	}{
		{
			name:        "nil pod returns error",
			pod:         nil,
			expected:    "",
			expectError: true,
		},
		{
			name: "pod without mounter container returns error",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "other-container",
							Image: "nginx:latest",
						},
					},
				},
			},
			expected:    "",
			expectError: true,
		},
		{
			name: "pod with mounter container returns container image",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  util.MounterPodNamePrefix + "-123",
							Image: "gcr.io/gke-release/gcsfuse-csi-mounter:v1.0.0",
						},
					},
				},
			},
			expected:    "gcr.io/gke-release/gcsfuse-csi-mounter:v1.0.0",
			expectError: false,
		},
		{
			name: "pod with multiple containers returns correct mounter container image",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "sidecar",
							Image: "sidecar-image:v1",
						},
						{
							Name:  util.MounterPodNamePrefix + "-abc",
							Image: "mounter-image:v2",
						},
					},
				},
			},
			expected:    "mounter-image:v2",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mounterPodImage(tc.pod)
			if (err != nil) != tc.expectError {
				t.Errorf("mounterPodImage() error = %v, expectError %v", err, tc.expectError)
				return
			}
			if got != tc.expected {
				t.Errorf("mounterPodImage() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func sortVolumes(volumes []corev1.Volume) {
	sort.Slice(volumes, func(i, j int) bool {
		return volumes[i].Name < volumes[j].Name
	})
}

func TestCreateMounterPodSpec(t *testing.T) {
	// Default resources expected when no overrides are provided
	defaultResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceMemory:           resource.MustParse("768Mi"),
			corev1.ResourceCPU:              resource.MustParse("750m"),
			corev1.ResourceEphemeralStorage: resource.MustParse("15Gi"),
		},
		Limits: corev1.ResourceList{},
	}

	// Standard volume mounts expected in the container, IN THE EXACT ORDER as in createMounterPodSpec
	expectedVolumeMounts := []corev1.VolumeMount{
		{
			Name:             mounterPodMountDir,
			MountPath:        util.KubeletDir,
			MountPropagation: ptr.To(corev1.MountPropagationBidirectional),
		},
		{
			Name:      util.SidecarContainerTmpVolumeName,
			MountPath: util.SidecarContainerTmpVolumePath,
		},
		{
			Name:      webhook.SidecarContainerBufferVolumeName,
			MountPath: webhook.SidecarContainerBufferVolumeMountPath,
		},
		{
			Name:      webhook.SidecarContainerCacheVolumeName,
			MountPath: webhook.SidecarContainerCacheVolumeMountPath,
		},
	}

	// Kubelet HostPath volume always expected
	kubeletHostPathVolume := corev1.Volume{
		Name: mounterPodMountDir,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: util.KubeletDir,
				Type: ptr.To(corev1.HostPathDirectoryOrCreate),
			},
		},
	}

	// Custom volume for override test
	customBufferVolume := corev1.Volume{
		Name: webhook.SidecarContainerBufferVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
		},
	}
	customCacheVolume := corev1.Volume{
		Name: webhook.SidecarContainerCacheVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
		},
	}

	tests := []struct {
		name   string
		config *mounterPodConfig
		want   *corev1.Pod
	}{
		{
			name: "basic config - should include default volumes and resources",
			config: &mounterPodConfig{
				podName:            "my-mounter-pod",
				namespace:          "my-namespace",
				serviceAccountName: "my-ksa",
				nodeID:             "node-123",
				image:              "gcr.io/my-project/my-image:v1.0.0",
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-mounter-pod",
					Namespace: "my-namespace",
					Labels: map[string]string{
						"gke-gcsfuse/shared-mount": "true",
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "node-123",
						"kubernetes.io/os":       "linux",
					},
					ServiceAccountName: "my-ksa",
					PriorityClassName:  mounterPodPriorityClass,
					Containers: []corev1.Container{
						{
							Name:            util.MounterPodNamePrefix,
							Image:           "gcr.io/my-project/my-image:v1.0.0",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptr.To(true),
							},
							Resources:    defaultResources,
							VolumeMounts: expectedVolumeMounts,
						},
					},
					Volumes: []corev1.Volume{
						testBuffVolume,
						testCacheVolume,
						testTmpVolume,
						kubeletHostPathVolume,
					},
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
				},
			},
		},
		{
			name: "config with resource overrides - should merge resources",
			config: &mounterPodConfig{
				podName:            "my-mounter-pod",
				namespace:          "my-namespace",
				serviceAccountName: "my-ksa",
				nodeID:             "node-123",
				image:              "gcr.io/my-project/my-image:v1.0.0",
				resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1000m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2000m"),
					},
				},
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-mounter-pod",
					Namespace: "my-namespace",
					Labels: map[string]string{
						"gke-gcsfuse/shared-mount": "true",
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "node-123",
						"kubernetes.io/os":       "linux",
					},
					ServiceAccountName: "my-ksa",
					PriorityClassName:  mounterPodPriorityClass,
					Containers: []corev1.Container{
						{
							Name:            util.MounterPodNamePrefix,
							Image:           "gcr.io/my-project/my-image:v1.0.0",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptr.To(true),
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("1000m"),
									corev1.ResourceMemory:           resource.MustParse("1Gi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("15Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("2000m"),
								},
							},
							VolumeMounts: expectedVolumeMounts,
						},
					},
					Volumes: []corev1.Volume{
						testBuffVolume,
						testCacheVolume,
						testTmpVolume,
						kubeletHostPathVolume,
					},
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
				},
			},
		},
		{
			name: "config with volume overrides - should use overridden volumes",
			config: &mounterPodConfig{
				podName:            "my-mounter-pod",
				namespace:          "my-namespace",
				serviceAccountName: "my-ksa",
				nodeID:             "node-123",
				image:              "gcr.io/my-project/my-image:v1.0.0",
				volumes:            []corev1.Volume{customBufferVolume},
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-mounter-pod",
					Namespace: "my-namespace",
					Labels: map[string]string{
						"gke-gcsfuse/shared-mount": "true",
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "node-123",
						"kubernetes.io/os":       "linux",
					},
					ServiceAccountName: "my-ksa",
					PriorityClassName:  mounterPodPriorityClass,
					Containers: []corev1.Container{
						{
							Name:            util.MounterPodNamePrefix,
							Image:           "gcr.io/my-project/my-image:v1.0.0",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptr.To(true),
							},
							Resources:    defaultResources,
							VolumeMounts: expectedVolumeMounts,
						},
					},
					Volumes: []corev1.Volume{
						customBufferVolume, // Overridden
						testCacheVolume,
						testTmpVolume,
						kubeletHostPathVolume,
					},
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
				},
			},
		},
		{
			name: "config with cache volume overrides - should add cache-created-by-user label",
			config: &mounterPodConfig{
				podName:            "my-mounter-pod",
				namespace:          "my-namespace",
				serviceAccountName: "my-ksa",
				nodeID:             "node-123",
				image:              "gcr.io/my-project/my-image:v1.0.0",
				volumes:            []corev1.Volume{customCacheVolume},
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-mounter-pod",
					Namespace: "my-namespace",
					Labels: map[string]string{
						"gke-gcsfuse/shared-mount":          "true",
						"gke-gcsfuse/cache-created-by-user": "true",
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "node-123",
						"kubernetes.io/os":       "linux",
					},
					ServiceAccountName: "my-ksa",
					PriorityClassName:  mounterPodPriorityClass,
					Containers: []corev1.Container{
						{
							Name:            util.MounterPodNamePrefix,
							Image:           "gcr.io/my-project/my-image:v1.0.0",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptr.To(true),
							},
							Resources:    defaultResources,
							VolumeMounts: expectedVolumeMounts,
						},
					},
					Volumes: []corev1.Volume{
						testBuffVolume,
						customCacheVolume, // Overridden
						testTmpVolume,
						kubeletHostPathVolume,
					},
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
				},
			},
		},
		{
			name: "profilesEnabled true - should include profile volumes and volume mounts",
			config: &mounterPodConfig{
				podName:            "my-mounter-pod",
				namespace:          "my-namespace",
				serviceAccountName: "my-ksa",
				nodeID:             "node-123",
				image:              "gcr.io/my-project/my-image:v1.0.0",
				profilesEnabled:    true,
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-mounter-pod",
					Namespace: "my-namespace",
					Labels: map[string]string{
						"gke-gcsfuse/shared-mount": "true",
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "node-123",
						"kubernetes.io/os":       "linux",
					},
					ServiceAccountName: "my-ksa",
					PriorityClassName:  mounterPodPriorityClass,
					Containers: []corev1.Container{
						{
							Name:            util.MounterPodNamePrefix,
							Image:           "gcr.io/my-project/my-image:v1.0.0",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptr.To(true),
							},
							Resources: defaultResources,
							VolumeMounts: append(expectedVolumeMounts,
								webhook.EphemeralFileCacheVolumeMount,
								webhook.RamFileCacheVolumeMount,
							),
						},
					},
					Volumes: []corev1.Volume{
						testBuffVolume,
						testCacheVolume,
						testTmpVolume,
						kubeletHostPathVolume,
						webhook.EphemeralFileCacheVolume,
						webhook.RamFileCacheVolume,
					},
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Sort volumes in the 'want' spec for consistent comparison
			sortVolumes(tc.want.Spec.Volumes)

			got := createMounterPodSpec(tc.config)

			// Sort volumes in the 'got' spec
			sortVolumes(got.Spec.Volumes)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("createMounterPodSpec() returned an unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCreateMounterPod(t *testing.T) {
	ctx := context.Background()

	baseConfig := &mounterPodConfig{
		namespace: testNamespace,
		podName:   testPod,
		nodeID:    testNodeID,
		image:     testImage,
	}

	timeNow := metav1.Now()

	makePod := func(deletionTimestamp *metav1.Time) *corev1.Pod {
		p := createMounterPodSpec(baseConfig)
		p.ObjectMeta.DeletionTimestamp = deletionTimestamp
		p.ResourceVersion = "1" // Needed for some fake client operations
		return p
	}

	tests := []struct {
		name         string
		initialState []runtime.Object
		getErr       error
		createErr    error
		wantErr      bool
		wantCode     codes.Code
		wantCreates  int
	}{
		{
			name:         "pod does not exist - create pod successfully",
			initialState: []runtime.Object{},
			wantErr:      false,
			wantCreates:  1,
		},
		{
			name: "pod exists - no-op success",
			initialState: []runtime.Object{
				makePod(nil),
			},
			wantErr:     false,
			wantCreates: 0,
		},
		{
			name: "pod exists with deletion timestamp - return error",
			initialState: []runtime.Object{
				makePod(&timeNow),
			},
			wantErr:     true,
			wantCode:    codes.Aborted,
			wantCreates: 0,
		},
		{
			name:        "pod get fails with non-NotFound error - return error",
			getErr:      errors.New("simulated internal error"),
			wantErr:     true,
			wantCreates: 0,
		},
		{
			name:        "pod create fails with error - return error",
			createErr:   errors.New("simulated create failed"),
			wantErr:     true,
			wantCreates: 1, // Create was attempted
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// NewFakeClientset now initializes the standard fake client
			testClientset := clientset.NewFakeClientset(tc.initialState...)
			fakeK8sClient := testClientset.K8sClient().(*fake.Clientset)

			// Inject errors using reactors on the standard fake client
			if tc.getErr != nil {
				fakeK8sClient.PrependReactor("get", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, tc.getErr
				})
			}
			if tc.createErr != nil {
				fakeK8sClient.PrependReactor("create", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, tc.createErr
				})
			}

			err := createMounterPod(testClientset, ctx, baseConfig)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("createMounterPod(%v) succeeded unexpectedly, want error", baseConfig)
				}
				if tc.wantCode != codes.OK {
					st, ok := status.FromError(err)
					if !ok {
						t.Fatalf("createMounterPod(%v) returned non-status error %v, want status error with code %v", baseConfig, err, tc.wantCode)
					}
					if st.Code() != tc.wantCode {
						t.Errorf("createMounterPod(%v) returned error code %v, want %v (error: %v)", baseConfig, st.Code(), tc.wantCode, err)
					}
				}
			} else {
				if err != nil {
					t.Fatalf("createMounterPod(%v) failed unexpectedly: %v", baseConfig, err)
				}
			}

			// Verify create calls on the standard fake client
			actions := fakeK8sClient.Actions()
			gotCreates := 0
			for _, action := range actions {
				if action.Matches("create", "pods") {
					gotCreates++
				}
			}

			if gotCreates != tc.wantCreates {
				t.Errorf("createMounterPod(%v) made %d create calls, want %d", baseConfig, gotCreates, tc.wantCreates)
			}
		})
	}
}
func TestWaitForMounterPodScheduled(t *testing.T) {
	// Do not enable t.Parallel to avoid global data races.
	oldInterval := mounterPodPollInterval
	mounterPodPollInterval = 10 * time.Millisecond
	defer func() { mounterPodPollInterval = oldInterval }()

	testMounterPodName := createMounterPodName(testNodeID, testVolumeID)
	// Fast timeout to improve test speed.
	testContextTimeout := 200 * time.Millisecond
	cases := []struct {
		name       string
		expectErr  error
		podStatus  *corev1.PodStatus
		nodeName   string
		getPodErr  error
		initialPod *clientset.FakePodConfig
		patchPod   *clientset.FakePodConfig
		nodeID     string
	}{
		{
			name:      "scheduled to node - should return success",
			expectErr: nil,
			initialPod: &clientset.FakePodConfig{
				PodStatus: &corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
				},
				NodeName: testNodeID,
			},
			nodeID: testNodeID,
		},
		{
			name:      "scheduled to wrong node - should return internal error",
			expectErr: status.Errorf(codes.Internal, "mounter pod %s/%s expected to be scheduled to node %q, instead, was scheduled to node %q", testNamespace, testMounterPodName, testNodeID, "other-node"),
			initialPod: &clientset.FakePodConfig{
				PodStatus: &corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
				},
				NodeName: "other-node",
			},
			nodeID: testNodeID,
		},
		{
			name:      "scheduled to node with a delay - should return success",
			expectErr: nil,
			initialPod: &clientset.FakePodConfig{
				NodeName: testNodeID,
			},
			patchPod: &clientset.FakePodConfig{
				PodStatus: &corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
				},
				NodeName: testNodeID,
			},
			nodeID: testNodeID,
		},
		{
			name: "scheduled but nodeName missing - should return error",
			expectErr: status.Errorf(codes.DeadlineExceeded,
				"timeout waiting for mounter pod %s/%s to be scheduled to node %q: context deadline exceeded",
				testNamespace, testMounterPodName, testNodeID,
			),
			initialPod: &clientset.FakePodConfig{
				PodStatus: &corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			nodeID: testNodeID,
		},
		{
			name: "not scheduled but has nodeName - should return error",
			expectErr: status.Errorf(codes.DeadlineExceeded,
				"timeout waiting for mounter pod %s/%s to be scheduled to node %q: context deadline exceeded",
				testNamespace, testMounterPodName, testNodeID,
			),
			initialPod: &clientset.FakePodConfig{
				PodStatus: &corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionFalse,
						},
					},
				},
				NodeName: testNodeID,
			},
			nodeID: testNodeID,
		},
		{
			name:      "time out waiting for scheduled - should return DeadlineExceeded error",
			podStatus: &corev1.PodStatus{},
			expectErr: status.Errorf(codes.DeadlineExceeded,
				"timeout waiting for mounter pod %s/%s to be scheduled to node %q: context deadline exceeded",
				testNamespace, testMounterPodName, testNodeID,
			),
			nodeID: testNodeID,
		},
		{
			name:      "pod get internal error - should return Internal error",
			podStatus: &corev1.PodStatus{},
			getPodErr: status.Errorf(codes.Internal, "failed to get pod"),
			expectErr: status.Errorf(codes.Internal, "failed to get mounter pod %s/%s: %v", testNamespace, testMounterPodName, status.Errorf(codes.Internal, "failed to get pod")),
			nodeID:    testNodeID,
		},
		{
			name:      "pod not found - should time out and return DeadlineExceeded error",
			podStatus: &corev1.PodStatus{},
			getPodErr: k8serrors.NewNotFound(schema.GroupResource{}, "pod not found"),
			expectErr: status.Errorf(codes.DeadlineExceeded,
				"timeout waiting for mounter pod %s/%s to be scheduled to node %q: context deadline exceeded",
				testNamespace, testMounterPodName, testNodeID,
			),
			nodeID: testNodeID,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testContextTimeout)
			defer cancel()
			clientset := clientset.NewFakeClientset()

			if test.initialPod != nil {
				clientset.CreatePod(*test.initialPod)
			}

			if test.getPodErr != nil {
				clientset.GetPodErr = test.getPodErr
			}

			// Asynchronously wait for the pod so we can patch it while it polls.
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := waitForMounterPodScheduled(clientset, ctx, testNamespace, testMounterPodName, test.nodeID)
				if !errors.Is(err, test.expectErr) {
					t.Errorf("Expected error: %v, got: %v", test.expectErr, err)
				}
			}()

			if test.patchPod != nil {
				time.Sleep(15 * time.Millisecond)
				// CreatePod replaces the pod when it already exists, so it
				// acts like a patch.
				clientset.CreatePod(*test.patchPod)
			}

			wg.Wait()
		})
	}
}

func TestMounterPodTemplate(t *testing.T) {
	namespace := "test-ns"
	templateName := "my-pod-template"

	existingPodTemplate := clientset.FakePodTemplateConfig{
		Name: templateName,
	}

	testCases := []struct {
		name              string
		pvc               *corev1.PersistentVolumeClaim
		initialObjects    []clientset.FakePodTemplateConfig
		wantPodTemplate   *corev1.PodTemplate
		wantErr           bool
		getPodTemplateErr error
	}{
		{
			name: "pvc nil annotation - should return error",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pvc-no-anno",
					Namespace: namespace,
				},
			},
			wantPodTemplate: nil,
			wantErr:         true,
		},
		{
			name: "pvc unrelated annotation - should return error",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pvc-other-anno",
					Namespace: namespace,
					Annotations: map[string]string{
						"another.annotation/foo": "bar",
					},
				},
			},
			wantPodTemplate: nil,
			wantErr:         true,
		},
		{
			name: "pvc empty annotation - should return error",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pvc-empty-anno",
					Namespace: namespace,
					Annotations: map[string]string{
						"gke-gcsfuse/mounter-pod-template": "",
					},
				},
			},
			wantPodTemplate: nil,
			wantErr:         true,
		},
		{
			name: "pod template not found - should return error",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pvc-pt-notfound",
					Namespace: namespace,
					Annotations: map[string]string{
						"gke-gcsfuse/mounter-pod-template": "non-existent-template",
					},
				},
			},
			initialObjects:    []clientset.FakePodTemplateConfig{existingPodTemplate}, // Seed with a different one
			wantPodTemplate:   nil,
			wantErr:           true,
			getPodTemplateErr: errors.New("pod template not found"),
		},
		{
			name: "pod template found - should return pod template",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pvc-pt-found",
					Namespace: namespace,
					Annotations: map[string]string{
						"gke-gcsfuse/mounter-pod-template": templateName,
					},
				},
			},
			initialObjects: []clientset.FakePodTemplateConfig{existingPodTemplate},
			wantPodTemplate: &corev1.PodTemplate{ObjectMeta: metav1.ObjectMeta{
				Name: templateName,
			}},
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Initialize the fake clientset with any predefined objects
			fakeClientset := clientset.NewFakeClientset()

			if tc.getPodTemplateErr != nil {
				fakeClientset.GetPodTemplateErr = tc.getPodTemplateErr
			}

			for _, podTemplate := range tc.initialObjects {
				fakeClientset.CreatePodTemplate(podTemplate)
			}

			gotPodTemplate, err := mounterPodTemplate(fakeClientset, tc.pvc)

			if tc.wantErr {
				if err == nil {
					t.Errorf("mounterPodTemplate(%v): expected an error but got none", tc.pvc.Name)
				}
			} else {
				if err != nil {
					t.Errorf("mounterPodTemplate(%v): unexpected error: %v", tc.pvc.Name, err)
				}
			}

			if diff := cmp.Diff(tc.wantPodTemplate, gotPodTemplate); diff != "" {
				t.Errorf("mounterPodTemplate(%v): returned diff (-want +got):\n%s", tc.pvc.Name, diff)
			}
		})
	}
}

func TestDeleteMounterPod(t *testing.T) {
	oldInterval := mounterPodPollInterval
	mounterPodPollInterval = 10 * time.Millisecond
	defer func() { mounterPodPollInterval = oldInterval }()

	podName := "test-mounter-pod"
	namespace := "test-namespace"

	basePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            podName,
			Namespace:       namespace,
			ResourceVersion: "1",
		},
	}

	tests := []struct {
		name                  string
		initialObjects        []runtime.Object // Objects for the inner fake.Clientset
		deleteReactionError   error            // Error returned by the Delete reactor
		getPodShouldDisappear bool             // Whether custom GetPod should start returning NotFound after delete
		getPodPersistentError error            // Persistent error for custom GetPod

		wantErr      bool
		wantCode     codes.Code
		expectDelete bool // Whether a delete call is expected
	}{
		{
			name:                  "pod exists - should be deleted successfully",
			initialObjects:        []runtime.Object{basePod},
			getPodShouldDisappear: true, // Simulate NotFound after delete call
			expectDelete:          true,
			wantErr:               false,
		},
		{
			name:                  "pod does not exist - should return no-op success",
			initialObjects:        []runtime.Object{},
			deleteReactionError:   k8serrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, podName),
			getPodShouldDisappear: true, // GetPod should also reflect NotFound
			expectDelete:          true, // Delete is still called
			wantErr:               false,
		},
		{
			name:                "delete fails with internal error - should return Internal error",
			initialObjects:      []runtime.Object{basePod},
			deleteReactionError: k8serrors.NewInternalError(errors.New("simulated delete error")),
			expectDelete:        true,
			wantErr:             true,
			wantCode:            codes.Internal,
		},
		{
			name:                  "pod never disappears (timeout) - should return DeadlineExceeded error",
			initialObjects:        []runtime.Object{basePod},
			getPodShouldDisappear: false, // GetPod continues to return the pod
			expectDelete:          true,
			wantErr:               true,
			wantCode:              codes.DeadlineExceeded,
		},
		{
			name:                  "getPod fails during poll with internal error - should return Internal error",
			initialObjects:        []runtime.Object{basePod},
			getPodPersistentError: k8serrors.NewInternalError(errors.New("simulated get error")),
			expectDelete:          true,
			wantErr:               true,
			wantCode:              codes.Internal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond) // Short timeout for testing
			defer cancel()

			testClientset := clientset.NewFakeClientset(tc.initialObjects...)
			fakeK8sClient := testClientset.K8sClient().(*fake.Clientset)

			deleteCalled := false
			// Reactor for Delete
			fakeK8sClient.PrependReactor("delete", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
				deleteCalled = true
				if tc.deleteReactionError != nil {
					return true, nil, tc.deleteReactionError
				}

				// Simulate the effect on the custom GetPod
				if tc.getPodShouldDisappear {
					testClientset.GetPodErr = k8serrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, podName)
				}
				return true, nil, nil
			})

			// Configure initial state of custom GetPod
			if tc.getPodPersistentError != nil {
				testClientset.GetPodErr = tc.getPodPersistentError
			} else {
				found := false
				for _, obj := range tc.initialObjects {
					if pod, ok := obj.(*corev1.Pod); ok && pod.Name == podName && pod.Namespace == namespace {
						found = true
						break
					}
				}
				if !found {
					testClientset.GetPodErr = k8serrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, podName)
				} else {
					testClientset.GetPodErr = nil // Pod exists
				}
			}

			err := deleteMounterPod(ctx, testClientset, namespace, podName)

			if deleteCalled != tc.expectDelete {
				t.Errorf("Expected delete call: %v, got: %v", tc.expectDelete, deleteCalled)
			}

			if tc.wantErr {
				if err == nil {
					t.Fatalf("deleteMounterPod() succeeded unexpectedly, want error")
				}
				st, ok := status.FromError(err)
				if !ok {
					// Handle non-status errors if wantCode is not set
					if tc.wantCode == codes.OK {
						return
					}
					t.Fatalf("deleteMounterPod() returned non-status error %v, want status error with code %v", err, tc.wantCode)
				}
				if st.Code() != tc.wantCode {
					t.Errorf("deleteMounterPod() returned error code %v, want %v (error: %v)", st.Code(), tc.wantCode, err)
				}
			} else {
				if err != nil {
					t.Fatalf("deleteMounterPod() failed unexpectedly: %v", err)
				}
			}
		})
	}
}

func TestSetResource(t *testing.T) {
	tests := []struct {
		name         string
		initial      corev1.ResourceList
		override     corev1.ResourceList
		resourceName corev1.ResourceName
		want         corev1.ResourceList
	}{
		{
			name:         "override missing - no change",
			initial:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			override:     corev1.ResourceList{},
			resourceName: corev1.ResourceCPU,
			want:         corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		},
		{
			name:         "override is zero - delete resource",
			initial:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")},
			override:     corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")},
			resourceName: corev1.ResourceCPU,
			want:         corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
		},
		{
			name:         "override has value - update resource",
			initial:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			override:     corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			resourceName: corev1.ResourceCPU,
			want:         corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Deep copy initial map to avoid mutation issues between test iterations
			target := make(corev1.ResourceList)
			for k, v := range tc.initial {
				target[k] = v
			}

			setResource(&target, tc.override, tc.resourceName)

			if diff := cmp.Diff(tc.want, target); diff != "" {
				t.Errorf("setResource() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("nil target pointer does not panic", func(t *testing.T) {
		setResource(nil, corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}, corev1.ResourceCPU)
	})

	t.Run("pointer to nil map does not panic and initializes correctly", func(t *testing.T) {
		var target corev1.ResourceList // target map is nil
		setResource(&target, corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}, corev1.ResourceCPU)

		want := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}
		if diff := cmp.Diff(want, target); diff != "" {
			t.Errorf("setResource() with pointer to nil map returned unexpected diff (-want +got):\n%s", diff)
		}
	})
}

func TestMounterPodResources(t *testing.T) {
	defaultRequests := corev1.ResourceList{
		corev1.ResourceMemory:           resource.MustParse("768Mi"),
		corev1.ResourceCPU:              resource.MustParse("750m"),
		corev1.ResourceEphemeralStorage: resource.MustParse("15Gi"),
	}

	tests := []struct {
		name   string
		config *mounterPodConfig
		want   *corev1.ResourceRequirements
	}{
		{
			name: "no overrides (nil config resources) - should return defaults",
			config: &mounterPodConfig{
				resources: nil,
			},
			want: &corev1.ResourceRequirements{
				Requests: defaultRequests,
				Limits:   corev1.ResourceList{},
			},
		},
		{
			name: "override CPU and memory requests - should succeed",
			config: &mounterPodConfig{
				resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
			want: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory:           resource.MustParse("1Gi"),
					corev1.ResourceCPU:              resource.MustParse("1"),
					corev1.ResourceEphemeralStorage: resource.MustParse("15Gi"),
				},
				Limits: corev1.ResourceList{},
			},
		},
		{
			name: "set limits without changing requests - should succeed",
			config: &mounterPodConfig{
				resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			},
			want: &corev1.ResourceRequirements{
				Requests: defaultRequests,
				Limits: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
			},
		},
		{
			name: "override with zero - should delete default request",
			config: &mounterPodConfig{
				resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("0"),
					},
				},
			},
			want: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory:           resource.MustParse("768Mi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("15Gi"),
				},
				Limits: corev1.ResourceList{},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mounterPodResources(tc.config)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mounterPodResources() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMounterPodVolumes(t *testing.T) {
	// Expected HostPath volume for KubeletDir
	kubeletHostPathVolume := corev1.Volume{
		Name: mounterPodMountDir,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: util.KubeletDir,
				Type: ptr.To(corev1.HostPathDirectoryOrCreate),
			},
		},
	}

	// Custom volumes for override tests
	customBufferVolume := corev1.Volume{
		Name: webhook.SidecarContainerBufferVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
		},
	}
	customCacheVolume := corev1.Volume{
		Name: webhook.SidecarContainerCacheVolumeName,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/tmp/mycache"},
		},
	}

	testCases := []struct {
		name   string
		config *mounterPodConfig
		want   []corev1.Volume
	}{
		{
			name:   "no overrides - should return defaults plus hostpath",
			config: &mounterPodConfig{},
			want: []corev1.Volume{
				testTmpVolume,
				testBuffVolume,
				testCacheVolume,
				kubeletHostPathVolume,
			},
		},
		{
			name: "override buffer volume - should use custom buffer",
			config: &mounterPodConfig{
				volumes: []corev1.Volume{customBufferVolume},
			},
			want: []corev1.Volume{
				testTmpVolume,
				testCacheVolume,
				customBufferVolume,
				kubeletHostPathVolume,
			},
		},
		{
			name: "override cache volume - should use custom cache",
			config: &mounterPodConfig{
				volumes: []corev1.Volume{customCacheVolume},
			},
			want: []corev1.Volume{
				testTmpVolume,
				testBuffVolume,
				customCacheVolume,
				kubeletHostPathVolume,
			},
		},
		{
			name: "override buffer and cache - should use custom volumes",
			config: &mounterPodConfig{
				volumes: []corev1.Volume{customBufferVolume, customCacheVolume},
			},
			want: []corev1.Volume{
				testTmpVolume,
				customBufferVolume,
				customCacheVolume,
				kubeletHostPathVolume,
			},
		},
		{
			name: "unrelated volumes in config - should be included",
			config: &mounterPodConfig{
				volumes: []corev1.Volume{{Name: "extra-vol"}},
			},
			want: []corev1.Volume{
				testTmpVolume,
				testBuffVolume,
				testCacheVolume,
				{Name: "extra-vol"},
				kubeletHostPathVolume,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := mounterPodVolumes(tc.config)

			sortVolumes(got)
			sortVolumes(tc.want)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mounterPodVolumes() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}
