/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog"
	mount "k8s.io/mount-utils"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/storage"
	sidecarmounter "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/sidecar_mounter"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/util"
)

// NodePublishVolume VolumeContext parameters
const (
	VolumeContextKeyPodUID       = "csi.storage.k8s.io/pod.uid"
	VolumeContextKeyEphemeral    = "csi.storage.k8s.io/ephemeral"
	VolumeContextKeyBucketName   = "bucketName"
	VolumeContextKeyMountOptions = "mountOptions"
)

// nodeServer handles mounting and unmounting of GCFS volumes on a node
type nodeServer struct {
	driver                *GCSDriver
	storageServiceManager storage.ServiceManager
	mounter               mount.Interface
	volumeLocks           *util.VolumeLocks
	chdirMu               sync.Mutex
}

func newNodeServer(driver *GCSDriver, mounter mount.Interface) csi.NodeServer {
	return &nodeServer{
		driver:                driver,
		storageServiceManager: driver.config.StorageServiceManager,
		mounter:               mounter,
		volumeLocks:           util.NewVolumeLocks(),
		chdirMu:               sync.Mutex{},
	}
}

func (s *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: s.driver.config.NodeID,
	}, nil
}

func (s *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: s.driver.nscap,
	}, nil
}

func (s *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	// Validate arguments
	bucketName := req.GetVolumeId()
	vc := req.GetVolumeContext()

	fuseMountOptions := []string{}
	if capMount := req.GetVolumeCapability().GetMount(); capMount != nil {
		fuseMountOptions = capMount.GetMountFlags()
	}

	if vc[VolumeContextKeyEphemeral] == "true" {
		bucketName = vc[VolumeContextKeyBucketName]
		if len(bucketName) == 0 {
			return nil, status.Errorf(codes.InvalidArgument, "NodePublishVolume VolumeContext %q must be provided for ephemeral storage", VolumeContextKeyBucketName)
		}

		if ephemeralVolMountOptions, ok := vc[VolumeContextKeyMountOptions]; ok {
			fuseMountOptions = joinMountOptions(fuseMountOptions, strings.Split(ephemeralVolMountOptions, ","))
		}
	}

	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume target path must be provided")
	}

	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Acquire a lock on the target path instead of volumeID, since we do not want to serialize multiple node publish calls on the same volume.
	if acquired := s.volumeLocks.TryAcquire(targetPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, targetPath)
	}
	defer s.volumeLocks.Release(targetPath)

	// Check the if the given Service Account has the access to the GCS bucket, and the bucket exists
	storageService, err := s.prepareStorageService(ctx, req.GetVolumeContext())
	if err != nil {
		return nil, err
	}

	_, err = storageService.GetBucket(ctx, &storage.ServiceBucket{Name: bucketName})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get GCS bucket %q: %v", bucketName, err)
	}

	// Prepare the temp volume base path
	podID, volumeName, err := util.ParsePodIDVolume(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to parse volume name from target path %p: %v", &targetPath, err)
	}
	volumeBasePath := fmt.Sprintf("/var/lib/kubelet/pods/%v/volumes/kubernetes.io~empty-dir/gke-gcsfuse/.volumes/%v", podID, volumeName)
	if err := os.MkdirAll(volumeBasePath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir failed for path %q: %v", volumeBasePath, err)
	}

	// Mount source
	mounted, err := s.isDirMounted(targetPath)
	if err != nil {
		return nil, err
	}

	if mounted {
		// Already mounted
		klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q, mount already exists.", bucketName, targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	} else {
		klog.V(4).Infof("NodePublishVolume attempting mkdir for path %q", targetPath)
		if err := os.MkdirAll(targetPath, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "mkdir failed for path %q (%v)", targetPath, err)
		}
	}

	// Prepare the mount options
	mountOptions := []string{
		"nodev",
		"nosuid",
		"noexec",
		"allow_other",
		"default_permissions",
		"rootmode=40000",
		fmt.Sprintf("user_id=%d", os.Getuid()),
		fmt.Sprintf("group_id=%d", os.Getgid()),
	}
	if req.GetReadonly() {
		mountOptions = joinMountOptions(mountOptions, []string{"ro"})
	}

	klog.V(4).Info("NodePublishVolume opening the device /dev/fuse")
	fd, err := syscall.Open("/dev/fuse", syscall.O_RDWR, 0644)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to open the device /dev/fuse: %v", err)
	}
	mountOptions = append(mountOptions, fmt.Sprintf("fd=%v", fd))

	klog.V(4).Info("NodePublishVolume mounting the fuse filesystem")
	err = s.mounter.MountSensitiveWithoutSystemdWithMountFlags(bucketName, targetPath, "fuse", mountOptions, nil, []string{"--internal-only"})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount the fuse filesystem: %v", err)
	}

	klog.V(4).Info("NodePublishVolume passing the descriptor")
	// Need to change the current working directory to the temp volume base path,
	// because the socket absolute path is longer than 104 characters,
	// which will cause "bind: invalid argument" errors.
	s.chdirMu.Lock()
	exPwd, _ := os.Getwd()
	os.Chdir(volumeBasePath)
	klog.V(4).Info("NodePublishVolume creating a listener for the socket")
	l, err := net.Listen("unix", "./socket")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create the listener for the socket: %v", err)
	}
	os.Chdir(exPwd)
	s.chdirMu.Unlock()

	// Prepare sidecar mounter MountConfig
	mc := sidecarmounter.MountConfig{
		BucketName: bucketName,
		Options:    fuseMountOptions,
	}
	mcb, err := json.Marshal(mc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to marchal sidecar mounter MountConfig %v: %v", mc, err)
	}

	go func(l net.Listener, bucketName, podID, volumeName string, msg []byte, fd int) {
		defer syscall.Close(fd)
		defer l.Close()

		logPrefix := fmt.Sprintf("[Pod %v, Volume %v, Bucket %v]", podID, volumeName, bucketName)
		klog.V(4).Infof("%v start to accept connections to the listener.", logPrefix)
		a, err := l.Accept()
		if err != nil {
			klog.Errorf("%v failed to accept connections to the listener: %v", logPrefix, err)
			return
		}
		defer a.Close()

		klog.V(4).Infof("%v start to send file descriptor and mount options", logPrefix)
		if err = util.SendMsg(a, fd, msg); err != nil {
			klog.Errorf("%v failed to send file descriptor and mount options: %v", logPrefix, err)
		}

		klog.V(4).Infof("%v exiting the goroutine.", logPrefix)
	}(l, bucketName, podID, volumeName, mcb, fd)

	klog.Infof("NodePublishVolume successfully mounts the bucket %q for Pod %q, volume %q", podID, volumeName, bucketName)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	// Validate arguments
	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeUnpublishVolume target path must be provided")
	}

	// Acquire a lock on the target path instead of volumeID, since we do not want to serialize multiple node unpublish calls on the same volume.
	if acquired := s.volumeLocks.TryAcquire(targetPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, targetPath)
	}
	defer s.volumeLocks.Release(targetPath)

	// Force unmount the target path.
	forceUnmounter, ok := s.mounter.(mount.MounterForceUnmounter)
	if !ok {
		return nil, status.Error(codes.Internal, "failed to cast the mounter to a forceUnmounter")
	}
	err := forceUnmounter.UnmountWithForce(targetPath, 10*time.Second)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to force unmount target target %q: %v", targetPath, err)
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// isDirMounted checks if the path is already a mount point
func (s *nodeServer) isDirMounted(targetPath string) (bool, error) {
	mps, err := s.mounter.List()
	if err != nil {
		return false, err
	}
	for _, m := range mps {
		if m.Path == targetPath {
			return true, nil
		}
	}
	return false, nil
}

// joinMountOptions joins mount options eliminating duplicates
func joinMountOptions(userOptions []string, systemOptions []string) []string {
	allMountOptions := sets.NewString()

	for _, mountOption := range userOptions {
		if len(mountOption) > 0 {
			allMountOptions.Insert(mountOption)
		}
	}

	for _, mountOption := range systemOptions {
		allMountOptions.Insert(mountOption)
	}
	return allMountOptions.List()
}

// prepareStorageService prepares the GCS Storage Service using CreateVolume/DeleteVolume sercets
func (s *nodeServer) prepareStorageService(ctx context.Context, vc map[string]string) (storage.Service, error) {
	sa, err := s.driver.config.TokenManager.GetK8sServiceAccountFromVolumeContext(vc)
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes Service Account with error: %v", err)
	}

	if sa.Token == nil {
		return nil, fmt.Errorf("VolumeContext does not contain Kubernetes Service Account token for Identity Pool")
	}
	ts, err := s.driver.config.TokenManager.GetTokenSourceFromK8sServiceAccount(ctx, sa)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "token manager failed to get token source: %v", err)
	}

	storageService, err := s.storageServiceManager.SetupService(ctx, ts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "storage service manager failed to setup service: %v", err)
	}

	return storageService, nil
}
