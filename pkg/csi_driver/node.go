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

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog"
	mount "k8s.io/mount-utils"
	proxyclient "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/client"
	mount_gcs "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/pb"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/util"
)

// NodePublishVolume VolumeContext parameters
const (
	VolumeContextKeyEphemeral  = "csi.storage.k8s.io/ephemeral"
	VolumeContextKeyBucketName = "bucketName"
)

// nodeServer handles mounting and unmounting of GCFS volumes on a node
type nodeServer struct {
	driver             *GCSDriver
	gcsfuseProxyClient proxyclient.ProxyClient
	mounter            mount.Interface
	volumeLocks        *util.VolumeLocks
}

func newNodeServer(driver *GCSDriver, mounter mount.Interface, gcsfuseProxyClient proxyclient.ProxyClient) csi.NodeServer {
	return &nodeServer{
		driver:             driver,
		gcsfuseProxyClient: gcsfuseProxyClient,
		mounter:            mounter,
		volumeLocks:        util.NewVolumeLocks(),
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
	volumeID := req.GetVolumeId()
	vc := req.GetVolumeContext()
	if vc[VolumeContextKeyEphemeral] == "true" {
		volumeID = vc[VolumeContextKeyBucketName]
		if len(volumeID) == 0 {
			return nil, status.Errorf(codes.InvalidArgument, "NodePublishVolume VolumeContext %s must be provided for ephemeral storage", VolumeContextKeyBucketName)
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

	// Update GCP token on the proxy server.
	if err := s.gcsfuseProxyClient.UpdateGCPToken(ctx, vc); err != nil {
		return nil, status.Errorf(codes.Internal, "UpdateGCPToken failed with error: %v", err)
	}

	// Mount source
	mounted, err := s.isDirMounted(targetPath)
	needsCreateDir := false
	if err != nil {
		if os.IsNotExist(err) {
			needsCreateDir = true
		} else {
			return nil, err
		}
	}

	if mounted {
		// Already mounted
		klog.V(4).Infof("NodePublishVolume succeeded on volume %v to target path %s, mount already exists.", volumeID, targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	if needsCreateDir {
		klog.V(4).Infof("NodePublishVolume attempting mkdir for path %s", targetPath)
		if err := os.MkdirAll(targetPath, 0750); err != nil {
			return nil, fmt.Errorf("mkdir failed for path %s (%v)", targetPath, err)
		}
	}

	// Call the proxy server to mount the GCS bucket.
	options := []string{}
	if capMount := req.GetVolumeCapability().GetMount(); capMount != nil {
		options = append(options, capMount.GetMountFlags()...)
	}
	if req.GetReadonly() {
		options = append(options, "ro")
	}
	mountreq := mount_gcs.MountGCSRequest{
		Source:           volumeID,
		Target:           targetPath,
		Fstype:           "gcsfuse",
		Options:          options,
		SensitiveOptions: []string{},
	}
	if err := s.gcsfuseProxyClient.MountGCS(ctx, &mountreq); err != nil {
		return nil, status.Errorf(codes.Internal, "MountGCS failed with error: %v", err)
	}
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

	// Clean up the mount point.
	err := mount.CleanupMountPoint(targetPath, s.mounter, false /*extensiveMountPointCheck*/)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount target target %q: %v", targetPath, err)
	}

	// Clean up the GCP token on the proxy server.
	err = s.gcsfuseProxyClient.CleanupGCPToken(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "CleanupGCPToken failed with error: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// isDirMounted checks if the path is already a mount point
func (s *nodeServer) isDirMounted(targetPath string) (bool, error) {
	notMnt, err := s.mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		return false, err
	}
	if !notMnt {
		// Already mounted
		return true, nil
	}
	return false, nil
}
