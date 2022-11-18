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
	"fmt"
	"os"
	"strings"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/storage"
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
}

func newNodeServer(driver *GCSDriver, mounter mount.Interface) csi.NodeServer {
	return &nodeServer{
		driver:                driver,
		storageServiceManager: driver.config.StorageServiceManager,
		mounter:               mounter,
		volumeLocks:           util.NewVolumeLocks(),
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
	if req.GetReadonly() {
		fuseMountOptions = joinMountOptions(fuseMountOptions, []string{"ro"})
	}
	if capMount := req.GetVolumeCapability().GetMount(); capMount != nil {
		fuseMountOptions = joinMountOptions(fuseMountOptions, capMount.GetMountFlags())
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

	// Check if the given Service Account has the access to the GCS bucket, and the bucket exists
	storageService, err := s.prepareStorageService(ctx, req.GetVolumeContext())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to prepare storage service: %v", err)
	}

	_, err = storageService.GetBucket(ctx, &storage.ServiceBucket{Name: bucketName})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get GCS bucket %q: %v", bucketName, err)
	}

	// Check if the target path is already mounted
	mounted, err := s.isDirMounted(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if path %q is already mounted: %v", targetPath, err)
	}

	if mounted {
		// Already mounted
		klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q, mount already exists.", bucketName, targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	} else {
		klog.V(4).Infof("NodePublishVolume attempting mkdir for path %q", targetPath)
		if err := os.MkdirAll(targetPath, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "mkdir failed for path %q: %v", targetPath, err)
		}
	}

	if err = s.mounter.Mount(bucketName, targetPath, "fuse", fuseMountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount volume %q to target path %q: %v", bucketName, targetPath, err)
	}

	klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q", bucketName, targetPath)
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

	// Check if the target path is already mounted
	if mounted, err := s.isDirMounted(targetPath); mounted || err != nil {
		if err != nil {
			klog.Errorf("failed to check if path %q is already mounted: %v", targetPath, err)
		}
		// Force unmount the target path
		// Try to do force unmount firstly becasue if the file descriptor was not closed,
		// mount.CleanupMountPoint() call will hang.
		forceUnmounter, ok := s.mounter.(mount.MounterForceUnmounter)
		if ok {
			if err = forceUnmounter.UnmountWithForce(targetPath, time.Second); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to force unmount target path %q: %v", targetPath, err)
			}
		} else {
			klog.Warningf("failed to cast the mounter to a forceUnmounter, proceed with the default mounter Unmount")
			if err = s.mounter.Unmount(targetPath); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to unmount target path %q: %v", targetPath, err)
			}
		}
	}

	// Cleanup the mount point
	if err := mount.CleanupMountPoint(targetPath, s.mounter, false /* bind mount */); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to cleanup the mount point %q: %v", targetPath, err)
	}

	klog.V(4).Infof("NodeUnpublishVolume succeeded on target path %q", targetPath)
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

// prepareStorageService prepares the GCS Storage Service using the Kubernetes Service Account from VolumeContext
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
