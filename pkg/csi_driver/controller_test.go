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

package driver

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testVolumeID  = "test-volume-id"
	testNodeID    = "test-node-id"
	testPV        = "test-pv"
	testPVC       = "test-pvc"
	testNamespace = "test-namespace"
	testPod       = "test-pod"
	testImage     = "k8s.gcr.io/pause"
)

func initTestController(t *testing.T, clientset clientset.Interface) csi.ControllerServer {
	t.Helper()
	driver := initTestDriver(t, nil, clientset)
	cs, err := newControllerServer(driver, driver.config.StorageServiceManager, &GCSDriverFeatureOptions{FeatureGCSFuseProfiles: &FeatureGCSFuseProfiles{}})
	if err != nil {
		t.Fatalf("newControllerServer failed: %v", err)
	}
	return cs
}

func TestCreateVolume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		req       *csi.CreateVolumeRequest
		resp      *csi.CreateVolumeResponse
		expectErr error
	}{
		{
			name: "valid defaults",
			req: &csi.CreateVolumeRequest{
				Name: testVolumeID,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Secrets: map[string]string{
					"projectID":               "test-project",
					"serviceAccountName":      "test-sa-name",
					"serviceAccountNamespace": "test-sa-namespace",
				},
			},
			resp: &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					CapacityBytes: 1 * util.Mb,
					VolumeId:      testVolumeID,
				},
			},
		},
		{
			name: "empty name",
			req: &csi.CreateVolumeRequest{
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "CreateVolume name must be provided"),
		},
	}

	for _, test := range cases {
		cs := initTestController(t, clientset.NewFakeClientset())
		resp, err := cs.CreateVolume(context.TODO(), test.req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
		}
		if !reflect.DeepEqual(resp, test.resp) {
			t.Errorf("test %q failed:\ngot resp %+v,\nexpected resp %+v", test.name, resp, test.resp)
		}
	}
}

func TestDeleteVolume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		req       *csi.DeleteVolumeRequest
		resp      *csi.DeleteVolumeResponse
		expectErr error
	}{
		{
			name: "valid",
			req: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeID,
				Secrets: map[string]string{
					"projectID":               "test-project",
					"serviceAccountName":      "test-sa-name",
					"serviceAccountNamespace": "test-sa-namespace",
				},
			},
			resp: &csi.DeleteVolumeResponse{},
		},
		{
			name: "invalid id",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "abc",
				Secrets: map[string]string{
					"projectID":               "test-project",
					"serviceAccountName":      "test-sa-name",
					"serviceAccountNamespace": "test-sa-namespace",
				},
			},
			resp: &csi.DeleteVolumeResponse{},
		},
		{
			name:      "empty id",
			req:       &csi.DeleteVolumeRequest{},
			expectErr: status.Error(codes.InvalidArgument, "DeleteVolume volumeID must be provided"),
		},
	}

	for _, test := range cases {
		cs := initTestController(t, clientset.NewFakeClientset())
		resp, err := cs.DeleteVolume(context.TODO(), test.req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
		}
		if !reflect.DeepEqual(resp, test.resp) {
			t.Errorf("test %q failed:\ngot resp %+v,\nexpected resp %+v", test.name, resp, test.resp)
		}
	}
}

func TestControllerPublishVolume(t *testing.T) {
	oldInterval := mounterPodPollInterval
	mounterPodPollInterval = 10 * time.Millisecond
	defer func() { mounterPodPollInterval = oldInterval }()

	timeNow := metav1.Now()

	// Helper to create a mounter pod for initial state
	makeMounterPod := func(config *mounterPodConfig, deletionTimestamp *metav1.Time) *corev1.Pod {
		p := createMounterPodSpec(config)
		p.ObjectMeta.DeletionTimestamp = deletionTimestamp
		p.ResourceVersion = "1"
		return p
	}

	defaultMounterPodConfig := &mounterPodConfig{
		podName:   createMounterPodName(testNodeID, testVolumeID),
		namespace: testNamespace,
		nodeID:    testNodeID,
		image:     testImage,
	}

	cases := []struct {
		name               string
		req                *csi.ControllerPublishVolumeRequest
		wantPublishContext map[string]string
		setupFake          func() *clientset.FakeClientset
		podGetErr          error
		podCreateErr       error
		expectErr          bool
		expectErrCode      codes.Code
	}{
		{
			name: "empty volume ID - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         "",
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "empty node ID - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           "",
				VolumeCapability: testVolumeCapability,
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "empty volume capabilities - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: nil,
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "missing sharedMount - should return success",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    map[string]string{},
			},
			setupFake: func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr: false,
		},
		{
			name: "sharedMount false - should return success",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "false",
				},
			},
			setupFake: func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr: false,
		},
		{
			name: "sharedMount true - no pv found - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "true",
				},
			},
			setupFake:     func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "sharedMount true - claimRef nil - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "true",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePV(clientset.FakePVConfig{Name: testPV, VolumeHandle: testVolumeID, ClaimRef: nil})
				return fc
			},
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "sharedMount true - mounter pod created successfully - should return success",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "true",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePV(clientset.FakePVConfig{Name: testPV, VolumeHandle: testVolumeID, ClaimRef: &corev1.ObjectReference{
					Name:      testPVC,
					Namespace: testNamespace,
				}})
				return fc
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			expectErr: false,
		},
		{
			name: "sharedMount true - mounter pod already exists - should return success",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "true",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				existingPod := makeMounterPod(defaultMounterPodConfig, nil)
				fc := clientset.NewFakeClientset(existingPod)
				fc.CreatePod(clientset.FakePodConfig{
					NodeName: testNodeID,
					PodStatus: &corev1.PodStatus{
						Conditions: []corev1.PodCondition{
							{
								Type:   corev1.PodScheduled,
								Status: corev1.ConditionTrue,
							},
						},
					},
				})
				fc.CreatePV(clientset.FakePVConfig{Name: testPV, VolumeHandle: testVolumeID, ClaimRef: &corev1.ObjectReference{
					Name:      testPVC,
					Namespace: testNamespace,
				}})
				return fc
			},
			wantPublishContext: map[string]string{
				PublishContextKeyMounterPodNamespace: testNamespace,
				PublishContextKeyMounterPodName:      createMounterPodName(testNodeID, testVolumeID),
			},
			expectErr: false,
		},
		{
			name: "sharedMount true - mounter pod being deleted - should return Aborted error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "true",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				existingPod := makeMounterPod(defaultMounterPodConfig, &timeNow)
				fc := clientset.NewFakeClientset(existingPod)
				fc.CreatePV(clientset.FakePVConfig{Name: testPV, VolumeHandle: testVolumeID, ClaimRef: &corev1.ObjectReference{
					Name:      testPVC,
					Namespace: testNamespace,
				}})
				return fc
			},
			expectErr:     true,
			expectErrCode: codes.Aborted,
		},
		{
			name: "sharedMount true - mounter pod get error - should return Internal error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "true",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePV(clientset.FakePVConfig{Name: testPV, VolumeHandle: testVolumeID, ClaimRef: &corev1.ObjectReference{
					Name:      testPVC,
					Namespace: testNamespace,
				}})
				return fc
			},
			podGetErr:     errors.New("simulated get error"),
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "sharedMount true - mounter pod create error - should return Internal error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					"sharedMount": "true",
				},
			},
			setupFake: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePV(clientset.FakePVConfig{Name: testPV, VolumeHandle: testVolumeID, ClaimRef: &corev1.ObjectReference{
					Name:      testPVC,
					Namespace: testNamespace,
				}})
				return fc
			},
			podCreateErr:  errors.New("simulated create error"),
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fc := test.setupFake()
			s := initTestController(t, fc)

			fakeK8sClient := fc.K8sClient().(*fake.Clientset)

			if test.podGetErr != nil {
				fakeK8sClient.PrependReactor("get", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, test.podGetErr
				})
			}
			if test.podCreateErr != nil {
				fakeK8sClient.Fake.PrependReactor("create", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, test.podCreateErr
				})
			} else {
				fakeK8sClient.Fake.PrependReactor("create", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					createAction := action.(k8stesting.CreateAction)
					pod := createAction.GetObject().(*corev1.Pod)
					pod.Spec.NodeName = testNodeID
					pod.Status.Conditions = []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					}

					// Update the fakePod in clientset so GetPod returns it!
					fc.CreatePod(clientset.FakePodConfig{
						NodeName:  testNodeID,
						PodStatus: &pod.Status,
					})

					return false, nil, nil
				})
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resp, err := s.ControllerPublishVolume(ctx, test.req)

			if test.expectErr {
				if err == nil {
					t.Fatalf("Expected error code %v, got nil", test.expectErrCode)
				}
				st, ok := status.FromError(err)
				if !ok {
					// If not a status error, check if any error was expected
					if test.expectErrCode != codes.Unknown {
						t.Fatalf("Expected status error with code %v, got non-status error: %v", test.expectErrCode, err)
					}
				} else if st.Code() != test.expectErrCode {
					t.Errorf("Expected error code %v, got %v (error: %v)", test.expectErrCode, st.Code(), err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
				if !reflect.DeepEqual(test.wantPublishContext, resp.PublishContext) {
					t.Errorf("Expected publish context: %v, got: %v", test.wantPublishContext, resp.PublishContext)
				}
			}
		})
	}
}
