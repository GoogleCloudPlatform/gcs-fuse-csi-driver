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
	"os"
	"path/filepath"
	"sync"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
)

var testVolumeCapability = &csi.VolumeCapability{
	AccessType: &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{},
	},
	AccessMode: &csi.VolumeCapability_AccessMode{
		Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
	},
}

type nodeServerTestEnv struct {
	ns csi.NodeServer
	fm *mount.FakeMounter
}

func initTestNodeServer(t *testing.T) *nodeServerTestEnv {
	t.Helper()
	mounter := mount.NewFakeMounter([]mount.MountPoint{})
	driver := initTestDriver(t, mounter)
	s, _ := driver.config.StorageServiceManager.SetupService(context.TODO(), nil)
	if _, err := s.CreateBucket(context.Background(), &storage.ServiceBucket{Name: testVolumeID}); err != nil {
		t.Fatalf("failed to create the fake bucket: %v", err)
	}

	return &nodeServerTestEnv{
		ns: newNodeServer(driver, mounter),
		fm: mounter,
	}
}

func initTestNodeServerWithCustomClientset(t *testing.T, clientSet *clientset.FakeClientset, wiNodeLabelCheck bool) *nodeServerTestEnv {
	t.Helper()
	mounter := mount.NewFakeMounter([]mount.MountPoint{})
	driver := initTestDriverWithCustomNodeServer(t, mounter, clientSet, wiNodeLabelCheck)
	s, _ := driver.config.StorageServiceManager.SetupService(context.TODO(), nil)
	if _, err := s.CreateBucket(context.Background(), &storage.ServiceBucket{Name: testVolumeID}); err != nil {
		t.Fatalf("failed to create the fake bucket: %v", err)
	}

	return &nodeServerTestEnv{
		ns: newNodeServer(driver, mounter),
		fm: mounter,
	}
}

func TestNodePublishVolume(t *testing.T) {
	t.Parallel()
	defaultPerm := os.FileMode(0o750) + os.ModeDir

	// Setup mount target path
	tmpDir := "/tmp/var/lib/kubelet/pods/test-pod-id/volumes/kubernetes.io~csi/"
	if err := os.MkdirAll(tmpDir, defaultPerm); err != nil {
		t.Fatalf("failed to setup tmp dir path: %v", err)
	}
	base, err := os.MkdirTemp(tmpDir, "node-publish-")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	testTargetPath := filepath.Join(base, "mount")
	if err = os.MkdirAll(testTargetPath, defaultPerm); err != nil {
		t.Fatalf("failed to setup target path: %v", err)
	}
	defer os.RemoveAll(base)

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodePublishVolumeRequest
		expectedMount *mount.MountPoint
		expectErr     error
	}{
		{
			name:      "empty request",
			req:       &csi.NodePublishVolumeRequest{},
			expectErr: status.Error(codes.InvalidArgument, "NodePublishVolume target path must be provided"),
		},
		{
			name: "valid request not already mounted",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
			},
			expectedMount: &mount.MountPoint{Device: testVolumeID, Path: testTargetPath, Type: "fuse", Opts: []string{}},
		},
		{
			name:   "valid request already mounted",
			mounts: []mount.MountPoint{{Device: "/test-device", Path: testTargetPath}},
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
			},
			expectedMount: &mount.MountPoint{Device: "/test-device", Path: testTargetPath},
		},
		{
			name: "valid request with user mount options",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							MountFlags: []string{"foo", "bar"},
						},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
			},
			expectedMount: &mount.MountPoint{Device: testVolumeID, Path: testTargetPath, Type: "fuse", Opts: []string{"foo", "bar"}},
		},
		{
			name: "valid request read only",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
				Readonly:         true,
			},
			expectedMount: &mount.MountPoint{Device: testVolumeID, Path: testTargetPath, Type: "fuse", Opts: []string{"ro"}},
		},
		{
			name: "empty target path",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				VolumeCapability: testVolumeCapability,
			},
			expectErr: status.Error(codes.InvalidArgument, "NodePublishVolume target path must be provided"),
		},
		{
			name: "invalid volume capability",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
			expectErr: status.Error(codes.InvalidArgument, "volume capability must be provided"),
		},
	}

	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}
		_, err := testEnv.ns.NodePublishVolume(context.TODO(), test.req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
		}
		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
	}
}

func TestNodePublishVolumeWIDisabledOnNode(t *testing.T) {
	t.Parallel()
	defaultPerm := os.FileMode(0o750) + os.ModeDir
	// Setup mount target path
	tmpDir := "/tmp/var/lib/kubelet/pods/test-pod-id/volumes/kubernetes.io~csi/"
	if err := os.MkdirAll(tmpDir, defaultPerm); err != nil {
		t.Fatalf("failed to setup tmp dir path: %v", err)
	}
	base, err := os.MkdirTemp(tmpDir, "node-publish-")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	testTargetPath := filepath.Join(base, "mount")
	if err = os.MkdirAll(testTargetPath, defaultPerm); err != nil {
		t.Fatalf("failed to setup target path: %v", err)
	}
	defer os.RemoveAll(base)

	req := &csi.NodePublishVolumeRequest{
		VolumeId:         testVolumeID,
		TargetPath:       testTargetPath,
		VolumeCapability: testVolumeCapability,
	}

	cases := []struct {
		name                          string
		hostNetworkEnabledOnPod       bool
		workloadIdentityEnabledOnNode bool
		expectErr                     error
	}{
		{
			name:                          "workload identity is enabled on node + pod using hostnetwork",
			hostNetworkEnabledOnPod:       true,
			workloadIdentityEnabledOnNode: true,
		},
		{
			name:                          "workload identity is not enabled on node + pod is not using hostnetwork, expecting error",
			hostNetworkEnabledOnPod:       false,
			workloadIdentityEnabledOnNode: false,
			expectErr:                     status.Errorf(codes.FailedPrecondition, "Workload Identity Federation is not enabled on node. Please make sure this is enabled on both cluster and node pool level (https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity)"),
		},
		{
			name:                          "testcase3",
			hostNetworkEnabledOnPod:       false,
			workloadIdentityEnabledOnNode: true,
		},
		{
			name:                          "testcase4",
			hostNetworkEnabledOnPod:       true,
			workloadIdentityEnabledOnNode: false,
			// TODO: confirm if this case needs to throw an error (if hostnetwork requires node to have GKE Metadata server enabled) once hostnetwork feature is available for testing
		},
	}
	for _, test := range cases {
		fakeClientSet := &clientset.FakeClientset{}
		fakeClientSet.CreateNode( /* workloadIdentityEnabled */ clientset.FakeNodeConfig{IsWorkloadIdentityEnabled: test.workloadIdentityEnabledOnNode})
		fakeClientSet.CreatePod(clientset.FakePodConfig{HostNetworkEnabled: test.hostNetworkEnabledOnPod})
		testEnv := initTestNodeServerWithCustomClientset(t, fakeClientSet, true)

		_, err = testEnv.ns.NodePublishVolume(context.TODO(), req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
		}
	}

}

func TestNodeUnpublishVolume(t *testing.T) {
	t.Parallel()
	defaultPerm := os.FileMode(0o750) + os.ModeDir

	// Setup mount target path
	base, err := os.MkdirTemp("", "node-publish-")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	defer os.RemoveAll(base)
	testTargetPath := filepath.Join(base, "mount")
	if err = os.MkdirAll(testTargetPath, defaultPerm); err != nil {
		t.Fatalf("failed to setup target path: %v", err)
	}

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodeUnpublishVolumeRequest
		actions       []mount.FakeAction
		expectedMount *mount.MountPoint
		expectErr     error
	}{
		{
			name:   "successful unmount",
			mounts: []mount.MountPoint{{Device: testVolumeID, Path: testTargetPath}},
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
		},
		{
			name: "empty target path",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId: testVolumeID,
			},
			expectErr: status.Error(codes.InvalidArgument, "NodeUnpublishVolume target path must be provided"),
		},
		{
			name: "dir doesn't exist",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: "/node-unpublish-dir-not-exists",
			},
		},
		{
			name: "dir not mounted",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
		},
	}

	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}

		_, err = testEnv.ns.NodeUnpublishVolume(context.TODO(), test.req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
	}
}

func validateMountPoint(t *testing.T, name string, fm *mount.FakeMounter, e *mount.MountPoint) {
	t.Helper()
	if e == nil {
		if len(fm.MountPoints) != 0 {
			t.Errorf("test %q failed: got mounts %+v, expected none", name, fm.MountPoints)
		}

		return
	}

	if mLen := len(fm.MountPoints); mLen != 1 {
		t.Errorf("test %q failed: got %v mounts(%+v), expected %v", name, mLen, fm.MountPoints, 1)

		return
	}

	a := &fm.MountPoints[0]
	if a.Device != e.Device {
		t.Errorf("test %q failed: got device %q, expected %q", name, a.Device, e.Device)
	}
	if a.Path != e.Path {
		t.Errorf("test %q failed: got path %q, expected %q", name, a.Path, e.Path)
	}
	if a.Type != e.Type {
		t.Errorf("test %q failed: got type %q, expected %q", name, a.Type, e.Type)
	}

	less := func(a, b string) bool { return a > b }
	if diff := cmp.Diff(a.Opts, e.Opts, cmpopts.SortSlices(less)); diff != "" {
		t.Errorf("unexpected options args (-got, +want)\n%s", diff)
	}
}

func TestConcurrentMapWrites(t *testing.T) {
	t.Parallel()
	// Create a shared map for the test
	sharedVSS := util.NewVolumeStateStore()
	// Number of concurrent writes we want to simulate
	numWrites := 2000

	// Use a WaitGroup to wait for all goroutines to finish
	var wg sync.WaitGroup

	// Run concurrent tests manually using goroutines
	for i := range numWrites {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Simulate concurrent writes to the shared map
			sharedVSS.Store(string(rune(i)), &util.VolumeState{})
		}()
	}

	// Wait for all goroutines to finish
	wg.Wait()

	// validate correct number of writes occurred
	if int(sharedVSS.Size()) != numWrites {
		t.Errorf("expected %d entries in the map, got %d", numWrites, sharedVSS.Size())
	}
}

func TestNodePublishVolumeWILabelCheck(t *testing.T) {
	t.Parallel()
	defaultPerm := os.FileMode(0o750) + os.ModeDir
	// Setup mount target path
	tmpDir := "/tmp/var/lib/kubelet/pods/test-pod-id/volumes/kubernetes.io~csi/"
	if err := os.MkdirAll(tmpDir, defaultPerm); err != nil {
		t.Fatalf("failed to setup tmp dir path: %v", err)
	}
	base, err := os.MkdirTemp(tmpDir, "node-publish-")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	testTargetPath := filepath.Join(base, "mount")
	if err = os.MkdirAll(testTargetPath, defaultPerm); err != nil {
		t.Fatalf("failed to setup target path: %v", err)
	}
	defer os.RemoveAll(base)

	req := &csi.NodePublishVolumeRequest{
		VolumeId:         testVolumeID,
		TargetPath:       testTargetPath,
		VolumeCapability: testVolumeCapability,
	}

	cases := []struct {
		name                          string
		wiNodeLabelCheck              bool
		workloadIdentityEnabledOnNode bool
		expectErr                     error
	}{
		{
			name:                          "WI node label check is disabled, WI is disabled on node, should succeed",
			wiNodeLabelCheck:              false,
			workloadIdentityEnabledOnNode: false,
		},
		{
			name:                          "WI node label check is enabled, WI is disabled on node, should fail",
			wiNodeLabelCheck:              true,
			workloadIdentityEnabledOnNode: false,
			expectErr:                     status.Errorf(codes.FailedPrecondition, "Workload Identity Federation is not enabled on node. Please make sure this is enabled on both cluster and node pool level (https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity)"),
		},
		{
			name:                          "WI node label check is enabled, WI is enabled on node, should succeed",
			wiNodeLabelCheck:              true,
			workloadIdentityEnabledOnNode: true,
		},
	}
	for _, test := range cases {
		fakeClientSet := &clientset.FakeClientset{}
		fakeClientSet.CreateNode(clientset.FakeNodeConfig{IsWorkloadIdentityEnabled: test.workloadIdentityEnabledOnNode})
		fakeClientSet.CreatePod(clientset.FakePodConfig{HostNetworkEnabled: false})
		testEnv := initTestNodeServerWithCustomClientset(t, fakeClientSet, test.wiNodeLabelCheck)

		_, err = testEnv.ns.NodePublishVolume(context.TODO(), req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:got error %q, expected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:got error %q, expected error %q", test.name, err, test.expectErr)
		}
	}
}
