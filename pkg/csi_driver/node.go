/*
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
	"os"
	"path/filepath"
	"strings"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
)

// NodePublishVolume VolumeContext parameters.
const (
	VolumeContextKeyServiceAccountName = "csi.storage.k8s.io/serviceAccount.name"
	//nolint:gosec
	VolumeContextKeyServiceAccountToken = "csi.storage.k8s.io/serviceAccount.tokens"
	VolumeContextKeyPodName             = "csi.storage.k8s.io/pod.name"
	VolumeContextKeyPodNamespace        = "csi.storage.k8s.io/pod.namespace"
	VolumeContextKeyEphemeral           = "csi.storage.k8s.io/ephemeral"
	VolumeContextKeyBucketName          = "bucketName"
	VolumeContextKeyMountOptions        = "mountOptions"
)

// nodeServer handles mounting and unmounting of GCFS volumes on a node.
type nodeServer struct {
	driver                *GCSDriver
	storageServiceManager storage.ServiceManager
	mounter               mount.Interface
	volumeLocks           *util.VolumeLocks
	k8sClients            clientset.Interface
}

func newNodeServer(driver *GCSDriver, mounter mount.Interface) csi.NodeServer {
	return &nodeServer{
		driver:                driver,
		storageServiceManager: driver.config.StorageServiceManager,
		mounter:               mounter,
		volumeLocks:           util.NewVolumeLocks(),
		k8sClients:            driver.config.K8sClients,
	}
}

func (s *nodeServer) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: s.driver.config.NodeID,
	}, nil
}

func (s *nodeServer) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
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
	if mountOptions, ok := vc[VolumeContextKeyMountOptions]; ok {
		fuseMountOptions = joinMountOptions(fuseMountOptions, strings.Split(mountOptions, ","))
	}

	if vc[VolumeContextKeyEphemeral] == "true" {
		bucketName = vc[VolumeContextKeyBucketName]
		if len(bucketName) == 0 {
			return nil, status.Errorf(codes.InvalidArgument, "NodePublishVolume VolumeContext %q must be provided for ephemeral storage", VolumeContextKeyBucketName)
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
	if bucketName != "_" {
		storageService, err := s.prepareStorageService(ctx, req.GetVolumeContext())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to prepare storage service: %v", err)
		}

		_, err = storageService.GetBucket(ctx, &storage.ServiceBucket{Name: bucketName})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get GCS bucket %q: %v", bucketName, err)
		}
	}

	// Check if the sidecar container was injected into the Pod
	pod, err := s.k8sClients.GetPod(ctx, vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyPodName])
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get pod: %v", err)
	}
	if !webhook.ValidatePodHasSidecarContainerInjected(s.driver.config.SidecarImage, pod) {
		return nil, status.Errorf(codes.Internal, "failed to find the sidecar container in Pod spec")
	}

	// Check if the Pod is owned by a Job
	isOwnedByJob := false
	for _, o := range pod.ObjectMeta.OwnerReferences {
		if o.Kind == "Job" {
			isOwnedByJob = true

			break
		}
	}

	// Check if all the containers besides the sidecar container exited
	sidecarShouldExit := true
	if isOwnedByJob {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name != webhook.SidecarContainerName && cs.State.Terminated == nil {
				sidecarShouldExit = false

				break
			}
		}
	}

	// Prepare the emptyDir path for the mounter to pass the file descriptor
	emptyDirBasePath, err := util.PrepareEmptyDir(targetPath, true)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to prepare emptyDir path: %v", err)
	}

	// Put an exit file to notify the sidecar container to exit
	if isOwnedByJob && sidecarShouldExit {
		klog.V(4).Info("all the other containers exited in the Job Pod, put the exit file.")
		exitFilePath := filepath.Dir(emptyDirBasePath) + "/exit"
		f, err := os.Create(exitFilePath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to put the exit file: %v", err)
		}
		f.Close()
		err = os.Chown(exitFilePath, webhook.NobodyUID, webhook.NobodyGID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to change ownership on the exit file: %v", err)
		}
	}

	// Check if there is any error from the sidecar container
	errMsg, err := os.ReadFile(emptyDirBasePath + "/error")
	if err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "failed to open error file %q: %v", emptyDirBasePath+"/error", err)
	}
	if err == nil && len(errMsg) > 0 {
		return nil, status.Errorf(codes.Internal, "the sidecar container failed with error: %v", string(errMsg))
	}

	// TODO: Check if the socket listener timed out

	// Check if the target path is already mounted
	mounted, err := s.isDirMounted(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if path %q is already mounted: %v", targetPath, err)
	}

	if mounted {
		// Already mounted
		klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q, mount already exists.", bucketName, targetPath)

		return &csi.NodePublishVolumeResponse{}, nil
	}
	klog.V(4).Infof("NodePublishVolume attempting mkdir for path %q", targetPath)
	if err := os.MkdirAll(targetPath, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir failed for path %q: %v", targetPath, err)
	}

	// Start to mount
	if err = s.mounter.Mount(bucketName, targetPath, "fuse", fuseMountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount volume %q to target path %q: %v", bucketName, targetPath, err)
	}

	klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q", bucketName, targetPath)

	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *nodeServer) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
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
		// Try to do force unmount firstly because if the file descriptor was not closed,
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

// isDirMounted checks if the path is already a mount point.
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

// joinMountOptions joins mount options eliminating duplicates.
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

// prepareStorageService prepares the GCS Storage Service using the Kubernetes Service Account from VolumeContext.
func (s *nodeServer) prepareStorageService(ctx context.Context, vc map[string]string) (storage.Service, error) {
	ts, err := s.driver.config.TokenManager.GetTokenSourceFromK8sServiceAccount(vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyServiceAccountName], vc[VolumeContextKeyServiceAccountToken])
	if err != nil {
		return nil, status.Errorf(codes.Internal, "token manager failed to get token source: %v", err)
	}

	storageService, err := s.storageServiceManager.SetupService(ctx, ts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "storage service manager failed to setup service: %v", err)
	}

	return storageService, nil
}
