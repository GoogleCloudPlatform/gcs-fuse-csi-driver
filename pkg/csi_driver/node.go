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
	"k8s.io/klog"
	mount "k8s.io/mount-utils"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/auth"
	proxyclient "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/client"
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
	driver             *GCSDriver
	tokenManager       auth.TokenManager
	gcsfuseProxyClient proxyclient.ProxyClient
	mounter            mount.Interface
	volumeLocks        *util.VolumeLocks
}

func newNodeServer(driver *GCSDriver, mounter mount.Interface, gcsfuseProxyClient proxyclient.ProxyClient, tokenManager auth.TokenManager) csi.NodeServer {
	return &nodeServer{
		driver:             driver,
		tokenManager:       tokenManager,
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
	mountOptions := []string{}
	if vc[VolumeContextKeyEphemeral] == "true" {
		volumeID = vc[VolumeContextKeyBucketName]
		if len(volumeID) == 0 {
			return nil, status.Errorf(codes.InvalidArgument, "NodePublishVolume VolumeContext %q must be provided for ephemeral storage", VolumeContextKeyBucketName)
		}

		if ephemeralVolMountOptions, ok := vc[VolumeContextKeyMountOptions]; ok {
			mountOptions = joinMountOptions(mountOptions, strings.Split(ephemeralVolMountOptions, ","))
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
	if err := s.updateGCPToken(ctx, vc); err != nil {
		return nil, status.Errorf(codes.Internal, "updateGCPToken failed with error: %v", err)
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
		klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q, mount already exists.", volumeID, targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	if needsCreateDir {
		klog.V(4).Infof("NodePublishVolume attempting mkdir for path %q", targetPath)
		if err := os.MkdirAll(targetPath, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "mkdir failed for path %q (%v)", targetPath, err)
		}
	}

	// Call the proxy server to mount the GCS bucket.
	if capMount := req.GetVolumeCapability().GetMount(); capMount != nil {
		mountOptions = joinMountOptions(mountOptions, capMount.GetMountFlags())
	}
	if req.GetReadonly() {
		mountOptions = joinMountOptions(mountOptions, []string{"ro"})
	}
	if err := s.gcsfuseProxyClient.MountGCS(ctx, volumeID, targetPath, "gcsfuse", mountOptions, []string{}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount %q: %v", volumeID, err)
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
	err = s.gcsfuseProxyClient.DeleteGCPToken(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to cleanup token: %v", err)
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

// updateGCPToken calls GCS Fuse Proxy Server to update GCP token
func (s *nodeServer) updateGCPToken(ctx context.Context, vc map[string]string) error {
	sa, err := s.tokenManager.GetK8sServiceAccountFromVolumeContext(vc)
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes Service Account with error: %v", err)
	}

	if sa.Token == nil {
		return fmt.Errorf("VolumeContext does not contain Kubernetes Service Account token for Identity Pool")
	}

	podID := vc[VolumeContextKeyPodUID]
	expiry, err := s.gcsfuseProxyClient.GetGCPTokenExpiry(ctx, podID)
	if err == nil && expiry.After(time.Now().Add(time.Minute*5)) {
		klog.V(2).Infof("no need to update token for Pod %q", podID)
		return nil
	}

	ts, err := s.tokenManager.GetTokenSourceFromK8sServiceAccount(ctx, sa)
	if err != nil {
		return err
	}

	gcpToken, err := ts.Token()
	if err != nil {
		return err
	}

	err = s.gcsfuseProxyClient.PutGCPToken(ctx, podID, gcpToken)
	if err != nil {
		return fmt.Errorf("failed to put GCP token : %v", err)
	}
	return nil
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
