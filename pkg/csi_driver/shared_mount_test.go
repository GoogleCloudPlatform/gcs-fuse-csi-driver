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
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/google/go-cmp/cmp"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
