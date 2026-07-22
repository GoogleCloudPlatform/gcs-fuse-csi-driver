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
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	mount "k8s.io/mount-utils"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/proto/mounter"
)

var testVolumeCapability = &csi.VolumeCapability{
	AccessType: &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{},
	},
	AccessMode: &csi.VolumeCapability_AccessMode{
		Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
	},
}

type fakeMounterServer struct {
	mounter.UnimplementedMounterServer
	req *mounter.MountRequest
}

func (f *fakeMounterServer) Mount(ctx context.Context, req *mounter.MountRequest) (*mounter.MountResponse, error) {
	f.req = req
	return &mounter.MountResponse{}, nil
}

func startFakeMounterServer(t *testing.T, socketFile string) *fakeMounterServer {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socketFile), 0755); err != nil {
		t.Fatalf("failed to create dir for socket: %v", err)
	}
	_ = os.Remove(socketFile)
	shortSock := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", shortSock)
	if err != nil {
		t.Fatalf("failed to listen on socket %q: %v", shortSock, err)
	}
	if err := os.Symlink(shortSock, socketFile); err != nil {
		t.Fatalf("failed to symlink %q to %q: %v", shortSock, socketFile, err)
	}
	srv := grpc.NewServer()
	fs := &fakeMounterServer{}
	mounter.RegisterMounterServer(srv, fs)
	go srv.Serve(l)
	t.Cleanup(func() {
		srv.Stop()
	})
	return fs
}

type nodeServerTestEnv struct {
	ns    csi.NodeServer
	fm    *mount.FakeMounter
	nwMgr *fakeNetworkManager
}

func initTestNodeServer(t *testing.T) *nodeServerTestEnv {
	return initTestNodeServerWithMounter(t, mount.NewFakeMounter([]mount.MountPoint{}))
}

func initTestNodeServerWithCustomClientset(t *testing.T, clientSet *clientset.FakeClientset, wiNodeLabelCheck bool) *nodeServerTestEnv {
	t.Helper()
	mounter := mount.NewFakeMounter([]mount.MountPoint{})
	driver := initTestDriverWithCustomNodeServer(t, mounter, clientSet, wiNodeLabelCheck)
	s, _ := driver.config.StorageServiceManager.SetupService(context.TODO(), nil, "")
	if _, err := s.CreateBucket(context.Background(), &storage.ServiceBucket{Name: testVolumeID}); err != nil {
		t.Fatalf("failed to create the fake bucket: %v", err)
	}

	return &nodeServerTestEnv{
		ns:    newNodeServer(driver, mounter),
		fm:    mounter,
		nwMgr: driver.config.NetworkManager.(*fakeNetworkManager),
	}
}

type fakeForceUnmounter struct {
	*mount.FakeMounter
	unmountWithForceFunc func(target string, umountTimeout time.Duration) error
}

func (f *fakeForceUnmounter) UnmountWithForce(target string, umountTimeout time.Duration) error {
	if f.unmountWithForceFunc != nil {
		return f.unmountWithForceFunc(target, umountTimeout)
	}
	return f.FakeMounter.Unmount(target)
}

func initTestNodeServerWithForceMounter(t *testing.T) (*nodeServerTestEnv, *fakeForceUnmounter) {
	fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})
	mounter := &fakeForceUnmounter{
		FakeMounter: fakeMounter,
	}
	return initTestNodeServerWithMounter(t, mounter), mounter
}

func initTestNodeServerWithMounter(t *testing.T, mounter mount.Interface) *nodeServerTestEnv {
	t.Helper()
	fakeClient := clientset.NewFakeClientset()
	fakeClient.CreatePV(clientset.FakePVConfig{
		Name:         "test-pv",
		VolumeHandle: testVolumeID,
		ClaimRef: &corev1.ObjectReference{
			Namespace: "test-ns",
			Name:      "test-pvc",
		},
	})
	var fakeMounter *mount.FakeMounter
	if fm, ok := mounter.(*mount.FakeMounter); ok {
		fakeMounter = fm
	} else if ffm, ok := mounter.(*fakeForceUnmounter); ok {
		fakeMounter = ffm.FakeMounter
	}

	driver := initTestDriver(t, fakeMounter, fakeClient)
	s, _ := driver.config.StorageServiceManager.SetupService(context.TODO(), nil, "")
	if _, err := s.CreateBucket(context.Background(), &storage.ServiceBucket{Name: testVolumeID}); err != nil {
		t.Fatalf("failed to create the fake bucket: %v", err)
	}

	return &nodeServerTestEnv{
		ns:    newNodeServer(driver, mounter),
		fm:    fakeMounter,
		nwMgr: driver.config.NetworkManager.(*fakeNetworkManager),
	}
}

func setupTestDir(t *testing.T, path string) (string, func()) {
	t.Helper()
	// Create a temporary directory for the test
	tempDir := t.TempDir()
	// Append the full path
	fullPath := filepath.Join(tempDir, path)
	// Create the directory path (including any parent directories)
	err := os.MkdirAll(fullPath, 0755)
	if err != nil {
		t.Fatalf("failed to create path: %v", err)
	}
	t.Logf("Created test path %q", fullPath)
	return fullPath, func() { os.RemoveAll(tempDir) }
}

func setupTestTargetPath(t *testing.T) (string, func()) {
	return setupTestDir(t, "/var/lib/kubelet/pods/test-pod-id/volumes/kubernetes.io~csi/node-publish/mount")
}
func setupTestStagingPath(t *testing.T) (string, func()) {
	return setupTestDir(t, "/var/lib/kubelet/plugins/kubernetes.io/csi/gcsfuse.csi.storage.gke.io/fake-pv/globalmount")
}

func createErrorFile(t *testing.T, emptyDirPath, content string) {
	t.Helper()
	if err := os.MkdirAll(emptyDirPath, 0755); err != nil {
		t.Fatalf("failed to create emptyDir path: %v", err)
	}
	errorFilePath := filepath.Join(emptyDirPath, util.ErrorFileName)
	if err := os.WriteFile(errorFilePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create error file: %v", err)
	}
}

func TestNodePublishVolume(t *testing.T) {
	testTargetPath, cleanup := setupTestTargetPath(t)
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
			validateMountPoint(t, testEnv.fm, test.expectedMount, nil)
		})
	}
}

func TestExecuteNodeStageVolume(t *testing.T) {
	t.Parallel()

	stagingPath, cleanup := setupTestStagingPath(t)
	defer cleanup()

	nodeID := "test-node" // default from initTestDriver
	volID := testVolumeID
	podNamespace := "test-ns"
	podName := createMounterPodName(nodeID, volID)

	kubeletDir := t.TempDir()

	// Create a temporary directory for mounter pod empty dir
	mounterSocketDirValid := filepath.Join(kubeletDir, "pods", podName, "volumes", "kubernetes.io~empty-dir", util.SidecarContainerTmpVolumeName)
	if err := os.MkdirAll(mounterSocketDirValid, 0755); err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	validSocketFile := filepath.Join(mounterSocketDirValid, MounterPodSocketFile)

	_ = startFakeMounterServer(t, validSocketFile)

	cases := []struct {
		name      string
		req       *csi.NodeStageVolumeRequest
		mounts    []mount.MountPoint
		podExists bool
		expectErr error
	}{
		{
			name: "mounter pod does not exist - should return FailedPrecondition error",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          volID,
				StagingTargetPath: stagingPath,
				PublishContext: map[string]string{
					PublishContextKeyMounterPodNamespace: podNamespace,
					PublishContextKeyMounterPodName:      podName,
				},
			},
			podExists: false,
			expectErr: status.Errorf(codes.FailedPrecondition, "mounter pod %s/%s expected to exist but was not found", podNamespace, podName),
		},
		{
			name: "mounter pod exists, not already mounted - should return success",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          volID,
				StagingTargetPath: stagingPath,
				VolumeContext: map[string]string{
					util.VolumeContextKeyPVName: volID,
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodNamespace: podNamespace,
					PublishContextKeyMounterPodName:      podName,
				},
			},
			podExists: true,
			expectErr: nil,
		},
		{
			name: "mounter pod exists, already mounted - should return success",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          volID,
				StagingTargetPath: stagingPath,
				VolumeContext: map[string]string{
					util.VolumeContextKeyPVName: volID,
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodNamespace: podNamespace,
					PublishContextKeyMounterPodName:      podName,
				},
			},
			mounts: []mount.MountPoint{
				{Path: stagingPath},
			},
			podExists: true,
			expectErr: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fakeClientSet *clientset.FakeClientset
			if tc.podExists {
				fakeClientSet = clientset.NewFakeClientset()
				fakeClientSet.CreatePod(clientset.FakePodConfig{
					UID:          types.UID(podName),
					PodStatus:    &corev1.PodStatus{Phase: corev1.PodRunning},
					IsMounterPod: true,
				})
			} else {
				fakeClientSet = clientset.NewFakeClientset()
				fakeClientSet.GetPodErr = apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, podName)
			}

			testEnv := initTestNodeServerWithCustomClientset(t, fakeClientSet, false)
			if tc.mounts != nil {
				testEnv.fm.MountPoints = tc.mounts
			}

			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}
			ns.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath = func(uid string) string {
				return filepath.Join(kubeletDir, "pods", uid, "volumes", "kubernetes.io~empty-dir", util.SidecarContainerTmpVolumeName)
			}

			// Clear the staging path so we can check whether os.MkdirAll was called.
			// Note: isDirMounted relies on a FakeMounter which simply reads the test case's
			// mounts array. It doesn't check the physical file system. This allows us to
			// delete the physical directory here without affecting the mount check.
			if err := os.RemoveAll(stagingPath); err != nil && !os.IsNotExist(err) {
				t.Fatalf("failed to remove staging target path: %v", err)
			}

			_, err := ns.executeNodeStageVolume(context.TODO(), tc.req)
			if tc.expectErr == nil && err != nil {
				t.Errorf("got error %q, expected error nil", err)
			}
			if tc.expectErr != nil {
				if err == nil {
					t.Errorf("got error nil, expected error %q", tc.expectErr)
				} else if !errors.Is(err, tc.expectErr) && err.Error() != tc.expectErr.Error() {
					t.Errorf("got error %q, expected error %q", err, tc.expectErr)
				}
			}

			// Check if staging path was recreated
			dirExists := false
			if _, statErr := os.Stat(stagingPath); statErr == nil {
				dirExists = true
			}
			isAlreadyMounted := len(tc.mounts) > 0
			if tc.expectErr == nil && tc.podExists {
				if isAlreadyMounted && dirExists {
					t.Errorf("expected MkdirAll to not be called (mount already exists), but directory was created")
				}
				if !isAlreadyMounted && !dirExists {
					t.Errorf("expected MkdirAll to be called, but directory was not created")
				}
			}
		})
	}
}

func TestNodePublishVolumeWIDisabledOnNode(t *testing.T) {
	t.Parallel()
	testTargetPath, cleanup := setupTestTargetPath(t)
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
	testTargetPath, cleanup := setupTestTargetPath(t)
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
			}, nil)
		})
	}
}
func TestNodePublishVolumeEnableAutoGoMemLimit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                     string
		enableAutoGoMemLimit     bool
		autoGoMemLimitRatio      float64
		assumeGoodSidecarVersion bool
		userMountOptions         string
		expectedOptions          []string
	}{
		{
			name:                     "feature flag enabled, sidecar version supported, no user overrides, expect all driver defaults",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			expectedOptions:          []string{"enable-auto-gomemlimit=true", "auto-gomemlimit-ratio=0.95"},
		},
		{
			name:                     "feature flag disabled, sidecar version supported, no user overrides, expect no driver defaults injected",
			enableAutoGoMemLimit:     false,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			expectedOptions:          []string{},
		},
		{
			name:                     "feature flag enabled, sidecar version not supported, no user overrides, expect no driver defaults injected",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: false,
			expectedOptions:          []string{},
		},
		{
			name:                     "feature flag enabled, sidecar version supported, user overrides enable flag, expect user enable flag and driver default ratio",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			userMountOptions:         "enable-auto-gomemlimit=false",
			expectedOptions:          []string{"enable-auto-gomemlimit=false", "auto-gomemlimit-ratio=0.95"},
		},
		{
			name:                     "feature flag enabled, sidecar version supported, user overrides ratio flag, expect driver default enable flag and user ratio",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			userMountOptions:         "auto-gomemlimit-ratio=0.8",
			expectedOptions:          []string{"auto-gomemlimit-ratio=0.8", "enable-auto-gomemlimit=true"},
		},
		{
			name:                     "feature flag enabled, sidecar version supported, user overrides both flags, expect no driver defaults injected",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			userMountOptions:         "enable-auto-gomemlimit=false,auto-gomemlimit-ratio=0.8",
			expectedOptions:          []string{"enable-auto-gomemlimit=false", "auto-gomemlimit-ratio=0.8"},
		},
		{
			// In this case, the sidecar will pass the flag to the gcsfuse binary, which will crash because it doesn't recognize it.
			// This is the correct behavior and how it works with all other flags today.
			name:                     "feature flag enabled, sidecar version not supported, user overrides enable flag, expect user enable flag and no driver defaults",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: false,
			userMountOptions:         "enable-auto-gomemlimit=false",
			expectedOptions:          []string{"enable-auto-gomemlimit=false"},
		},
		{
			// In this case, the sidecar will pass the flag to the gcsfuse binary, which will crash because it doesn't recognize it.
			// This is the correct behavior and how it works with all other flags today.
			name:                     "feature flag enabled, sidecar version not supported, user overrides ratio flag, expect user ratio flag and no driver defaults",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: false,
			userMountOptions:         "auto-gomemlimit-ratio=0.8",
			expectedOptions:          []string{"auto-gomemlimit-ratio=0.8"},
		},
		{
			name:                     "feature flag disabled, sidecar version supported, user overrides enable flag, expect user enable flag and driver default ratio",
			enableAutoGoMemLimit:     false,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			userMountOptions:         "enable-auto-gomemlimit=true",
			expectedOptions:          []string{"enable-auto-gomemlimit=true", "auto-gomemlimit-ratio=0.95"},
		},
		{
			name:                     "feature flag disabled, sidecar version supported, user overrides ratio flag, expect user ratio flag and no driver enable flag",
			enableAutoGoMemLimit:     false,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			userMountOptions:         "auto-gomemlimit-ratio=0.8",
			expectedOptions:          []string{"auto-gomemlimit-ratio=0.8"},
		},
		{
			name:                     "feature flag disabled, sidecar version supported, user overrides both flags, expect user flags and no driver defaults",
			enableAutoGoMemLimit:     false,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			userMountOptions:         "enable-auto-gomemlimit=true,auto-gomemlimit-ratio=0.8",
			expectedOptions:          []string{"enable-auto-gomemlimit=true", "auto-gomemlimit-ratio=0.8"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testTargetPath, cleanup := setupTestTargetPath(t)
			defer cleanup()

			volumeContext := map[string]string{
				VolumeContextKeyPodName:      "test-pod",
				VolumeContextKeyPodNamespace: "test-ns",
			}
			if tc.userMountOptions != "" {
				volumeContext[VolumeContextKeyMountOptions] = tc.userMountOptions
			}

			req := &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    volumeContext,
			}
			fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})

			driver := initTestDriver(t, fakeMounter, clientset.NewFakeClientset())
			s, _ := driver.config.StorageServiceManager.SetupService(context.TODO(), nil, "")
			if _, err := s.CreateBucket(context.Background(), &storage.ServiceBucket{Name: testVolumeID}); err != nil {
				t.Fatalf("failed to create the fake bucket: %v", err)
			}

			driver.config.FeatureOptions.GoMemLimitOptions = &GoMemLimitOptions{
				EnableAutoGoMemLimit: tc.enableAutoGoMemLimit,
				AutoGoMemLimitRatio:  tc.autoGoMemLimitRatio,
			}
			driver.config.AssumeGoodSidecarVersion = tc.assumeGoodSidecarVersion
			ns := newNodeServer(driver, fakeMounter)

			_, err := ns.NodePublishVolume(context.Background(), req)

			if err != nil {
				t.Fatalf("failed to publish volume: %v", err)
			}

			// 1. Use the existing helper to ensure all expected options are present
			validateMountPoint(t, fakeMounter, &mount.MountPoint{
				Device: testVolumeID,
				Path:   testTargetPath,
				Type:   "fuse",
				Opts:   tc.expectedOptions,
			}, nil)

			// 2. Exact match check: ensure no extra options were injected
			if len(fakeMounter.MountPoints) == 1 {
				actualOpts := fakeMounter.MountPoints[0].Opts
				actualSet := make(map[string]bool)
				for _, opt := range actualOpts {
					actualSet[opt] = true
				}

				var missingOpts []string
				for _, expectedOpt := range tc.expectedOptions {
					if !actualSet[expectedOpt] {
						missingOpts = append(missingOpts, expectedOpt)
					}
				}

				if len(missingOpts) > 0 {
					t.Errorf("Expected options %v to be present, but options %v are missing from actual options %v",
						tc.expectedOptions, missingOpts, actualOpts)
				}
			}
		})
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	t.Parallel()
	testTargetPath, cleanup := setupTestTargetPath(t)
	defer cleanup()

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodeUnpublishVolumeRequest
		actions       []mount.FakeAction
		unmountFunc   func(path string) error
		expectedMount *mount.MountPoint
		ctx           context.Context
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
		{
			name:   "retry unmount succeeds eventually",
			mounts: []mount.MountPoint{{Device: testVolumeID, Path: testTargetPath}},
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
			unmountFunc: func() func(path string) error {
				calls := 0
				return func(path string) error {
					calls++
					if calls < 3 {
						return fmt.Errorf("umount: %s: target is busy", path)
					}
					return nil
				}
			}(),
		},
		{
			name:   "retry unmount fails after max retries",
			mounts: []mount.MountPoint{{Device: testVolumeID, Path: testTargetPath}},
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
			unmountFunc: func(path string) error {
				return fmt.Errorf("umount: %s: target is busy", path)
			},
			expectedMount: &mount.MountPoint{Device: testVolumeID, Path: testTargetPath},
			ctx: func() context.Context {
				ctx, _ := context.WithTimeout(context.Background(), 500*time.Millisecond)
				return ctx
			}(),
			expectErr: status.Errorf(codes.Internal, "failed to unmount target path %q: umount: %s: target is busy", testTargetPath, testTargetPath),
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			testEnv := initTestNodeServer(t)
			if test.mounts != nil {
				testEnv.fm.MountPoints = test.mounts
			}
			if test.unmountFunc != nil {
				testEnv.fm.UnmountFunc = test.unmountFunc
			}

			ctx := context.TODO()
			if test.ctx != nil {
				ctx = test.ctx
			}

			_, err := testEnv.ns.NodeUnpublishVolume(ctx, test.req)
			if test.expectErr == nil && err != nil {
				t.Errorf("got error %q, expected error nil", err)
			}
			if test.expectErr != nil && !errors.Is(err, test.expectErr) {
				t.Errorf("got error %q, expected error %q", err, test.expectErr)
			}

			validateMountPoint(t, testEnv.fm, test.expectedMount, nil)
		})
	}
}

func TestNodeUnpublishVolumeForceUnmount(t *testing.T) {
	t.Parallel()
	testTargetPath, cleanup := setupTestTargetPath(t)
	defer cleanup()

	cases := []struct {
		name                 string
		mounts               []mount.MountPoint
		req                  *csi.NodeUnpublishVolumeRequest
		unmountWithForceFunc func(target string, umountTimeout time.Duration) error
		expectedMount        *mount.MountPoint
		ctx                  context.Context
		expectErr            error
	}{
		{
			name:   "retry force unmount succeeds eventually",
			mounts: []mount.MountPoint{{Device: testVolumeID, Path: testTargetPath}},
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
			unmountWithForceFunc: func() func(target string, umountTimeout time.Duration) error {
				calls := 0
				return func(target string, umountTimeout time.Duration) error {
					calls++
					if calls < 3 {
						return fmt.Errorf("umount: %s: target is busy", target)
					}
					return nil
				}
			}(),
		},
		{
			name:   "retry force unmount fails after max retries",
			mounts: []mount.MountPoint{{Device: testVolumeID, Path: testTargetPath}},
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
			unmountWithForceFunc: func(target string, umountTimeout time.Duration) error {
				return fmt.Errorf("umount: %s: target is busy", target)
			},
			expectedMount: &mount.MountPoint{Device: testVolumeID, Path: testTargetPath},
			ctx: func() context.Context {
				ctx, _ := context.WithTimeout(context.Background(), 500*time.Millisecond)
				return ctx
			}(),
			expectErr: status.Errorf(codes.Internal, "failed to force unmount target path %q: umount: %s: target is busy", testTargetPath, testTargetPath),
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			testEnv, forceMounter := initTestNodeServerWithForceMounter(t)
			if test.mounts != nil {
				testEnv.fm.MountPoints = test.mounts
			}
			if test.unmountWithForceFunc != nil {
				forceMounter.unmountWithForceFunc = test.unmountWithForceFunc
			}

			ctx := context.TODO()
			if test.ctx != nil {
				ctx = test.ctx
			}

			_, err := testEnv.ns.NodeUnpublishVolume(ctx, test.req)
			if test.expectErr == nil && err != nil {
				t.Errorf("got error %q, expected error nil", err)
			}
			if test.expectErr != nil && !errors.Is(err, test.expectErr) {
				t.Errorf("got error %q, expected error %q", err, test.expectErr)
			}

			validateMountPoint(t, testEnv.fm, test.expectedMount, nil)
		})
	}
}

func TestNodeStageVolume(t *testing.T) {
	t.Parallel()

	stagingPath, cleanup := setupTestStagingPath(t)
	defer cleanup()
	nodeID := "test-node" // default from initTestDriver
	volID := testVolumeID
	podName := createMounterPodName(nodeID, volID)

	kubeletDir := t.TempDir()

	// Create a temporary directory for mounter pod empty dir
	mounterSocketDirValid := filepath.Join(kubeletDir, "pods", podName, "volumes", "kubernetes.io~empty-dir", util.SidecarContainerTmpVolumeName)
	if err := os.MkdirAll(mounterSocketDirValid, 0755); err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	validSocketFile := filepath.Join(mounterSocketDirValid, MounterPodSocketFile)

	fakeServer := startFakeMounterServer(t, validSocketFile)

	cases := []struct {
		name                 string
		req                  *csi.NodeStageVolumeRequest
		expectErr            error
		profilesEnabled      bool
		pvConfig             *clientset.FakePVConfig
		scConfig             *clientset.FakeSCConfig
		expectedMountOptions []string
	}{
		{
			name:      "empty request",
			req:       nil,
			expectErr: status.Error(codes.InvalidArgument, "NodeStageVolume Request cannot be nil"),
		},
		{
			name: "empty volume id",
			req: &csi.NodeStageVolumeRequest{
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
			},
			expectErr: status.Error(codes.InvalidArgument, "NodeStageVolume Volume ID must be provided"),
		},
		{
			name: "missing volume capability",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
			},
			expectErr: status.Error(codes.InvalidArgument, "NodeStageVolume Volume capability must be provided"),
		},
		{
			name: "invalid volume capability",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_UNKNOWN,
					},
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "driver does not support access mode: UNKNOWN"),
		},
		{
			name: "empty staging target path",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:         testVolumeID,
				VolumeCapability: testVolumeCapability,
			},
			expectErr: status.Error(codes.InvalidArgument, "NodeStageVolume Staging Target Path must be provided"),
		},
		{
			name: "valid request with sharedMount false",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "false",
				},
			},
		},
		{
			name: "nil publish context - should return InvalidArgument error",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
				},
				PublishContext: nil,
			},
			expectErr: status.Error(codes.InvalidArgument, "publishContext must be provided"),
		},
		{
			name: "missing pod name in publish context - should return InvalidArgument error",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
				},
				PublishContext: map[string]string{},
			},
			expectErr: status.Error(codes.InvalidArgument, "publishContext must contain mounter pod name"),
		},
		{
			name: "empty pod name in publish context - should return InvalidArgument error",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName: "",
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "mounter pod name in publishContext cannot be empty"),
		},
		{
			name: "missing pod namespace in publish context - should return InvalidArgument error",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName: createMounterPodName("test-node", testVolumeID),
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "publishContext must contain mounter pod namespace"),
		},
		{
			name: "empty pod namespace in publish context - should return InvalidArgument error",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName:      createMounterPodName("test-node", testVolumeID),
					PublishContextKeyMounterPodNamespace: "",
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "mounter pod namespace in publishContext cannot be empty"),
		},
		{
			name: "valid request with sharedMount true",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
					util.VolumeContextKeyPVName:  testVolumeID,
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName:      createMounterPodName("test-node", testVolumeID),
					PublishContextKeyMounterPodNamespace: "test-ns",
				},
			},
		},
		{
			name: "valid request with sharedMount true and profilesEnabled true",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							MountFlags: []string{"implicit-dirs"},
						},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
					util.VolumeContextKeyPVName:  testVolumeID,
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName:      createMounterPodName("test-node", testVolumeID),
					PublishContextKeyMounterPodNamespace: "test-ns",
				},
			},
			profilesEnabled: true,
			expectedMountOptions: []string{
				"implicit-dirs",
				"file-cache:max-size-mb:1",
				"file-cache:cache-file-for-range-read:true",
				"log-severity=trace",
				"metadata-cache:stat-cache-max-size-mb:34",
				"file-cache-medium=ram",
			},
			pvConfig: &clientset.FakePVConfig{
				Name:         testVolumeID,
				VolumeHandle: testVolumeID,
				SCName:       "test-sc",
				Annotations: map[string]string{
					"gke-gcsfuse/bucket-scan-num-objects":      "1000",
					"gke-gcsfuse/bucket-scan-total-size-bytes": "1000000",
					"gke-gcsfuse/bucket-scan-location-type":    "regional",
				},
			},
			scConfig: &clientset.FakeSCConfig{
				Name: "test-sc",
				Labels: map[string]string{
					"gke-gcsfuse/profile": "true",
				},
				Parameters: map[string]string{
					"fileCacheForRangeRead": "true",
				},
				MountOptions: []string{"log-severity=trace"},
			},
		},
		{
			name: "valid request with sharedMount true and cloud profiler enabled",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount:               "true",
					util.VolumeContextKeyPVName:                testVolumeID,
					VolumeContextEnableCloudProfilerForSidecar: "true",
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName:      createMounterPodName("test-node", testVolumeID),
					PublishContextKeyMounterPodNamespace: "test-ns",
				},
			},
			expectedMountOptions: []string{
				util.EnableCloudProfilerForSidecarConst + "=true",
				util.PodNameConst + "=" + createMounterPodName("test-node", testVolumeID),
				util.PodUIDConst + "=" + createMounterPodName("test-node", testVolumeID),
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fakeServer.req = nil // reset before each test
			fakeClientset := clientset.NewFakeClientset()
			fakeClientset.CreateNode(clientset.FakeNodeConfig{
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
			})
			fakeClientset.CreatePod(clientset.FakePodConfig{
				Name:         podName,
				Namespace:    "test-ns",
				UID:          types.UID(podName),
				IsMounterPod: true,
				PodStatus:    &corev1.PodStatus{Phase: corev1.PodRunning},
				SidecarLimits: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
			})
			if test.pvConfig != nil {
				fakeClientset.CreatePV(*test.pvConfig)
			}
			if test.scConfig != nil {
				fakeClientset.CreateSC(*test.scConfig)
			}

			testEnv := initTestNodeServerWithCustomClientset(t, fakeClientset, false)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}
			ns.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath = func(uid string) string {
				return filepath.Join(kubeletDir, "pods", uid, "volumes", "kubernetes.io~empty-dir", util.SidecarContainerTmpVolumeName)
			}
			ns.driver.config.FeatureOptions.FeatureGCSFuseProfiles.Enabled = test.profilesEnabled

			_, err := testEnv.ns.NodeStageVolume(context.TODO(), test.req)
			if test.expectErr == nil && err != nil {
				t.Errorf("got error %q, expected error nil", err)
			}
			if test.expectErr != nil {
				if err == nil {
					t.Errorf("got error nil, expected error %q", test.expectErr)
				} else if !errors.Is(err, test.expectErr) && err.Error() != test.expectErr.Error() {
					t.Errorf("got error %q, expected error %q", err, test.expectErr)
				}
			}

			if test.expectErr == nil && test.expectedMountOptions != nil {
				if fakeServer.req == nil {
					t.Errorf("expected mounter server to receive request, got nil")
				} else {
					opts := cmpopts.SortSlices(func(a, b string) bool { return a < b })
					if diff := cmp.Diff(test.expectedMountOptions, fakeServer.req.MountOptions, opts); diff != "" {
						t.Errorf("Mount options mismatch (-want +got):\n%s", diff)
					}
				}
			}
		})
	}
}

func TestNodeUnstageVolume(t *testing.T) {
	t.Parallel()
	stagingPath, cleanup := setupTestStagingPath(t)
	defer cleanup()

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodeUnstageVolumeRequest
		expectedMount *mount.MountPoint
		expectErr     error
	}{
		{
			name:   "successful unmount - should return InvalidArgument error",
			mounts: []mount.MountPoint{{Device: testVolumeID, Path: stagingPath}},
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
			},
		},
		{
			name:      "empty request",
			req:       nil,
			expectErr: status.Error(codes.InvalidArgument, "NodeUnstageVolume Request cannot be nil"),
		},
		{
			name: "empty volume ID -  should return InvalidArgument error",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          "",
				StagingTargetPath: stagingPath,
			},
			expectErr: status.Error(codes.InvalidArgument, "NodeUnstageVolume Volume ID must be provided"),
		},
		{
			name: "missing staging path - should return InvalidArgument error",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: "",
			},
			expectErr: status.Error(codes.InvalidArgument, "NodeUnstageVolume Staging Target Path must be provided"),
		},
		{
			name: "dir doesn't exist - should succeed",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: "/node-unstage-dir-not-exists",
			},
		},
		{
			name: "dir not mounted - should succeed",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingPath,
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			testEnv := initTestNodeServer(t)
			if test.mounts != nil {
				testEnv.fm.MountPoints = test.mounts
			}

			_, err := testEnv.ns.NodeUnstageVolume(context.TODO(), test.req)
			if test.expectErr == nil && err != nil {
				t.Errorf("got error %q, expected error nil", err)
			}
			if test.expectErr != nil && !errors.Is(err, test.expectErr) {
				t.Errorf("got error %q, expected error %q", err, test.expectErr)
			}

			validateMountPoint(t, testEnv.fm, test.expectedMount, nil)
		})
	}
}

func TestMountToNode(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{
			name:    "successful mount to node - should succeed",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			podUID := "test-pod-uid"
			volumeID := "test-volume-id"
			stagingPath := filepath.Join(t.TempDir(), "var/lib/kubelet/pods", podUID, "volumes/kubernetes.io~csi", volumeID, "mount")

			testEnv := initTestNodeServer(t)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}

			emptyDirBasePath, err := util.PrepareEmptyDir(stagingPath, true)
			if err != nil {
				t.Fatalf("failed to prepare emptyDir path: %v", err)
			}
			ns.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath = func(uid string) string {
				return emptyDirBasePath
			}

			socketFile := filepath.Join(emptyDirBasePath, MounterPodSocketFile)

			startFakeMounterServer(t, socketFile)

			err = ns.mountToNode(ctx, podUID, stagingPath, volumeID, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("mountToNode() error = %v, wantErr %v", err, tc.wantErr)
			}

			// Verify that the symlink was removed (because it is deferred inside mountToNode)
			symlink := filepath.Join(ns.driver.config.FeatureOptions.SharedMountOptions.FuseSocketDir, mounterPodSocketDir, podUID)
			if _, err := os.Lstat(symlink); !os.IsNotExist(err) {
				t.Errorf("expected symlink %q to be removed, but it still exists or had another error", symlink)
			}
		})
	}
}

func validateMountOptions(t *testing.T, actualOpts []string, expectedOpts []string, unexpectedOpts []string) {
	t.Helper()
	optsMap := make(map[string]bool)
	for _, opt := range actualOpts {
		optsMap[opt] = true
	}
	for _, expectedOpt := range expectedOpts {
		if !optsMap[expectedOpt] {
			t.Errorf("expected option %q not found in actual options %v", expectedOpt, actualOpts)
		}
	}
	for _, unexpectedOpt := range unexpectedOpts {
		if optsMap[unexpectedOpt] {
			t.Errorf("unexpected option %q found in actual options %v", unexpectedOpt, actualOpts)
		}
	}
}

func validateMountPoint(t *testing.T, fm *mount.FakeMounter, e *mount.MountPoint, unexpectedOpts []string) {
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

	validateMountOptions(t, a.Opts, e.Opts, unexpectedOpts)
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

	testTargetPath, cleanup := setupTestTargetPath(t)
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

func TestNodePublishVolumeEnableGCSFuseKernelParams(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                       string
		enableKernelParamsFileFlag bool
		assumeGoodSidecarVersion   bool
		expectedOptions            []string
		unexpectedOptions          []string
	}{
		{
			name:                       "feature enabled, sidecar supported",
			enableKernelParamsFileFlag: true,
			assumeGoodSidecarVersion:   true,
			expectedOptions:            []string{"enable-gcsfuse-kernel-params=true"},
		},
		{
			name:                       "feature enabled, sidecar not supported",
			enableKernelParamsFileFlag: true,
			assumeGoodSidecarVersion:   false,
			unexpectedOptions:          []string{"enable-gcsfuse-kernel-params=true"},
		},
		{
			name:                       "feature disabled, sidecar supported",
			enableKernelParamsFileFlag: false,
			assumeGoodSidecarVersion:   true,
			unexpectedOptions:          []string{"enable-gcsfuse-kernel-params=true"},
		},
		{
			name:                       "feature disabled, sidecar not supported",
			enableKernelParamsFileFlag: false,
			assumeGoodSidecarVersion:   false,
			unexpectedOptions:          []string{"enable-gcsfuse-kernel-params=true"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testTargetPath, cleanup := setupTestTargetPath(t)
			defer cleanup()

			req := &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
			}
			fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})

			driver := initTestDriver(t, fakeMounter, clientset.NewFakeClientset())
			s, _ := driver.config.StorageServiceManager.SetupService(context.TODO(), nil, "")
			if _, err := s.CreateBucket(context.Background(), &storage.ServiceBucket{Name: testVolumeID}); err != nil {
				t.Fatalf("failed to create the fake bucket: %v", err)
			}
			driver.config.FeatureOptions.EnableGCSFuseKernelParams = tc.enableKernelParamsFileFlag
			driver.config.AssumeGoodSidecarVersion = tc.assumeGoodSidecarVersion
			ns := newNodeServer(driver, fakeMounter)

			_, err := ns.NodePublishVolume(context.TODO(), req)

			if err != nil {
				t.Fatalf("failed to publish volume: %v", err)
			}
			validateMountPoint(t, fakeMounter, &mount.MountPoint{
				Device: testVolumeID,
				Path:   testTargetPath,
				Type:   "fuse",
				Opts:   tc.expectedOptions,
			},
				tc.unexpectedOptions)
		})
	}
}

func TestNodePublishVolumeRespectEnableSidecarBucketAccessCheck(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                           string
		enableSidecarBucketAccessCheck bool
		userMountOptions               string
		assumeGoodSidecarVersion       bool
		expectedOptions                []string
		unexpectedOptions              []string
	}{
		{
			name:                           "sidecar check enabled globally, user does not specify option, sidecar check should be enabled",
			enableSidecarBucketAccessCheck: true,
			userMountOptions:               "",
			assumeGoodSidecarVersion:       true,
			expectedOptions:                []string{"enable-sidecar-bucket-access-check=true"},
		},
		{
			name:                           "sidecar check enabled globally, user specifies false, sidecar check should be disabled",
			enableSidecarBucketAccessCheck: true,
			userMountOptions:               "enable-sidecar-bucket-access-check=false",
			assumeGoodSidecarVersion:       true,
			expectedOptions:                []string{"enable-sidecar-bucket-access-check=false"},
			unexpectedOptions:              []string{"enable-sidecar-bucket-access-check=true"},
		},
		{
			name:                           "sidecar check enabled globally, user specifies true, sidecar check should be enabled",
			enableSidecarBucketAccessCheck: true,
			userMountOptions:               "enable-sidecar-bucket-access-check=true",
			assumeGoodSidecarVersion:       true,
			expectedOptions:                []string{"enable-sidecar-bucket-access-check=true"},
		},
		{
			name:                           "sidecar check disabled globally, user does not specify option, sidecar check should be disabled (absent)",
			enableSidecarBucketAccessCheck: false,
			userMountOptions:               "",
			assumeGoodSidecarVersion:       true,
			unexpectedOptions:              []string{"enable-sidecar-bucket-access-check=true", "enable-sidecar-bucket-access-check=false"},
		},
		{
			name:                           "sidecar check disabled globally, user specifies true, sidecar check should be enabled",
			enableSidecarBucketAccessCheck: false,
			userMountOptions:               "enable-sidecar-bucket-access-check=true",
			assumeGoodSidecarVersion:       true,
			expectedOptions:                []string{"enable-sidecar-bucket-access-check=true"},
		},
		{
			name:                           "sidecar check disabled globally, user specifies false, sidecar check should be disabled",
			enableSidecarBucketAccessCheck: false,
			userMountOptions:               "enable-sidecar-bucket-access-check=false",
			assumeGoodSidecarVersion:       true,
			expectedOptions:                []string{"enable-sidecar-bucket-access-check=false"},
			unexpectedOptions:              []string{"enable-sidecar-bucket-access-check=true"},
		},
		{
			name:                           "sidecar check enabled globally, unsupported sidecar version, user does not specify, sidecar check should be absent",
			enableSidecarBucketAccessCheck: true,
			userMountOptions:               "",
			assumeGoodSidecarVersion:       false,
			unexpectedOptions:              []string{"enable-sidecar-bucket-access-check=true", "enable-sidecar-bucket-access-check=false"},
		},
		{
			name:                           "sidecar check enabled globally, unsupported sidecar version, user specifies true, sidecar check should be enabled",
			enableSidecarBucketAccessCheck: true,
			userMountOptions:               "enable-sidecar-bucket-access-check=true",
			assumeGoodSidecarVersion:       false,
			expectedOptions:                []string{"enable-sidecar-bucket-access-check=true"},
		},
		{
			name:                           "sidecar check enabled globally, unsupported sidecar version, user specifies false, sidecar check should be disabled",
			enableSidecarBucketAccessCheck: true,
			userMountOptions:               "enable-sidecar-bucket-access-check=false",
			assumeGoodSidecarVersion:       false,
			expectedOptions:                []string{"enable-sidecar-bucket-access-check=false"},
			unexpectedOptions:              []string{"enable-sidecar-bucket-access-check=true"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testTargetPath, cleanup := setupTestTargetPath(t)
			defer cleanup()

			volumeContext := map[string]string{
				VolumeContextKeyPodName:            "test-pod",
				VolumeContextKeyPodNamespace:       "test-ns",
				VolumeContextKeyServiceAccountName: "test-sa",
			}
			if tc.userMountOptions != "" {
				volumeContext[VolumeContextKeyMountOptions] = tc.userMountOptions
			}

			req := &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    volumeContext,
			}
			fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})

			driver := initTestDriver(t, fakeMounter, clientset.NewFakeClientset())
			s, _ := driver.config.StorageServiceManager.SetupService(context.TODO(), nil, "")
			if _, err := s.CreateBucket(context.Background(), &storage.ServiceBucket{Name: testVolumeID}); err != nil {
				t.Fatalf("failed to create the fake bucket: %v", err)
			}
			driver.config.EnableSidecarBucketAccessCheck = tc.enableSidecarBucketAccessCheck
			driver.config.AssumeGoodSidecarVersion = tc.assumeGoodSidecarVersion
			ns := newNodeServer(driver, fakeMounter)

			_, err := ns.NodePublishVolume(context.TODO(), req)
			if err != nil {
				t.Fatalf("failed to publish volume: %v", err)
			}
			validateMountPoint(t, fakeMounter, &mount.MountPoint{
				Device: testVolumeID,
				Path:   testTargetPath,
				Type:   "fuse",
				Opts:   tc.expectedOptions,
			},
				tc.unexpectedOptions)
		})
	}
}

type volumeTestCase struct {
	name                         string
	totalEphemeralVolumeCount    int
	totalPersistentVolumeCount   int
	gcsFuseEphemeralVolumeCount  int
	gcsFusePersistentVolumeCount int
	expectCollectorRegistered    bool
}

const csiDriverName string = "gcs-fuse-csi.storage.gke.io"
const otherDriverName string = "other.csi.driver"

func createVolumesTestCase(fakeClientSet *clientset.FakeClientset, tc volumeTestCase) []corev1.Volume {
	volumes := []corev1.Volume{}

	// Add GCS Fuse ephemeral volumes.
	for i := 0; i < tc.gcsFuseEphemeralVolumeCount; i++ {
		volumes = append(volumes, corev1.Volume{
			Name: "gcs-fuse-csi-ephemeral-volume-" + strconv.Itoa(i),
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver: csiDriverName,
				},
			},
		})
	}

	// Add non GCS Fuse ephemeral volumes.
	for i := 0; i < tc.totalEphemeralVolumeCount-tc.gcsFuseEphemeralVolumeCount; i++ {
		volumes = append(volumes, corev1.Volume{
			Name: "other-csi-ephemeral-volume-" + strconv.Itoa(i),
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver: otherDriverName,
				},
			},
		})
	}

	// Add GCS Fuse persistent volumes.
	for i := 0; i < tc.gcsFusePersistentVolumeCount; i++ {
		pvcName := "gcs-fuse-csi-pvc-" + strconv.Itoa(i)
		pvName := "gcs-fuse-csi-pv-" + strconv.Itoa(i)
		volumes = append(volumes, corev1.Volume{
			Name: pvcName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		})
		fakeClientSet.CreatePV(clientset.FakePVConfig{
			Name:       pvName,
			DriverName: csiDriverName,
		})
		fakeClientSet.CreatePVC(clientset.FakePVCConfig{
			Name:       pvcName,
			VolumeName: pvName,
		})
	}

	// Add non GCS Fuse persistent volumes.
	for i := 0; i < tc.totalPersistentVolumeCount-tc.gcsFusePersistentVolumeCount; i++ {
		pvcName := "other-csi-pvc-" + strconv.Itoa(i)
		pvName := "other-csi-pv-" + strconv.Itoa(i)
		volumes = append(volumes, corev1.Volume{
			Name: pvcName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		})
		fakeClientSet.CreatePV(clientset.FakePVConfig{
			Name:       pvName,
			DriverName: otherDriverName,
			SCName:     "test-sc",
		})
		fakeClientSet.CreatePVC(clientset.FakePVCConfig{
			Name:       pvcName,
			VolumeName: pvName,
		})
	}

	return volumes
}

func TestCountGcsFuseVolumes(t *testing.T) {
	testCases := []struct {
		name          string
		tcVolumes     volumeTestCase
		expectedCount int
	}{
		{
			name:          "no volumes",
			tcVolumes:     volumeTestCase{},
			expectedCount: 0,
		},
		{
			name: "one gcsfuse ephemeral volume",
			tcVolumes: volumeTestCase{
				totalEphemeralVolumeCount:   1,
				gcsFuseEphemeralVolumeCount: 1,
			},
			expectedCount: 1,
		},
		{
			name: "one gcsfuse persistent volume",
			tcVolumes: volumeTestCase{
				totalPersistentVolumeCount:   1,
				gcsFusePersistentVolumeCount: 1,
			},
			expectedCount: 1,
		},
		{
			name: "multiple gcsfuse ephemeral volumes",
			tcVolumes: volumeTestCase{
				totalEphemeralVolumeCount:   2,
				gcsFuseEphemeralVolumeCount: 2,
			},
			expectedCount: 2,
		},
		{
			name: "multiple gcsfuse persistent volumes",
			tcVolumes: volumeTestCase{
				totalPersistentVolumeCount:   2,
				gcsFusePersistentVolumeCount: 2,
			},
			expectedCount: 2,
		},
		{
			name: "one ephemeral and one persistent gcsfuse volumes",
			tcVolumes: volumeTestCase{
				totalEphemeralVolumeCount:    1,
				totalPersistentVolumeCount:   1,
				gcsFuseEphemeralVolumeCount:  1,
				gcsFusePersistentVolumeCount: 1,
			},
			expectedCount: 2,
		},
		{
			name: "multiple ephemeral and multiple persistent gcsfuse volumes",
			tcVolumes: volumeTestCase{
				totalEphemeralVolumeCount:    2,
				totalPersistentVolumeCount:   3,
				gcsFuseEphemeralVolumeCount:  2,
				gcsFusePersistentVolumeCount: 3,
			},
			expectedCount: 5,
		},
		{
			name: "mixed csi drivers with some gcsfuse volumes",
			tcVolumes: volumeTestCase{
				totalEphemeralVolumeCount:    5,
				totalPersistentVolumeCount:   5,
				gcsFuseEphemeralVolumeCount:  2,
				gcsFusePersistentVolumeCount: 3,
			},
			expectedCount: 7,
		},
		{
			name: "mixed csi drivers with no gcsfuse volumes",
			tcVolumes: volumeTestCase{
				totalEphemeralVolumeCount:  5,
				totalPersistentVolumeCount: 5,
			},
			expectedCount: 5,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClientSet := clientset.NewFakeClientset()
			testEnv := initTestNodeServerWithCustomClientset(t, fakeClientSet, false)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}
			ns.driver.config.Name = csiDriverName
			volumes := createVolumesTestCase(fakeClientSet, tc.tcVolumes)
			pod := &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: volumes,
				},
			}
			count, _ := ns.countGcsFuseVolumes(pod)
			if count != tc.expectedCount {
				t.Errorf("got %d, want %d", count, tc.expectedCount)
			}
		})
	}
}

func TestNodePublishVolumeAssertMetricsCollectorRegistration(t *testing.T) {
	t.Parallel()
	testTargetPath, cleanup := setupTestTargetPath(t)
	defer cleanup()

	baseReq := &csi.NodePublishVolumeRequest{
		VolumeId:         testVolumeID,
		TargetPath:       testTargetPath,
		VolumeCapability: testVolumeCapability,
		VolumeContext: map[string]string{
			VolumeContextKeyPodName:      "test-pod",
			VolumeContextKeyPodNamespace: "test-ns",
			"bucketName":                 testVolumeID,
		},
	}

	testCases := []volumeTestCase{
		{
			name:                        "should register collector for 1 gcsfuse ephemeral volume",
			totalEphemeralVolumeCount:   1,
			gcsFuseEphemeralVolumeCount: 1,
			expectCollectorRegistered:   true,
		},
		{
			name:                         "should register collector for 1 gcsfuse persistent volume",
			totalPersistentVolumeCount:   1,
			gcsFusePersistentVolumeCount: 1,
			expectCollectorRegistered:    true,
		},
		{
			name:                         "should register collector for a mix of gcsfuse volumes",
			totalEphemeralVolumeCount:    2,
			totalPersistentVolumeCount:   2,
			gcsFuseEphemeralVolumeCount:  1,
			gcsFusePersistentVolumeCount: 1,
			expectCollectorRegistered:    true,
		},
		{
			name:                        "should register collector for 10 gcsfuse ephemeral volumes",
			totalEphemeralVolumeCount:   10,
			gcsFuseEphemeralVolumeCount: 10,
			expectCollectorRegistered:   true,
		},
		{
			name:                         "should register collector for 10 gcsfuse persistent volumes",
			totalPersistentVolumeCount:   10,
			gcsFusePersistentVolumeCount: 10,
			expectCollectorRegistered:    true,
		},
		{
			name:                         "should register collector for a mix of 10 gcsfuse volumes",
			totalEphemeralVolumeCount:    5,
			totalPersistentVolumeCount:   5,
			gcsFuseEphemeralVolumeCount:  5,
			gcsFusePersistentVolumeCount: 5,
			expectCollectorRegistered:    true,
		},
		{
			name:                        "should not register collector for 11 gcsfuse ephemeral volumes",
			totalEphemeralVolumeCount:   11,
			gcsFuseEphemeralVolumeCount: 11,
			expectCollectorRegistered:   false,
		},
		{
			name:                         "should not register collector for 11 gcsfuse persistent volumes",
			totalPersistentVolumeCount:   11,
			gcsFusePersistentVolumeCount: 11,
			expectCollectorRegistered:    false,
		},
		{
			name:                         "should not register collector for a mix of 11 gcsfuse volumes",
			totalEphemeralVolumeCount:    6,
			totalPersistentVolumeCount:   5,
			gcsFuseEphemeralVolumeCount:  6,
			gcsFusePersistentVolumeCount: 5,
			expectCollectorRegistered:    false,
		},
		{
			name:                         "should register collector with other non gcsfuse volumes",
			totalEphemeralVolumeCount:    5,
			totalPersistentVolumeCount:   5,
			gcsFuseEphemeralVolumeCount:  2,
			gcsFusePersistentVolumeCount: 2,
			expectCollectorRegistered:    true,
		},
		{
			name:                         "should not register collector with other non gcsfuse volumes when gcsfuse volumes exceeds limit",
			totalEphemeralVolumeCount:    15,
			totalPersistentVolumeCount:   15,
			gcsFuseEphemeralVolumeCount:  6,
			gcsFusePersistentVolumeCount: 5,
			expectCollectorRegistered:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup mock clientset
			fakeClientSet := clientset.NewFakeClientset()
			volumes := createVolumesTestCase(fakeClientSet, tc)

			fakeClientSet.AddPodVolumes(volumes)
			// Setup node server
			testEnv := initTestNodeServerWithCustomClientset(t, fakeClientSet, false)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}
			ns.driver.config.Name = csiDriverName

			// Setup metrics manager
			mm := metrics.NewFakeMetricsManager()
			ns.driver.config.MetricsManager = mm

			// Update request
			req := proto.Clone(baseReq).(*csi.NodePublishVolumeRequest)

			// Call NodePublishVolume
			_, err := ns.NodePublishVolume(context.Background(), req)
			if err != nil {
				// The fake clientset does not have the pod annotations,
				// which will cause the sidecar check to fail.
				if !strings.Contains(err.Error(), "failed to find the sidecar container in Pod spec") {
					t.Fatalf("NodePublishVolume() failed: %v", err)
				}
			}

			// Assertions
			collectors := mm.GetCollectors()
			_, collectorRegistered := collectors[testTargetPath]
			if tc.expectCollectorRegistered && !collectorRegistered {
				t.Error("expected metrics collector to be registered, but it was not")
			}
			if !tc.expectCollectorRegistered && collectorRegistered {
				t.Error("expected metrics collector not to be registered, but it was")
			}
		})
	}
}

func TestNodePublishVolumeStorageEndpointInternal(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                     string
		universeDomain           string
		assumeGoodSidecarVersion bool
		expectedOptions          []string
		unexpectedOptions        []string
	}{
		{
			name:                     "sidecar version supported, universe domain set, injects internal storage endpoint override",
			universeDomain:           "my-custom-universe",
			assumeGoodSidecarVersion: true,
			expectedOptions:          []string{"storage-endpoint-internal=storage.my-custom-universe:443"},
		},
		{
			name:                     "sidecar version supported, empty universe domain, injects default internal storage endpoint override",
			universeDomain:           "",
			assumeGoodSidecarVersion: true,
			expectedOptions:          []string{"storage-endpoint-internal=storage.googleapis.com:443"},
		},
		{
			name:                     "sidecar version not supported, universe domain set, should not inject flag",
			universeDomain:           "my-custom-universe.com",
			assumeGoodSidecarVersion: false,
			unexpectedOptions:        []string{"storage-endpoint-internal="},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testTargetPath, cleanup := setupTestTargetPath(t)
			defer cleanup()

			req := &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextKeyPodName:      "test-pod",
					VolumeContextKeyPodNamespace: "test-ns",
				},
			}
			fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})

			driver := initTestDriver(t, fakeMounter, clientset.NewFakeClientset())
			s, _ := driver.config.StorageServiceManager.SetupService(context.TODO(), nil, "")
			if _, err := s.CreateBucket(context.Background(), &storage.ServiceBucket{Name: testVolumeID}); err != nil {
				t.Fatalf("failed to create the fake bucket: %v", err)
			}

			driver.config.UniverseDomain = tc.universeDomain
			driver.config.AssumeGoodSidecarVersion = tc.assumeGoodSidecarVersion
			ns := newNodeServer(driver, fakeMounter)

			_, err := ns.NodePublishVolume(context.Background(), req)
			if err != nil {
				t.Fatalf("failed to publish volume: %v", err)
			}

			validateMountPoint(t, fakeMounter, &mount.MountPoint{
				Device: testVolumeID,
				Path:   testTargetPath,
				Type:   "fuse",
				Opts:   tc.expectedOptions,
			}, tc.unexpectedOptions)

			// Double check if unexpected options were completely avoided from the list prefix
			if !tc.assumeGoodSidecarVersion && len(fakeMounter.MountPoints) == 1 {
				for _, opt := range fakeMounter.MountPoints[0].Opts {
					if strings.HasPrefix(opt, "storage-endpoint-internal=") {
						t.Errorf("found unexpected storage-endpoint-internal flag option: %s", opt)
					}
				}
			}
		})
	}
}

func TestNodePublishVolumeForSharedMount(t *testing.T) {
	t.Parallel()

	nodeID := "test-node"
	podNamespace := "test-ns"
	podName := createMounterPodName(nodeID, testVolumeID)
	podUID := types.UID(podName)

	cases := []struct {
		name              string
		reqBuilder        func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest
		setupClient       func() *clientset.FakeClientset
		errorFileContent  string
		targetPathMounted bool
		expectErrCode     codes.Code
		expectedBindOpts  []string
	}{
		{
			name: "invalid volume capability - should return InvalidArgument error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
					VolumeId: testVolumeID,
					VolumeCapability: &csi.VolumeCapability{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_UNKNOWN, // Invalid
						},
					},
				}
			},
			setupClient:   func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "missing volume capability - should return InvalidArgument error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
					VolumeId: testVolumeID,
					// VolumeCapability: testVolumeCapability, // Missing
				}
			},
			setupClient:   func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "missing volume id - should return InvalidArgument error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
					// VolumeId: "", missing volume ID.
					VolumeCapability: testVolumeCapability,
				}
			},
			setupClient:   func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "valid request - should succeed",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
				}
			},
			setupClient: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePod(clientset.FakePodConfig{
					Name:      podName,
					Namespace: podNamespace,
					UID:       podUID,
					PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
				})
				return fc
			},
			expectErrCode: codes.OK,
		},
		{
			name: "missing staging path - should return InvalidArgument error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:         testVolumeID,
					VolumeCapability: testVolumeCapability,
					TargetPath:       targetPath,
					// StagingTargetPath: "", // Missing
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
				}
			},
			setupClient:   func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "missing target path - should return InvalidArgument error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:         testVolumeID,
					VolumeCapability: testVolumeCapability,
					// TargetPath:        "", // Missing
					StagingTargetPath: stagingPath,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
				}
			},
			setupClient:   func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "missing publish context - should return InvalidArgument error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					// PublishContext:    nil, // Missing
				}
			},
			setupClient:   func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "missing mounter pod namespace - should return InvalidArgument error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName: podName,
					},
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
				}
			},
			setupClient:   func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "missing mounter pod name - should return InvalidArgument error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					PublishContext: map[string]string{
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
				}
			},
			setupClient:   func() *clientset.FakeClientset { return clientset.NewFakeClientset() },
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "mounter pod not found - should return FailedPrecondition error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
				}
			},
			setupClient: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.GetPodErr = apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, podName)
				return fc
			},
			expectErrCode: codes.FailedPrecondition,
		},
		{
			name: "mounter pod not running - should return Internal error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
				}
			},
			setupClient: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePod(clientset.FakePodConfig{
					Name:      podName,
					Namespace: podNamespace,
					UID:       podUID,
					PodStatus: &corev1.PodStatus{Phase: corev1.PodFailed}, // Not running
				})
				return fc
			},
			expectErrCode: codes.Internal,
		},
		{
			name: "error getting mounter pod - should return Internal error",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
				}
			},
			setupClient: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.GetPodErr = errors.New("simulated api server error")
				return fc
			},
			expectErrCode: codes.Internal,
		},
		{
			name: "mounter pod error file exists - should return error from file",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
				}
			},
			setupClient: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePod(clientset.FakePodConfig{
					Name:      podName,
					Namespace: podNamespace,
					UID:       podUID,
					PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
				})
				return fc
			},
			errorFileContent: "gcsfuse failed with error: bucket doesn't exist",
			expectErrCode:    codes.NotFound,
		},
		{
			name: "target path already mounted - should return early success",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
				}
			},
			setupClient: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePod(clientset.FakePodConfig{
					Name:      podName,
					Namespace: podNamespace,
					UID:       podUID,
					PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
				})
				return fc
			},
			targetPathMounted: true,
			expectErrCode:     codes.OK,
		},
		{
			name: "target path not mounted - should return successful read-write bind mount",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					Readonly:          false,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
				}
			},
			setupClient: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePod(clientset.FakePodConfig{
					Name:      podName,
					Namespace: podNamespace,
					UID:       podUID,
					PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
				})
				return fc
			},
			targetPathMounted: false,
			expectErrCode:     codes.OK,
			expectedBindOpts:  []string{"bind"},
		},
		{
			name: "target path not mounted - should return successful read-only bind mount",
			reqBuilder: func(t *testing.T, targetPath, stagingPath string) *csi.NodePublishVolumeRequest {
				return &csi.NodePublishVolumeRequest{
					VolumeId:          testVolumeID,
					VolumeCapability:  testVolumeCapability,
					TargetPath:        targetPath,
					StagingTargetPath: stagingPath,
					Readonly:          true,
					VolumeContext: map[string]string{
						VolumeContextSharedNodeMount: "true",
					},
					PublishContext: map[string]string{
						PublishContextKeyMounterPodName:      podName,
						PublishContextKeyMounterPodNamespace: podNamespace,
					},
				}
			},
			setupClient: func() *clientset.FakeClientset {
				fc := clientset.NewFakeClientset()
				fc.CreatePod(clientset.FakePodConfig{
					Name:      podName,
					Namespace: podNamespace,
					UID:       podUID,
					PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
				})
				return fc
			},
			targetPathMounted: false,
			expectErrCode:     codes.OK,
			expectedBindOpts:  []string{"bind", "ro"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testTargetPath, cleanupTarget := setupTestTargetPath(t)
			defer cleanupTarget()

			testStagingPath, cleanupStaging := setupTestStagingPath(t)
			defer cleanupStaging()

			fakeClientSet := tc.setupClient()
			testEnv := initTestNodeServerWithCustomClientset(t, fakeClientSet, false)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}

			// Setup EmptyDirBasePath for this test case
			tempDir := t.TempDir()
			ns.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath = func(uid string) string {
				return filepath.Join(tempDir, uid)
			}

			// Create error file if specified
			if tc.errorFileContent != "" {
				emptyDirPath := ns.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath(string(podUID))
				createErrorFile(t, emptyDirPath, tc.errorFileContent)
			}

			// Configure existing mount states. Staging must always be reported as mounted for verification checks.
			initialMounts := []mount.MountPoint{
				{Path: testStagingPath, Device: "fuse"},
			}
			if tc.targetPathMounted {
				initialMounts = append(initialMounts, mount.MountPoint{Path: testTargetPath, Device: "fuse"})
			}
			testEnv.fm.MountPoints = initialMounts

			req := tc.reqBuilder(t, testTargetPath, testStagingPath)

			_, err := ns.NodePublishVolume(context.TODO(), req)

			if tc.expectErrCode != codes.OK {
				if err == nil {
					t.Errorf("Got error nil, expected error with code %v", tc.expectErrCode)
				} else {
					st, ok := status.FromError(err)
					if !ok {
						t.Errorf("Got non-status error %v, expected status error with code %v", err, tc.expectErrCode)
					} else if st.Code() != tc.expectErrCode {
						t.Errorf("Got error code %v, expected %v, err: %v", st.Code(), tc.expectErrCode, err)
					}
				}
			} else {
				if err != nil {
					t.Errorf("Got error %q, expected error nil", err)
				}

				// Verify actual flags captured on the fake mounter match expectations
				if len(tc.expectedBindOpts) > 0 {
					var targetBindFound bool
					for _, mp := range testEnv.fm.MountPoints {
						if mp.Path == testTargetPath {
							targetBindFound = true
							for _, expectedOpt := range tc.expectedBindOpts {
								hasOpt := false
								for _, o := range mp.Opts {
									if o == expectedOpt {
										hasOpt = true
										break
									}
								}
								if !hasOpt {
									t.Errorf("Expected flag %q missing from registered mount context: %v", expectedOpt, mp.Opts)
								}
							}
						}
					}
					if !targetBindFound {
						t.Error("Expected target path to be registered to the mount points map, but none was found")
					}
				}
			}
		})
	}
}

func TestNodeStageVolumeEnableAutoGoMemLimit(t *testing.T) {
	nodeID := "test-node"
	volID := testVolumeID
	podNamespace := "test-ns"
	podName := createMounterPodName(nodeID, volID)
	podUID := types.UID(podName)

	cases := []struct {
		name                     string
		enableAutoGoMemLimit     bool
		autoGoMemLimitRatio      float64
		assumeGoodSidecarVersion bool
		userMountOptions         string
		expectedOptions          []string
		unexpectedOptions        []string
	}{
		{
			name:                     "feature flag enabled, mounter pod image supported, no user overrides, expect all driver defaults",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			expectedOptions:          []string{"enable-auto-gomemlimit=true", "auto-gomemlimit-ratio=0.95"},
		},
		{
			name:                     "feature flag disabled, mounter pod image supported, no user overrides, expect no driver defaults",
			enableAutoGoMemLimit:     false,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			unexpectedOptions:        []string{"enable-auto-gomemlimit=true", "auto-gomemlimit-ratio=0.95"},
		},
		{
			name:                     "feature flag enabled, mounter pod image not supported, no user overrides, expect no driver defaults",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: false,
			unexpectedOptions:        []string{"enable-auto-gomemlimit=true", "auto-gomemlimit-ratio=0.95"},
		},
		{
			name:                     "feature flag enabled, mounter pod image supported, user overrides ratio, expect driver default enable flag and user ratio",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			userMountOptions:         "auto-gomemlimit-ratio=0.8",
			expectedOptions:          []string{"auto-gomemlimit-ratio=0.8", "enable-auto-gomemlimit=true"},
		},
		{
			name:                     "feature flag enabled, mounter pod image supported, user overrides enable flag to false, expect user enable flag and driver default ratio",
			enableAutoGoMemLimit:     true,
			autoGoMemLimitRatio:      0.95,
			assumeGoodSidecarVersion: true,
			userMountOptions:         "enable-auto-gomemlimit=false",
			expectedOptions:          []string{"enable-auto-gomemlimit=false", "auto-gomemlimit-ratio=0.95"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testStagingPath, cleanupStaging := setupTestStagingPath(t)
			defer cleanupStaging()

			tmpDir, err := os.MkdirTemp("/tmp", "s")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			t.Cleanup(func() { os.RemoveAll(tmpDir) })
			socketDir := filepath.Join(tmpDir, "s")
			emptyDirBasePath := filepath.Join(tmpDir, "e")

			sockFile := filepath.Join(emptyDirBasePath, string(podUID), MounterPodSocketFile)
			mounterServer := startFakeMounterServer(t, sockFile)

			fc := clientset.NewFakeClientset()
			fc.CreatePod(clientset.FakePodConfig{
				Name:         podName,
				Namespace:    podNamespace,
				UID:          podUID,
				PodStatus:    &corev1.PodStatus{Phase: corev1.PodRunning},
				IsMounterPod: true,
			})

			fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})

			testEnv := initTestNodeServerWithCustomClientset(t, fc, false)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}
			ns.mounter = fakeMounter

			ns.driver.config.FeatureOptions.GoMemLimitOptions = &GoMemLimitOptions{
				EnableAutoGoMemLimit: tc.enableAutoGoMemLimit,
				AutoGoMemLimitRatio:  tc.autoGoMemLimitRatio,
			}
			ns.driver.config.AssumeGoodSidecarVersion = tc.assumeGoodSidecarVersion
			ns.driver.config.FeatureOptions.SharedMountOptions = &SharedMountOptions{
				Enabled:       true,
				FuseSocketDir: socketDir,
				EmptyDirBasePath: func(podUID string) string {
					return filepath.Join(emptyDirBasePath, podUID)
				},
			}

			vc := map[string]string{
				VolumeContextSharedNodeMount: "true",
				util.VolumeContextKeyPVName:  volID,
			}
			if tc.userMountOptions != "" {
				vc[VolumeContextKeyMountOptions] = tc.userMountOptions
			}

			stageReq := &csi.NodeStageVolumeRequest{
				VolumeId:          volID,
				StagingTargetPath: testStagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     vc,
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName:      podName,
					PublishContextKeyMounterPodNamespace: podNamespace,
				},
			}

			_, err = ns.NodeStageVolume(context.Background(), stageReq)
			if err != nil {
				t.Fatalf("NodeStageVolume failed: %v", err)
			}
			defer func() {
				if vs, ok := ns.volumeStateStore.Load(testStagingPath); ok && vs != nil {
					if vs.GCSFuseKernelMonitorState.CancelFunc != nil {
						vs.GCSFuseKernelMonitorState.CancelFunc()
					}
				}
			}()

			if mounterServer.req == nil {
				t.Fatalf("expected mounterServer.req to be non-nil, but got nil")
			}
			validateMountOptions(t, mounterServer.req.MountOptions, tc.expectedOptions, tc.unexpectedOptions)
		})
	}
}

func TestNodeStageVolumeEnableGCSFuseKernelParams(t *testing.T) {
	nodeID := "test-node"
	volID := testVolumeID
	podNamespace := "test-ns"
	podName := createMounterPodName(nodeID, volID)
	podUID := types.UID(podName)

	cases := []struct {
		name                       string
		enableKernelParamsFileFlag bool
		assumeGoodSidecarVersion   bool
		expectedOptions            []string
		unexpectedOptions          []string
	}{
		{
			name:                       "feature enabled, mounter pod image supported",
			enableKernelParamsFileFlag: true,
			assumeGoodSidecarVersion:   true,
			expectedOptions:            []string{"enable-gcsfuse-kernel-params=true"},
		},
		{
			name:                       "feature enabled, mounter pod image not supported",
			enableKernelParamsFileFlag: true,
			assumeGoodSidecarVersion:   false,
			unexpectedOptions:          []string{"enable-gcsfuse-kernel-params=true"},
		},
		{
			name:                       "feature disabled, mounter pod image supported",
			enableKernelParamsFileFlag: false,
			assumeGoodSidecarVersion:   true,
			unexpectedOptions:          []string{"enable-gcsfuse-kernel-params=true"},
		},
		{
			name:                       "feature disabled, mounter pod image not supported",
			enableKernelParamsFileFlag: false,
			assumeGoodSidecarVersion:   false,
			unexpectedOptions:          []string{"enable-gcsfuse-kernel-params=true"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testStagingPath, cleanupStaging := setupTestStagingPath(t)
			defer cleanupStaging()

			// Use a short base dir to avoid hitting the 108-character limit for Unix domain sockets.
			tmpDir, err := os.MkdirTemp("/tmp", "s")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			t.Cleanup(func() { os.RemoveAll(tmpDir) })
			socketDir := filepath.Join(tmpDir, "s")
			emptyDirBasePath := filepath.Join(tmpDir, "e")

			sockFile := filepath.Join(emptyDirBasePath, string(podUID), MounterPodSocketFile)
			mounterServer := startFakeMounterServer(t, sockFile)

			fc := clientset.NewFakeClientset()
			fc.CreatePod(clientset.FakePodConfig{
				Name:         podName,
				Namespace:    podNamespace,
				UID:          podUID,
				PodStatus:    &corev1.PodStatus{Phase: corev1.PodRunning},
				IsMounterPod: true,
			})

			fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})

			testEnv := initTestNodeServerWithCustomClientset(t, fc, false)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}
			ns.mounter = fakeMounter

			ns.driver.config.FeatureOptions.EnableGCSFuseKernelParams = tc.enableKernelParamsFileFlag
			ns.driver.config.AssumeGoodSidecarVersion = tc.assumeGoodSidecarVersion
			ns.driver.config.FeatureOptions.SharedMountOptions = &SharedMountOptions{
				Enabled:       true,
				FuseSocketDir: socketDir,
				EmptyDirBasePath: func(podUID string) string {
					return filepath.Join(emptyDirBasePath, podUID)
				},
			}

			stageReq := &csi.NodeStageVolumeRequest{
				VolumeId:          volID,
				StagingTargetPath: testStagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
					util.VolumeContextKeyPVName:  volID,
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName:      podName,
					PublishContextKeyMounterPodNamespace: podNamespace,
				},
			}

			_, err = ns.NodeStageVolume(context.Background(), stageReq)
			if err != nil {
				t.Fatalf("NodeStageVolume failed: %v", err)
			}
			defer func() {
				if vs, ok := ns.volumeStateStore.Load(testStagingPath); ok && vs != nil {
					if vs.GCSFuseKernelMonitorState.CancelFunc != nil {
						vs.GCSFuseKernelMonitorState.CancelFunc()
					}
				}
			}()

			if mounterServer.req == nil {
				t.Fatalf("expected mounterServer.req to be non-nil, but got nil")
			}
			validateMountOptions(t, mounterServer.req.MountOptions, tc.expectedOptions, tc.unexpectedOptions)
		})
	}
}

func TestNodeStageVolumeHostNetwork(t *testing.T) {
	cases := []struct {
		name             string
		userSpecifiedIDP string
		expectedOptions  []string
	}{
		{
			name:             "mounter pod on host network with ksa opt-in and user-specified identity provider",
			userSpecifiedIDP: "https://container.googleapis.com/v1/projects/test/locations/test/clusters/test",
			expectedOptions: []string{
				"hnw-ksa=true",
				"token-server-identity-provider=https://container.googleapis.com/v1/projects/test/locations/test/clusters/test",
			},
		},
		{
			name:             "mounter pod on host network with ksa opt-in and default token manager identity provider",
			userSpecifiedIDP: "",
			expectedOptions: []string{
				"hnw-ksa=true",
				"token-server-identity-provider=fake.identity.provider",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodeID := "test-node"
			volID := testVolumeID
			podNamespace := "test-ns"
			podName := createMounterPodName(nodeID, volID)
			podUID := types.UID(podName)

			testStagingPath, cleanupStaging := setupTestStagingPath(t)
			defer cleanupStaging()

			tmpDir, err := os.MkdirTemp("/tmp", "s_hnw")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			t.Cleanup(func() { os.RemoveAll(tmpDir) })
			socketDir := filepath.Join(tmpDir, "s")
			emptyDirBasePath := filepath.Join(tmpDir, "e")

			sockFile := filepath.Join(emptyDirBasePath, string(podUID), MounterPodSocketFile)
			mounterServer := startFakeMounterServer(t, sockFile)

			fc := clientset.NewFakeClientset()
			fc.CreatePod(clientset.FakePodConfig{
				Name:               podName,
				Namespace:          podNamespace,
				UID:                podUID,
				PodStatus:          &corev1.PodStatus{Phase: corev1.PodRunning},
				IsMounterPod:       true,
				HostNetworkEnabled: true,
			})
			fc.AddPodVolumes([]corev1.Volume{
				{Name: webhook.SidecarContainerSATokenVolumeName},
			})

			fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})

			testEnv := initTestNodeServerWithCustomClientset(t, fc, false)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}
			ns.mounter = fakeMounter

			ns.driver.config.AssumeGoodSidecarVersion = true
			ns.driver.config.FeatureOptions.SharedMountOptions = &SharedMountOptions{
				Enabled:       true,
				FuseSocketDir: socketDir,
				EmptyDirBasePath: func(podUID string) string {
					return filepath.Join(emptyDirBasePath, podUID)
				},
			}

			volContext := map[string]string{
				VolumeContextSharedNodeMount:      "true",
				VolumeContextKeyHostNetworkPodKSA: "true",
				util.VolumeContextKeyPVName:       volID,
			}
			if tc.userSpecifiedIDP != "" {
				volContext[VolumeContextKeyIdentityProvider] = tc.userSpecifiedIDP
			}

			stageReq := &csi.NodeStageVolumeRequest{
				VolumeId:          volID,
				StagingTargetPath: testStagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     volContext,
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName:      podName,
					PublishContextKeyMounterPodNamespace: podNamespace,
				},
			}

			_, err = ns.NodeStageVolume(context.Background(), stageReq)
			if err != nil {
				t.Fatalf("NodeStageVolume failed: %v", err)
			}
			defer func() {
				if vs, ok := ns.volumeStateStore.Load(testStagingPath); ok && vs != nil {
					if vs.GCSFuseKernelMonitorState.CancelFunc != nil {
						vs.GCSFuseKernelMonitorState.CancelFunc()
					}
				}
			}()

			if mounterServer.req == nil {
				t.Fatalf("expected mounterServer.req to be non-nil, but got nil")
			}
			validateMountOptions(t, mounterServer.req.MountOptions, tc.expectedOptions, nil)
		})
	}
}

func TestNodeStageVolumeMachineTypeDefaulting(t *testing.T) {
	t.Parallel()

	nodeID := "test-node"
	volID := testVolumeID
	podNamespace := "test-ns"
	podName := createMounterPodName(nodeID, volID)
	podUID := types.UID(podName)
	expectedMachineType := "e2-medium" // default set by FakeClientset.CreateNode

	cases := []struct {
		name                     string
		assumeGoodSidecarVersion bool
		disableAutoconfig        bool
		expectFlagFileWritten    bool
	}{
		{
			name:                     "version check passes - flag file should be written with machine-type and disable-autoconfig",
			assumeGoodSidecarVersion: true,
			disableAutoconfig:        false,
			expectFlagFileWritten:    true,
		},
		{
			name:                     "version check passes, autoconfig disabled - flag file written with disable-autoconfig=true",
			assumeGoodSidecarVersion: true,
			disableAutoconfig:        true,
			expectFlagFileWritten:    true,
		},
		{
			name:                     "version check fails - flag file should not be written",
			assumeGoodSidecarVersion: false,
			disableAutoconfig:        false,
			expectFlagFileWritten:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			testStagingPath, cleanupStaging := setupTestStagingPath(t)
			defer cleanupStaging()

			tmpDir, err := os.MkdirTemp("/tmp", "s")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			t.Cleanup(func() { os.RemoveAll(tmpDir) })
			socketDir := filepath.Join(tmpDir, "s")
			emptyDirBaseRoot := filepath.Join(tmpDir, "e")

			sockFile := filepath.Join(emptyDirBaseRoot, string(podUID), MounterPodSocketFile)
			_ = startFakeMounterServer(t, sockFile)

			fc := clientset.NewFakeClientset()
			fc.CreatePod(clientset.FakePodConfig{
				Name:         podName,
				Namespace:    podNamespace,
				UID:          podUID,
				PodStatus:    &corev1.PodStatus{Phase: corev1.PodRunning},
				IsMounterPod: true,
			})

			testEnv := initTestNodeServerWithCustomClientset(t, fc, false)
			ns, ok := testEnv.ns.(*nodeServer)
			if !ok {
				t.Fatalf("Failed to cast NodeServer to *nodeServer")
			}
			ns.mounter = mount.NewFakeMounter([]mount.MountPoint{})
			ns.driver.config.AssumeGoodSidecarVersion = tc.assumeGoodSidecarVersion
			ns.driver.config.DisableAutoconfig = tc.disableAutoconfig
			ns.driver.config.FeatureOptions.SharedMountOptions = &SharedMountOptions{
				Enabled:       true,
				FuseSocketDir: socketDir,
				EmptyDirBasePath: func(uid string) string {
					return filepath.Join(emptyDirBaseRoot, uid)
				},
			}

			stageReq := &csi.NodeStageVolumeRequest{
				VolumeId:          volID,
				StagingTargetPath: testStagingPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					VolumeContextSharedNodeMount: "true",
					util.VolumeContextKeyPVName:  volID,
				},
				PublishContext: map[string]string{
					PublishContextKeyMounterPodName:      podName,
					PublishContextKeyMounterPodNamespace: podNamespace,
				},
			}

			_, err = ns.NodeStageVolume(context.Background(), stageReq)
			if err != nil {
				t.Fatalf("NodeStageVolume failed: %v", err)
			}
			defer func() {
				if vs, ok := ns.volumeStateStore.Load(testStagingPath); ok && vs != nil {
					if vs.GCSFuseKernelMonitorState.CancelFunc != nil {
						vs.GCSFuseKernelMonitorState.CancelFunc()
					}
				}
			}()

			emptyDirPath := ns.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath(string(podUID))
			flagFilePath := filepath.Join(emptyDirPath, FlagFileForDefaultingPath)
			content, readErr := os.ReadFile(flagFilePath)

			if tc.expectFlagFileWritten {
				if readErr != nil {
					t.Fatalf("expected flags-for-defaulting file at %q, got error: %v", flagFilePath, readErr)
				}
				flags := ParseFlagMapFromFlagFile(string(content))
				if got := flags["machine-type"]; got != expectedMachineType {
					t.Errorf("flags[%q] = %q, want %q", "machine-type", got, expectedMachineType)
				}
				if got := flags["disable-autoconfig"]; got != strconv.FormatBool(tc.disableAutoconfig) {
					t.Errorf("flags[%q] = %q, want %q", "disable-autoconfig", got, strconv.FormatBool(tc.disableAutoconfig))
				}
			} else {
				if readErr == nil {
					t.Errorf("expected no flags-for-defaulting file to be written, but found one with content: %q", string(content))
				}
			}
		})
	}
}

func TestAppendCloudProfilerOptions(t *testing.T) {
	t.Parallel()
	fakeMounter := mount.NewFakeMounter([]mount.MountPoint{})
	driver := initTestDriver(t, fakeMounter, clientset.NewFakeClientset())
	ns := newNodeServer(driver, fakeMounter).(*nodeServer)

	testCases := []struct {
		name                string
		mounterImage        string
		enableCloudProfiler bool
		podName             string
		podUID              string
		mountOptions        []string
		expectedOptions     []string
	}{
		{
			name:                "podName is empty - should return options unchanged",
			mounterImage:        "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.23.7-gke.0",
			enableCloudProfiler: true,
			podName:             "",
			podUID:              "test-uid",
			mountOptions:        []string{"debug_gcs", "ro"},
			expectedOptions:     []string{"debug_gcs", "ro"},
		},
		{
			name:                "podUID is empty - should return options unchanged",
			mounterImage:        "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.23.7-gke.0",
			enableCloudProfiler: true,
			podName:             "test-pod",
			podUID:              "",
			mountOptions:        []string{"debug_gcs", "ro"},
			expectedOptions:     []string{"debug_gcs", "ro"},
		},
		{
			name:                "enableCloudProfiler is false - should return options unchanged",
			mounterImage:        "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.23.7-gke.0",
			enableCloudProfiler: false,
			podName:             "test-pod",
			podUID:              "test-uid",
			mountOptions:        []string{"debug_gcs", "ro"},
			expectedOptions:     []string{"debug_gcs", "ro"},
		},
		{
			name:                "unsupported sidecar version - should return options unchanged",
			mounterImage:        "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.23.6-gke.0",
			enableCloudProfiler: true,
			podName:             "test-pod",
			podUID:              "test-uid",
			mountOptions:        []string{"debug_gcs", "ro"},
			expectedOptions:     []string{"debug_gcs", "ro"},
		},
		{
			name:                "valid inputs and supported sidecar - should append cloud profiler options",
			mounterImage:        "gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter:v1.23.7-gke.0",
			enableCloudProfiler: true,
			podName:             "test-pod",
			podUID:              "test-uid",
			mountOptions:        []string{"debug_gcs", "ro"},
			expectedOptions: []string{
				"debug_gcs",
				util.EnableCloudProfilerForSidecarConst + "=true",
				util.PodNameConst + "=test-pod",
				util.PodUIDConst + "=test-uid",
				"ro",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := ns.appendCloudProfilerOptions(tc.mounterImage, tc.enableCloudProfiler, tc.podName, tc.podUID, tc.mountOptions)
			if !reflect.DeepEqual(got, tc.expectedOptions) {
				t.Errorf("appendCloudProfilerOptions() = %v, want %v", got, tc.expectedOptions)
			}
		})
	}
}
