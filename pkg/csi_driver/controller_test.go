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
	"errors"
	"fmt"
	"reflect"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
)

const (
	testVolumeID  = "test-volume-id"
	testNodeID    = "test-node-id"
	testPV        = "test-pv"
	testPVC       = "test-pvc"
	testNamespace = "test-namespace"
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
	t.Parallel()
	cases := []struct {
		name      string
		req       *csi.ControllerPublishVolumeRequest
		setupFake func() *clientset.FakeClientset
		expectErr error
	}{
		{
			name: "empty volume ID - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         "",
				NodeId:           testNodeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    map[string]string{},
			},
			setupFake: func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr: status.Error(codes.InvalidArgument, "ControllerPublishVolume Volume ID must be provided"),
		},
		{
			name: "empty node ID - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           "",
				VolumeCapability: testVolumeCapability,
				VolumeContext:    map[string]string{},
			},
			setupFake: func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr: status.Error(codes.InvalidArgument, "ControllerPublishVolume Node ID must be provided"),
		},
		{
			name: "empty volume capabilities - should return error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           testNodeID,
				VolumeCapability: nil,
				VolumeContext:    map[string]string{},
			},
			setupFake: func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr: status.Error(codes.InvalidArgument, "volume capability must be provided"),
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
			expectErr: nil,
		},
		{
			name: "sharedMount true - should return success",
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
					Name:      "test-pvc",
					Namespace: "test-namespace",
				}})
				return fc
			},
			expectErr: nil,
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
			setupFake: func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErr: status.Errorf(codes.Internal, "no pv found for volumeID: %q", testVolumeID),
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
			expectErr: status.Errorf(codes.Internal, "pv %q is not bound to any pvc", testPV),
		},
		{
			name: "sharedMount true - claimRef namespace empty - should return error",
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
					Namespace: "",
				}})
				return fc
			},
			expectErr: status.Error(codes.Internal, fmt.Sprintf("pv claimRef namespace and name can't be empty, namespace: %q, name: %q", "", testPVC)),
		},
		{
			name: "sharedMount true - claimRef name empty - should return error",
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
					Name:      "",
					Namespace: testNamespace,
				}})
				return fc
			},
			expectErr: status.Error(codes.Internal, fmt.Sprintf("pv claimRef namespace and name can't be empty, namespace: %q, name: %q", testNamespace, "")),
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
			expectErr: nil,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			clientset := test.setupFake()
			s := initTestController(t, clientset)
			_, err := s.ControllerPublishVolume(context.Background(), test.req)
			if !errors.Is(err, test.expectErr) {
				t.Errorf("Expected error: %v, got: %v", test.expectErr, err)
			}
		})
	}
}
