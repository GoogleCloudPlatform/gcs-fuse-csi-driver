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
	"reflect"
	"sync"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
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
	ns    csi.NodeServer
	fm    *mount.FakeMounter
	nwMgr *fakeNetworkManager
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
		ns:    newNodeServer(driver, mounter),
		fm:    mounter,
		nwMgr: driver.config.NetworkManager.(*fakeNetworkManager),
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
		ns:    newNodeServer(driver, mounter),
		fm:    mounter,
		nwMgr: driver.config.NetworkManager.(*fakeNetworkManager),
	}
}

func setupMountTarget(t *testing.T) (string, func()) {
	t.Helper()
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
	return testTargetPath, func() { defer os.RemoveAll(base) }
}

func TestNodePublishVolume(t *testing.T) {
	testTargetPath, cleanup := setupMountTarget(t)
	defer cleanup()

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
		t.Run(test.name, func(t *testing.T) {
			testEnv := initTestNodeServer(t)
			if test.mounts != nil {
				testEnv.fm.MountPoints = test.mounts
			}
			_, err := testEnv.ns.NodePublishVolume(context.TODO(), test.req)
			if test.expectErr == nil && err != nil {
				t.Errorf("got error %q, expected error nil", err)
			}
			if test.expectErr != nil && !errors.Is(err, test.expectErr) {
				t.Errorf("got error %q, expected error %q", err, test.expectErr)
			}
			validateMountPoint(t, testEnv.fm, test.expectedMount)
		})
	}
}

func TestNodePublishVolumeWIDisabledOnNode(t *testing.T) {
	t.Parallel()
	testTargetPath, cleanup := setupMountTarget(t)
	defer cleanup()

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

		_, err := testEnv.ns.NodePublishVolume(context.TODO(), req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
		}
	}

}

func TestNodePublishVolumeMultiNIC(t *testing.T) {
	testTargetPath, cleanup := setupMountTarget(t)
	defer cleanup()

	for _, tc := range []struct {
		name              string
		poisonedIP        string
		rules             []Rule
		routes            []Route
		rtTables          string
		req               *csi.NodePublishVolumeRequest
		expectedNewRules  []Rule
		expectedNewRoutes []Route
		expectedOpts      []string
	}{
		{
			name: "use nic 1",
			req: &csi.NodePublishVolumeRequest{
				VolumeContext: map[string]string{
					VolumeContextKeyMultiNICIndex: "1",
				},
			},
			expectedNewRules:  []Rule{{Source: "10.144.0.8/32", Table: 50}},
			expectedNewRoutes: []Route{{Device: "eth1", Gateway: "10.144.0.1", Table: 50}},
			expectedOpts:      []string{"gcs-fuse-numa-node=1", "experimental-local-socket-address=10.144.0.8"},
		},
		{
			name: "existing network",
			req: &csi.NodePublishVolumeRequest{
				VolumeContext: map[string]string{
					VolumeContextKeyMultiNICIndex: "1",
				},
			},
			rtTables:     "45 gcsfusecsi_eth1",
			rules:        []Rule{{Source: "10.144.0.8/32", Table: 45}},
			routes:       []Route{{Device: "eth1", Gateway: "10.144.0.1", Table: 45}},
			expectedOpts: []string{"gcs-fuse-numa-node=1", "experimental-local-socket-address=10.144.0.8"},
		},
		{
			name: "partial network",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
				VolumeContext: map[string]string{
					VolumeContextKeyMultiNICIndex: "1",
				},
			},
			rules:             []Rule{{Source: "10.144.0.8/32", Table: 50}},
			expectedNewRoutes: []Route{{Device: "eth1", Gateway: "10.144.0.1", Table: 50}},
			expectedOpts:      []string{"gcs-fuse-numa-node=1", "experimental-local-socket-address=10.144.0.8"},
		},
		{
			name: "invalid node",
			req: &csi.NodePublishVolumeRequest{
				VolumeContext: map[string]string{
					VolumeContextKeyMultiNICIndex: "3",
				},
			},
			expectedOpts: []string{},
		},
		{
			name:       "failed route",
			poisonedIP: "10.128.0.6",
			req: &csi.NodePublishVolumeRequest{
				VolumeContext: map[string]string{
					VolumeContextKeyMultiNICIndex: "0",
				},
			},
			expectedOpts: []string{},
		},
		{
			name: "source address override",
			req: &csi.NodePublishVolumeRequest{
				VolumeContext: map[string]string{
					VolumeContextKeyMultiNICIndex: "1",
					VolumeContextKeyMountOptions:  "experimental-local-socket-address=151.101.129.164",
				},
			},
			expectedNewRules:  []Rule{{Source: "10.144.0.8/32", Table: 50}},
			expectedNewRoutes: []Route{{Device: "eth1", Gateway: "10.144.0.1", Table: 50}},
			expectedOpts:      []string{"gcs-fuse-numa-node=1", "experimental-local-socket-address=151.101.129.164"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fakeClientSet := &clientset.FakeClientset{}
			fakeClientSet.CreateNode(clientset.FakeNodeConfig{})
			fakeClientSet.CreatePod(clientset.FakePodConfig{})
			testEnv := initTestNodeServerWithCustomClientset(t, fakeClientSet, false /*wiNodeLabelCheck*/)
			testEnv.nwMgr.poisonedIP = tc.poisonedIP
			testEnv.nwMgr.devices = []LinkDevice{
				{Name: "eth0", Driver: "gve", NumaNode: 0},
				{Name: "eth1", Driver: "gve", NumaNode: 1},
			}
			testEnv.nwMgr.rules = append([]Rule{}, tc.rules...)
			testEnv.nwMgr.routes = append([]Route{
				{Device: "eth0", Gateway: "10.128.0.1", Source: "10.128.0.6", Table: 254},
				{Device: "eth1", Gateway: "10.144.0.1", Source: "10.144.0.8", Table: 254},
			}, tc.routes...)
			testEnv.nwMgr.rtTables = tc.rtTables
			expectedRules := append([]Rule{}, testEnv.nwMgr.rules...)
			expectedRules = append(expectedRules, tc.expectedNewRules...)
			expectedRoutes := append([]Route{}, testEnv.nwMgr.routes...)
			expectedRoutes = append(expectedRoutes, tc.expectedNewRoutes...)

			tc.req.VolumeId = testVolumeID
			tc.req.TargetPath = testTargetPath
			tc.req.VolumeCapability = &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
				},
			}
			_, err := testEnv.ns.NodePublishVolume(context.TODO(), tc.req)
			if err != nil {
				t.Errorf("Expected no error but got %v", err)
			}
			if !reflect.DeepEqual(testEnv.nwMgr.routes, expectedRoutes) {
				t.Errorf("Bad routes, expected %+v, got %+v", expectedRoutes, testEnv.nwMgr.routes)
			}
			if !reflect.DeepEqual(testEnv.nwMgr.rules, expectedRules) {
				t.Errorf("Bad rules, expected %+v, got %+v", expectedRules, testEnv.nwMgr.rules)
			}
			validateMountPoint(t, testEnv.fm, &mount.MountPoint{
				Device: testVolumeID,
				Path:   testTargetPath,
				Type:   "fuse",
				Opts:   tc.expectedOpts,
			})
		})
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	t.Parallel()
	testTargetPath, cleanup := setupMountTarget(t)
	defer cleanup()

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
		t.Run(test.name, func(t *testing.T) {
			testEnv := initTestNodeServer(t)
			if test.mounts != nil {
				testEnv.fm.MountPoints = test.mounts
			}

			_, err := testEnv.ns.NodeUnpublishVolume(context.TODO(), test.req)
			if test.expectErr == nil && err != nil {
				t.Errorf("got error %q, expected error nil", err)
			}
			if test.expectErr != nil && !errors.Is(err, test.expectErr) {
				t.Errorf("got error %q, expected error %q", err, test.expectErr)
			}

			validateMountPoint(t, testEnv.fm, test.expectedMount)
		})
	}
}

func validateMountPoint(t *testing.T, fm *mount.FakeMounter, e *mount.MountPoint) {
	t.Helper()
	if e == nil {
		if len(fm.MountPoints) != 0 {
			t.Errorf("got mounts %+v, expected none", fm.MountPoints)
		}

		return
	}

	if mLen := len(fm.MountPoints); mLen != 1 {
		t.Errorf("got %v mounts(%+v), expected %v", mLen, fm.MountPoints, 1)

		return
	}

	a := &fm.MountPoints[0]
	if a.Device != e.Device {
		t.Errorf("got device %q, expected %q", a.Device, e.Device)
	}
	if a.Path != e.Path {
		t.Errorf("got path %q, expected %q", a.Path, e.Path)
	}
	if a.Type != e.Type {
		t.Errorf("got type %q, expected %q", a.Type, e.Type)
	}

	// Validate expected options are present in actual options.
	actualOpts := make(map[string]bool)
	for _, opt := range a.Opts {
		actualOpts[opt] = true
	}
	for _, expectedOpt := range e.Opts {
		if !actualOpts[expectedOpt] {
			t.Errorf("expected option %q not found in actual options %v", expectedOpt, a.Opts)
		}
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

	testTargetPath, cleanup := setupMountTarget(t)
	defer cleanup()

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

		_, err := testEnv.ns.NodePublishVolume(context.TODO(), req)
		if test.expectErr == nil && err != nil {
			t.Errorf("test %q failed:got error %q, expected error nil", test.name, err)
		}
		if test.expectErr != nil && !errors.Is(err, test.expectErr) {
			t.Errorf("test %q failed:got error %q, expected error %q", test.name, err, test.expectErr)
		}
	}
}

func TestNodePublishVolumeEnableKernelParamsFileFlag(t *testing.T) {
	t.Parallel()
	testTargetPath, cleanup := setupMountTarget(t)
	defer cleanup()
	req := &csi.NodePublishVolumeRequest{
		VolumeId:         testVolumeID,
		TargetPath:       testTargetPath,
		VolumeCapability: testVolumeCapability,
	}
	fakeClientSet := clientset.NewFakeClientset()
	// initTestDriverWithCustomNodeServer sets AssumeGoodSidecarVersion to true.
	testEnv := initTestNodeServerWithCustomClientset(t, fakeClientSet, false)

	_, err := testEnv.ns.NodePublishVolume(context.TODO(), req)

	if err != nil {
		t.Errorf("got error %q, expected error nil", err)
	}
	validateMountPoint(t, testEnv.fm, &mount.MountPoint{
		Device: testVolumeID,
		Path:   testTargetPath,
		Type:   "fuse",
		Opts:   []string{"enable-kernel-params-file-flag=true"},
	})
}
