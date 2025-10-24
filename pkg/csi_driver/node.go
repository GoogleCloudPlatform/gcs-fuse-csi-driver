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
	"strconv"
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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
)

const (
	UmountTimeout = time.Second * 5

	FuseMountType = "fuse"
)

// nodeServer handles mounting and unmounting of GCS FUSE volumes on a node.
type nodeServer struct {
	csi.UnimplementedNodeServer
	driver                *GCSDriver
	storageServiceManager storage.ServiceManager
	mounter               mount.Interface
	volumeLocks           *util.VolumeLocks
	k8sClients            clientset.Interface
	limiter               rate.Limiter
	volumeStateStore      *util.VolumeStateStore
}

func newNodeServer(driver *GCSDriver, mounter mount.Interface) csi.NodeServer {
	return &nodeServer{
		driver:                driver,
		storageServiceManager: driver.config.StorageServiceManager,
		mounter:               mounter,
		volumeLocks:           util.NewVolumeLocks(),
		k8sClients:            driver.config.K8sClients,
		limiter:               *rate.NewLimiter(rate.Every(time.Second), 10),
		volumeStateStore:      util.NewVolumeStateStore(),
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
	targetPath, bucketName, userSpecifiedIdentityProvider, fuseMountOptions, skipBucketAccessCheck, disableMetricsCollection, optInHostnetworkKSA, enableCloudProfilerForSidecar, err := parseRequestArguments(req)
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
	// skip check if it has ever succeeded
	if bucketName != "_" && !skipBucketAccessCheck {
		// Use target path as an volume identifier because it corresponds to Pods and volumes.
		// Pods may belong to different namespaces and would need their own access check.
		vs, ok := s.volumeStateStore.Load(targetPath)
		if !ok {
			s.volumeStateStore.Store(targetPath, &util.VolumeState{})
			vs, _ = s.volumeStateStore.Load(targetPath)
		}
		// volumeState is safe to access for remaining of function since volumeLock prevents
		// Node Publish/Unpublish Volume calls from running more than once at a time per volume.
		if !vs.BucketAccessCheckPassed {
			storageService, err := s.prepareStorageService(ctx, vc)
			if err != nil {
				return nil, status.Errorf(codes.Unauthenticated, "failed to prepare storage service: %v", err)
			}
			defer storageService.Close()

			if exist, err := storageService.CheckBucketExists(ctx, &storage.ServiceBucket{Name: bucketName}); !exist {
				return nil, status.Errorf(storage.ParseErrCode(err), "failed to get GCS bucket %q: %v", bucketName, err)
			}

			vs.BucketAccessCheckPassed = true
		}
	}

	// Check if the sidecar container was injected into the Pod
	pod, err := s.k8sClients.GetPod(vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyPodName])
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to get pod: %v", err)
	}
	gcsFuseSidecarImage := gcsFuseSidecarContainerImage(pod)
	enableSidecarBucketAccessCheckForSidecarVersion := s.driver.config.EnableSidecarBucketAccessCheck && gcsFuseSidecarImage != "" && isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, SidecarBucketAccessCheckMinVersion)
	identityProvider := ""
	shouldPopulateIdentityProviderForHnwKSA := s.shouldPopulateIdentityProvider(pod, optInHostnetworkKSA, userSpecifiedIdentityProvider != "")
	if shouldPopulateIdentityProviderForHnwKSA {
		if userSpecifiedIdentityProvider != "" {
			identityProvider = userSpecifiedIdentityProvider
		} else {
			identityProvider = s.driver.config.TokenManager.GetIdentityProvider()
		}
	} else if enableSidecarBucketAccessCheckForSidecarVersion {
		// As host network and sidecar bucket access check are exclusive the below assignment doesn't overwrite any of host network settings.
		identityProvider = s.driver.config.TokenManager.GetIdentityProvider()
	}

	enableCloudProfilerForVersion := enableCloudProfilerForSidecar && gcsFuseSidecarImage != "" && isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, SidecarCloudProfilerMinVersion)
	identityPool := s.driver.config.TokenManager.GetIdentityPool()
	fuseMountOptions, err = addFuseMountOptions(identityProvider, identityPool, fuseMountOptions, vc, shouldPopulateIdentityProviderForHnwKSA, enableSidecarBucketAccessCheckForSidecarVersion, enableCloudProfilerForVersion)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to prepare sidecar fuse mount options: %v", err)
	}

	node, err := s.k8sClients.GetNode(s.driver.config.NodeID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to get node: %v", err)
	}

	if s.driver.config.WINodeLabelCheck {
		val, ok := node.Labels[clientset.GkeMetaDataServerKey]
		// If Workload Identity is not enabled, the key should be missing; the check for "val == false" is just for extra caution
		isWorkloadIdentityDisabled := val != "true" || !ok
		if isWorkloadIdentityDisabled && !pod.Spec.HostNetwork {
			return nil, status.Errorf(codes.FailedPrecondition, "Workload Identity Federation is not enabled on node. Please make sure this is enabled on both cluster and node pool level (https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity)")
		}
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

	// Register metrics collector.
	// It is idempotent to register the same collector in node republish calls.
	if s.driver.config.MetricsManager != nil && !disableMetricsCollection {
		klog.V(6).Infof("NodePublishVolume enabling metrics collector for target path %q", targetPath)
		s.driver.config.MetricsManager.RegisterMetricsCollector(targetPath, pod.Namespace, pod.Name, bucketName)
	}

	// Check if the sidecar container is still required,
	// if not, put an exit file to the emptyDir path to
	// notify the sidecar container to exit.
	if !isInitContainer {
		if err := putExitFile(pod, targetPath); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	// Only pass mountOptions flags for defaulting if sidecar container is managed and satisfies min version requirement
	if gcsFuseSidecarImage != "" && isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, MachineTypeAutoConfigSidecarMinVersion) {
		shouldDisableAutoConfig := s.driver.config.DisableAutoconfig
		machineType, ok := node.Labels[clientset.MachineTypeKey]
		if ok {
			flagMap := map[string]string{"machine-type": machineType, "disable-autoconfig": strconv.FormatBool(shouldDisableAutoConfig)}
			if err := PutFlagsFromDriverToTargetPath(flagMap, targetPath, FlagFileForDefaultingPath); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		} else {
			klog.Warningf("Unable to fetch target node %v's machine type", node.Name)
		}
	}

	// Check if the target path is already mounted
	mounted, err := s.isDirMounted(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if path %q is already mounted: %v", targetPath, err)
	}

	// Skip mount if the mount on target path already exists.
	if mounted {
		// Check if there is any error from the gcsfuse
		// We only check for sidecar liveliness if the path is reported as mounted.
		// This indicates the initial kernel mount is complete, and gcsfuse is ready to proceed.
		// With this approach, NodePublishVolume will still report an error if gcsfuse itself fails.
		code, err := checkGcsFuseErr(isInitContainer, pod, targetPath)
		if code != codes.OK {
			if code == codes.Canceled {
				klog.V(4).Infof("NodePublishVolume on volume %q to target path %q is not needed because the gcsfuse has terminated.", bucketName, targetPath)

				return &csi.NodePublishVolumeResponse{}, nil
			}

			return nil, status.Error(code, err.Error())
		}

		// Check if there is any error from the sidecar container
		code, err = checkSidecarContainerErr(isInitContainer, pod)
		if code != codes.OK {
			return nil, status.Error(code, err.Error())
		}

		// TODO: Check if the socket listener timed out

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

	// Unregister metrics collecter.
	// It is idempotent to unregister the same collector.
	if s.driver.config.MetricsManager != nil {
		s.driver.config.MetricsManager.UnregisterMetricsCollector(targetPath)
	}

	s.volumeStateStore.Delete(targetPath)

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
	ts := s.driver.config.TokenManager.GetTokenSourceFromK8sServiceAccount(vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyServiceAccountName], vc[VolumeContextKeyServiceAccountToken], "" /*audience*/, false)
	storageService, err := s.storageServiceManager.SetupService(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("storage service manager failed to setup service: %w", err)
	}

	return storageService, nil
}

func (s *nodeServer) shouldPopulateIdentityProvider(pod *corev1.Pod, optInHnwKSA bool, userInput bool) bool {
	if !optInHnwKSA {
		return false
	}

	if !pod.Spec.HostNetwork {
		return false
	}

	tokenVolumeInjected := false
	for _, vol := range pod.Spec.Volumes {
		if vol.Name == webhook.SidecarContainerSATokenVolumeName {
			klog.V(6).Infof("Service Account Token Injection feature is turned on from webhook")

			tokenVolumeInjected = true

			break
		}
	}
	var sidecarVersionSupported bool

	for _, container := range pod.Spec.InitContainers {
		if container.Name == webhook.GcsFuseSidecarName {
			sidecarVersionSupported = isSidecarVersionSupportedForGivenFeature(container.Image, TokenServerSidecarMinVersion)

			break
		}
	}

	return tokenVolumeInjected && (sidecarVersionSupported || userInput)
}

func gcsFuseSidecarContainerImage(pod *corev1.Pod) string {
	for _, container := range pod.Spec.InitContainers {
		if container.Name == webhook.GcsFuseSidecarName {
			return container.Image
		}
	}
	return ""
}

func addFuseMountOptions(identityProvider string, identityPool string, fuseMountOptions []string, vc map[string]string, hostNetworkEnabled bool, enableSidecarBucketAccessCheckForSidecarVersion bool, enableCloudProfilerForVersion bool) ([]string, error) {
	if hostNetworkEnabled {
		// Identity Provider is populated separately for host network enabled workloads.
		fuseMountOptions = joinMountOptions(fuseMountOptions, []string{util.OptInHnw + "=true", util.TokenServerIdentityProviderConst + "=" + identityProvider})
	} else if enableSidecarBucketAccessCheckForSidecarVersion {
		if identityPool == "" || identityProvider == "" {
			// Identity pool and provider details are used in sidecar mounter to create the metadata service
			return nil, fmt.Errorf("failed to get either of identity pool %q or identity provider %q", identityPool, identityProvider)
		}
		// Enable sidecar bucket access check only for Workload Identity workloads. This feature consumes additional quota for Host Network pods as we do not have token caching.
		fuseMountOptions = joinMountOptions(fuseMountOptions, []string{
			util.PodNamespaceConst + "=" + vc[VolumeContextKeyPodNamespace],
			util.ServiceAccountNameConst + "=" + vc[VolumeContextKeyServiceAccountName],
			util.TokenServerIdentityPoolConst + "=" + identityPool,
			util.TokenServerIdentityProviderConst + "=" + identityProvider,
			util.EnableSidecarBucketAccessCheckConst + "=" + strconv.FormatBool(enableSidecarBucketAccessCheckForSidecarVersion)})
	}
	klog.V(6).Infof("NodePublishVolume populating identity provider %q in mount options", identityProvider)
	if enableCloudProfilerForVersion {
		fuseMountOptions = joinMountOptions(fuseMountOptions, []string{util.EnableCloudProfilerForSidecarConst + "=" + strconv.FormatBool(enableCloudProfilerForVersion)})
	}
	return fuseMountOptions, nil
}
