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
	"fmt"
	"os"
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
	corev1 "k8s.io/api/core/v1"
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

	UmountTimeout = time.Second * 5

	FuseMountType = "fuse"
)

// nodeServer handles mounting and unmounting of GCS FUSE volumes on a node.
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
		// Delegate fsGroup to CSI Driver
		// Set gid, file-mode, and dir-mode for gcsfuse.
		// Allow users to overwrite these flags.
		if capMount.GetVolumeMountGroup() != "" {
			fuseMountOptions = joinMountOptions(fuseMountOptions, []string{"gid=" + capMount.GetVolumeMountGroup(), "file-mode=664", "dir-mode=775"})
		}
		fuseMountOptions = joinMountOptions(fuseMountOptions, capMount.GetMountFlags())
	}

	fuseMountOptions = parseVolumeAttributes(fuseMountOptions, vc)

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

	// Check if the given Service Account has the access to the GCS bucket, and the bucket exists.
	if bucketName != "_" {
		storageService, err := s.prepareStorageService(ctx, req.GetVolumeContext())
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, "failed to prepare storage service: %v", err)
		}
		defer storageService.Close()

		if exist, err := storageService.CheckBucketExists(ctx, &storage.ServiceBucket{Name: bucketName}); !exist {
			code := codes.Internal
			if storage.IsNotExistErr(err) {
				code = codes.NotFound
			}

			if storage.IsPermissionDeniedErr(err) {
				code = codes.PermissionDenied
			}

			return nil, status.Errorf(code, "failed to get GCS bucket %q: %v", bucketName, err)
		}
	}

	// Check if the sidecar container was injected into the Pod
	pod, err := s.k8sClients.GetPod(ctx, vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyPodName])
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get pod: %v", err)
	}

	// Since the webhook mutating ordering is not definitive,
	// the sidecar position is not checked in the ValidatePodHasSidecarContainerInjected func.
	shouldInjectedByWebhook := strings.ToLower(pod.Annotations[webhook.AnnotationGcsfuseVolumeEnableKey]) == "true"
	sidecarInjected, isInitContainer := webhook.ValidatePodHasSidecarContainerInjected(pod, false)
	if !sidecarInjected {
		if shouldInjectedByWebhook {
			return nil, status.Error(codes.Internal, "the webhook failed to inject the sidecar container into the Pod spec")
		}

		return nil, status.Error(codes.FailedPrecondition, "failed to find the sidecar container in Pod spec")
	}

	// Prepare the emptyDir path for the mounter to pass the file descriptor
	emptyDirBasePath, err := util.PrepareEmptyDir(targetPath, true)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to prepare emptyDir path: %v", err)
	}

	// Check if the sidecar container is still required,
	// if not, put an exit file to the emptyDir path to
	// notify the sidecar container to exit.
	if !isInitContainer {
		if err := putExitFile(pod, emptyDirBasePath); err != nil {
			return nil, status.Errorf(codes.Internal, err.Error())
		}
	}

	// Check if there is any error from the sidecar container
	errMsg, err := os.ReadFile(emptyDirBasePath + "/error")
	if err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "failed to open error file %q: %v", emptyDirBasePath+"/error", err)
	}
	if err == nil && len(errMsg) > 0 {
		errMsgStr := string(errMsg)
		code := codes.Internal
		if strings.Contains(errMsgStr, "Incorrect Usage") {
			code = codes.InvalidArgument
		}

		if strings.Contains(errMsgStr, "signal: killed") {
			code = codes.ResourceExhausted
		}

		if strings.Contains(errMsgStr, "signal: terminated") {
			klog.V(4).Infof("NodePublishVolume on volume %q to target path %q is not needed because the sidecar container has terminated.", bucketName, targetPath)

			return &csi.NodePublishVolumeResponse{}, nil
		}

		if strings.Contains(errMsgStr, "googleapi: Error 403") ||
			strings.Contains(errMsgStr, "IAM returned 403 Forbidden: Permission") ||
			strings.Contains(errMsgStr, "google: could not find default credentials") {
			code = codes.PermissionDenied
		}

		return nil, status.Errorf(code, "the sidecar container failed with error: %v", errMsgStr)
	}

	var containerStatusList []corev1.ContainerStatus
	// Use ContainerStatuses or InitContainerStatuses
	if isInitContainer {
		containerStatusList = pod.Status.InitContainerStatuses
	} else {
		containerStatusList = pod.Status.ContainerStatuses
	}

	// Check if the sidecar container terminated
	for _, cs := range containerStatusList {
		if cs.Name != webhook.SidecarContainerName {
			continue
		}

		var reason string
		var exitCode int32
		if cs.RestartCount > 0 && cs.LastTerminationState.Terminated != nil {
			reason = cs.LastTerminationState.Terminated.Reason
			exitCode = cs.LastTerminationState.Terminated.ExitCode
		} else if cs.State.Terminated != nil {
			reason = cs.State.Terminated.Reason
			exitCode = cs.State.Terminated.ExitCode
		}

		if exitCode != 0 {
			code := codes.Internal
			if reason == "OOMKilled" || exitCode == 137 {
				code = codes.ResourceExhausted
			}

			return nil, status.Errorf(code, "the sidecar container terminated due to %v, exit code: %v", reason, exitCode)
		}
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
	if err = s.mounter.Mount(bucketName, targetPath, FuseMountType, fuseMountOptions); err != nil {
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
			if err = forceUnmounter.UnmountWithForce(targetPath, UmountTimeout); err != nil {
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

// prepareStorageService prepares the GCS Storage Service using the Kubernetes Service Account from VolumeContext.
func (s *nodeServer) prepareStorageService(ctx context.Context, vc map[string]string) (storage.Service, error) {
	ts := s.driver.config.TokenManager.GetTokenSourceFromK8sServiceAccount(vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyServiceAccountName], vc[VolumeContextKeyServiceAccountToken])
	storageService, err := s.storageServiceManager.SetupService(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("storage service manager failed to setup service: %w", err)
	}

	return storageService, nil
}
