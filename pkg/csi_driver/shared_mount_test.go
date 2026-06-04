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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
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
			result := sharedMount(vc)
			if result != tc.expected {
				t.Errorf("Expected %v, but got %v", tc.expected, result)
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
			expected: fmt.Sprintf("%s-f4d1ad31ce3ffcfcada13d5fe95e0f8ddc801bf7", mounterPodNamePrefix),
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
			if len(actual) <= len(mounterPodNamePrefix) || actual[:len(mounterPodNamePrefix)] != mounterPodNamePrefix {
				t.Errorf("Expected pod name to start with %q, but got %q", mounterPodNamePrefix, actual)
			}
			if len(actual) != len(mounterPodNamePrefix+"-")+40 { // SHA1 hash is 40 characters long
				t.Errorf("Expected pod name to have a 40-character SHA1 hash, but got %q", actual)
			}
		})
	}
}

func TestPVFromVolumeID(t *testing.T) {
	// Helper to create the expected *corev1.PersistentVolume object for comparison
	makeExpectedPV := func(name, volumeHandle string) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
	}

	testCases := []struct {
		name      string
		volumeID  string
		setupFake func() *clientset.FakeClientset
		wantPV    *corev1.PersistentVolume
		wantErr   bool
	}{
		{
			name:     "PV found - should return it",
			volumeID: "csi-vol-001",
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePV(clientset.FakePVConfig{Name: "pv1", VolumeHandle: "csi-vol-000"})
				fc.CreatePV(clientset.FakePVConfig{Name: "pv2", VolumeHandle: "csi-vol-001"})
				fc.CreatePV(clientset.FakePVConfig{Name: "pv3", VolumeHandle: "csi-vol-002"})
				return fc
			},
			wantPV:  makeExpectedPV("pv2", "csi-vol-001"),
			wantErr: false,
		},
		{
			name:     "PV not found - should return error",
			volumeID: "csi-vol-999",
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePV(clientset.FakePVConfig{Name: "pv1", VolumeHandle: "csi-vol-000"})
				fc.CreatePV(clientset.FakePVConfig{Name: "pv2", VolumeHandle: "csi-vol-001"})
				return fc
			},
			wantErr: true,
		},
		{
			name:     "ListPV returns error - should return error",
			volumeID: "csi-vol-001",
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.ListPVErr = errors.New("failed to list PVs from apiserver")
				return fc
			},
			wantErr: true,
		},
		{
			name:     "Empty PV list - should return error",
			volumeID: "csi-vol-001",
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				// No PVs created
				return fc
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCS := tc.setupFake()

			gotPV, err := pvFromVolumeID(fakeCS, tc.volumeID)

			if tc.wantErr {
				if err == nil {
					t.Errorf("pvFromVolumeID(%q) succeeded unexpectedly, want error", tc.volumeID)
				}
				return
			}

			if err != nil {
				t.Fatalf("pvFromVolumeID(%q) returned an unexpected error: %v", tc.volumeID, err)
			}

			if diff := cmp.Diff(tc.wantPV, gotPV); diff != "" {
				t.Errorf("pvFromVolumeID(%q) returned unexpected diff (-want +got):\n%s", tc.volumeID, diff)
			}
		})
	}
}

func TestCreateMounterPodSpec(t *testing.T) {
	tests := []struct {
		name   string
		config *mounterPodConfig
		want   *corev1.Pod
	}{
		{
			name: "basic config - should succeed",
			config: &mounterPodConfig{
				podName:   "my-mounter-pod",
				namespace: "my-namespace",
				nodeID:    "node-123",
				image:     "gcr.io/my-project/my-image:v1.0.0",
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-mounter-pod",
					Namespace: "my-namespace",
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "node-123",
						"kubernetes.io/os":       "linux",
					},
					PriorityClassName: mounterPodPriorityClass,
					Containers: []corev1.Container{
						{
							Name:            mounterPodNamePrefix,
							Image:           "gcr.io/my-project/my-image:v1.0.0",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptr.To(true),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:             mounterPodMountDir,
									MountPath:        util.KubeletDir,
									MountPropagation: ptr.To(corev1.MountPropagationBidirectional),
								},
								{
									Name:      util.SidecarContainerTmpVolumeName,
									MountPath: util.SidecarContainerTmpVolumePath,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: mounterPodMountDir,
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: util.KubeletDir,
									Type: ptr.To(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: util.SidecarContainerTmpVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{
							Operator: corev1.TolerationOpExists,
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := createMounterPodSpec(tc.config)

			// Compare the got and want Pod specs using cmp.Diff
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("createMounterPodSpec(%v) returned an unexpected diff (-want +got):\n%s", tc.config, diff)
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
