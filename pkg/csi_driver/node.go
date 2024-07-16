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
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
)

const (
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
	limiter               rate.Limiter
}

func newNodeServer(driver *GCSDriver, mounter mount.Interface) csi.NodeServer {
	return &nodeServer{
		driver:                driver,
		storageServiceManager: driver.config.StorageServiceManager,
		mounter:               mounter,
		volumeLocks:           util.NewVolumeLocks(),
		k8sClients:            driver.config.K8sClients,
		limiter:               *rate.NewLimiter(rate.Every(time.Second), 10),
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
	// Rate limit NodePublishVolume calls to avoid kube API throttling.
	if err := s.limiter.Wait(ctx); err != nil {
		return nil, status.Errorf(codes.Aborted, "NodePublishVolume request is aborted due to rate limit: %v", err)
	}

	// Validate arguments
	targetPath, bucketName, fuseMountOptions, skipBucketAccessCheck, err := parseRequestArguments(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	klog.V(6).Infof("NodePublishVolume on volume %q has skipBucketAccessCheck %t", bucketName, skipBucketAccessCheck)

	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Acquire a lock on the target path instead of volumeID, since we do not want to serialize multiple node publish calls on the same volume.
	if acquired := s.volumeLocks.TryAcquire(targetPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, targetPath)
	}
	defer s.volumeLocks.Release(targetPath)

	vc := req.GetVolumeContext()

	// Check if the given Service Account has the access to the GCS bucket, and the bucket exists.
	if bucketName != "_" && !skipBucketAccessCheck {
		storageService, err := s.prepareStorageService(ctx, vc)
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, "failed to prepare storage service: %v", err)
		}
		defer storageService.Close()

		if exist, err := storageService.CheckBucketExists(ctx, &storage.ServiceBucket{Name: bucketName}); !exist {
			return nil, status.Errorf(storage.ParseErrCode(err), "failed to get GCS bucket %q: %v", bucketName, err)
		}
	}

	// Check if the sidecar container was injected into the Pod
	pod, err := s.k8sClients.GetPod(vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyPodName])
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to get pod: %v", err)
	}

	// Since the webhook mutating ordering is not definitive,
	// the sidecar position is not checked in the ValidatePodHasSidecarContainerInjected func.
	shouldInjectedByWebhook := strings.ToLower(pod.Annotations[webhook.GcsFuseVolumeEnableAnnotation]) == util.TrueStr
	sidecarInjected, isInitContainer := webhook.ValidatePodHasSidecarContainerInjected(pod)
	if !sidecarInjected {
		if shouldInjectedByWebhook {
			return nil, status.Error(codes.Internal, "the webhook failed to inject the sidecar container into the Pod spec")
		}

		return nil, status.Error(codes.FailedPrecondition, "failed to find the sidecar container in Pod spec")
	}

	// Check if the sidecar container is still required,
	// if not, put an exit file to the emptyDir path to
	// notify the sidecar container to exit.
	if !isInitContainer {
		if err := putExitFile(pod, targetPath); err != nil {
			return nil, status.Errorf(codes.Internal, err.Error())
		}
	}

	// Check if there is any error from the gcsfuse
	code, err := checkGcsFuseErr(isInitContainer, pod, targetPath)
	if code != codes.OK {
		if code == codes.Canceled {
			klog.V(4).Infof("NodePublishVolume on volume %q to target path %q is not needed because the gcsfuse has terminated.", bucketName, targetPath)

			return &csi.NodePublishVolumeResponse{}, nil
		}

		return nil, status.Errorf(code, err.Error())
	}

	// Check if there is any error from the sidecar container
	code, err = checkSidecarContainerErr(isInitContainer, pod)
	if code != codes.OK {
		return nil, status.Errorf(code, err.Error())
	}

	// TODO: Check if the socket listener timed out

	// Check if the target path is already mounted
	mounted, err := s.isDirMounted(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if path %q is already mounted: %v", targetPath, err)
	}

	if mounted {
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
