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
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles"
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
	UmountTimeout = 5 * time.Microsecond
	// GCSFuseKernelParamsFilePollInterval is the interval at which the GCSFuse kernel
	// parameters file is polled and any changes to kernel parameter files are applied.
	GCSFuseKernelParamsFilePollInterval = time.Second * 5
	FuseMountType                       = "fuse"
)

// nodeServer handles mounting and unmounting of GCS FUSE volumes on a node.
type nodeServer struct {
	csi.UnimplementedNodeServer
	driver                *GCSDriver
	storageServiceManager storage.ServiceManager
	mounter               mount.Interface
	nwMgr                 NetworkManager
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
		nwMgr:                 driver.config.NetworkManager,
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

	// Validate the target path.
	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "NodePublishVolume target path must be provided")
	}

	// Get the Pod object.
	vc := req.GetVolumeContext()
	pod, err := s.k8sClients.GetPod(vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyPodName])
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to get pod: %v", err)
	}
	sidecarInjected, isInitContainer := webhook.ValidatePodHasSidecarContainerInjected(pod)
	gcsFuseSidecarImage := gcsFuseSidecarContainerImage(pod)

	// Recommend VolumeAttributes if the gcsfuse profiles feature is enabled.
	profilesEnabled := s.driver.config.FeatureOptions.FeatureGCSFuseProfiles.Enabled
	var profile *profiles.ProfileConfig
	if profilesEnabled {
		klog.V(4).Infof("NodePublishVolume gcsfuse profiles feature is enabled for pod %s/%s", pod.Namespace, pod.Name)
		profile, err = profiles.BuildProfileConfig(&profiles.BuildProfileConfigParams{
			TargetPath:          targetPath,
			Clientset:           s.k8sClients,
			VolumeAttributeKeys: transformKeysToSet(volumeAttributesToMountOptionsMapping),
			PodNamespace:        pod.Namespace,
			PodName:             pod.Name,
			NodeName:            s.driver.config.NodeID,
			IsInitContainer:     isInitContainer,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to build profile config: %w", err)
		}
		if profile != nil {
			// Merge profile VolumeAttributes into the VolumeContext, respecting pre-existing keys.
			vc = profile.MergeVolumeAttributesOnRecommendedMissingKeys(vc)
		}
	}

	// Validate arguments
	args, err := parseRequestArguments(req, vc)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	klog.V(6).Infof("NodePublishVolume on volume %q has skipBucketAccessCheck %t", args.bucketName, args.skipCSIBucketAccessCheck)

	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Acquire a lock on the target path instead of volumeID, since we do not want to serialize multiple node publish calls on the same volume.
	if acquired := s.volumeLocks.TryAcquire(targetPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, targetPath)
	}
	defer s.volumeLocks.Release(targetPath)

	// Use target path as an volume identifier because it corresponds to Pods and volumes.
	// Pods may belong to different namespaces and would need their own bucket access check.
	// We initialize volumeState iff bucket name is NOT "_", I.e. It's not a dynamic mount.
	var vs *util.VolumeState
	if args.bucketName != "_" {
		var ok bool
		if vs, ok = s.volumeStateStore.Load(targetPath); !ok {
			s.volumeStateStore.Store(targetPath, &util.VolumeState{})
			vs, _ = s.volumeStateStore.Load(targetPath)
		}
	}
	// volumeState is safe to access if its not nil for remaining of function since volumeLock prevents
	// Node Publish/Unpublish Volume calls from running more than once at a time per volume.

	// Check if the given Service Account has the access to the GCS bucket, and the bucket exists.
	// skip check if it has ever succeeded
	if vs != nil && !args.skipCSIBucketAccessCheck {
		if !vs.BucketAccessCheckPassed {
			storageService, err := s.prepareStorageService(ctx, vc)
			if err != nil {
				return nil, status.Errorf(codes.Unauthenticated, "failed to prepare storage service: %v", err)
			}
			defer storageService.Close()

			if exist, err := storageService.CheckBucketExists(ctx, &storage.ServiceBucket{Name: args.bucketName}); !exist {
				return nil, status.Errorf(storage.ParseErrCode(err), "failed to get GCS bucket %q: %v", args.bucketName, err)
			}

			vs.BucketAccessCheckPassed = true
		}
	}

	enableSidecarBucketAccessCheckForSidecarVersion := s.driver.config.EnableSidecarBucketAccessCheck && gcsFuseSidecarImage != "" && s.driver.isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, SidecarBucketAccessCheckMinVersion)
	identityProvider := ""
	if s.shouldPopulateIdentityProvider(pod, args.optInHostnetworkKSA, args.userSpecifiedIdentityProvider != "") {
		if args.userSpecifiedIdentityProvider != "" {
			identityProvider = args.userSpecifiedIdentityProvider
		} else {
			identityProvider = s.driver.config.TokenManager.GetIdentityProvider()
		}
		klog.V(6).Infof("NodePublishVolume populating identity provider %q in mount options", identityProvider)
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.OptInHnw + "=true", util.TokenServerIdentityProviderConst + "=" + identityProvider})
	} else if enableSidecarBucketAccessCheckForSidecarVersion {
		//Enable sidecar bucket access check only for Workload Identity workloads. This feature consumes additional quota for Host Network pods as we do not have token caching.
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.EnableSidecarBucketAccessCheckConst + "=" + strconv.FormatBool(s.driver.config.EnableSidecarBucketAccessCheck)})
	}

	if enableSidecarBucketAccessCheckForSidecarVersion {
		if identityProvider == "" {
			identityProvider = s.driver.config.TokenManager.GetIdentityProvider()
			args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.TokenServerIdentityProviderConst + "=" + identityProvider})
		}
		klog.Infof("Got identity provider %s", identityProvider)

		identityPool := s.driver.config.TokenManager.GetIdentityPool()
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{
			util.PodNamespaceConst + "=" + vc[VolumeContextKeyPodNamespace],
			util.ServiceAccountNameConst + "=" + vc[VolumeContextKeyServiceAccountName],
			util.TokenServerIdentityPoolConst + "=" + identityPool})
	}

	if args.enableCloudProfilerForSidecar && gcsFuseSidecarImage != "" && s.driver.isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, SidecarCloudProfilerMinVersion) {
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.EnableCloudProfilerForSidecarConst + "=" + strconv.FormatBool(args.enableCloudProfilerForSidecar)})
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
	if !sidecarInjected {
		if shouldInjectedByWebhook {
			return nil, status.Error(codes.Internal, "the webhook failed to inject the sidecar container into the Pod spec")
		}

		return nil, status.Error(codes.FailedPrecondition, "failed to find the sidecar container in Pod spec")
	}

	// Register metrics collector.
	// It is idempotent to register the same collector in node republish calls.
	if s.driver.config.MetricsManager != nil && !args.disableMetricsCollection {
		klog.V(6).Infof("NodePublishVolume enabling metrics collector for target path %q", targetPath)
		s.driver.config.MetricsManager.RegisterMetricsCollector(targetPath, pod.Namespace, pod.Name, args.bucketName)
	}

	// Check if the sidecar container is still required,
	// if not, put an exit file to the emptyDir path to
	// notify the sidecar container to exit.
	if !isInitContainer {
		if err := putExitFile(pod, targetPath); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	// Only pass mountOptions flags for defaulting if sidecar container is managed and satisifies min version requirement
	if gcsFuseSidecarImage != "" && s.driver.isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, MachineTypeAutoConfigSidecarMinVersion) {
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
				klog.V(4).Infof("NodePublishVolume on volume %q to target path %q is not needed because the gcsfuse has terminated.", args.bucketName, targetPath)

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

		// Restart monitoring goroutine if the driver restarts for existing mounts.
		s.startGcsFuseKernelParamsMonitoring(targetPath, gcsFuseSidecarImage, vs)

		klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q, mount already exists.", args.bucketName, targetPath)

		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Unlike other features, we'll assume multi NIC can be used unless we know for certain we have a version mismatch.
	canUseMultiNIC := !isManagedSidecarImage(gcsFuseSidecarImage) || s.driver.isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, MultiNICMinVersion)
	if err := s.setupMultiNIC(&args, pod, canUseMultiNIC); err != nil {
		return nil, err
	}

	klog.V(4).Infof("NodePublishVolume attempting mkdir for path %q", targetPath)
	if err := os.MkdirAll(targetPath, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir failed for path %q: %v", targetPath, err)
	}

	if profilesEnabled && profile != nil {
		// Merge the recommended mount options with user's mount options if the profiles feature
		// is enabled. This operation respects the user's mount options if they are duplicated.
		args.fuseMountOptions, err = profile.MergeRecommendedMountOptionsOnMissingKeys(args.fuseMountOptions)
		if err != nil {
			return nil, fmt.Errorf("failed to recommend mount options: %w", err)
		}
	}

	// Pass kernel params file flag to GCSFuse iff GCSFuse Kernel Params feature is supported.
	if s.isGcsFuseKernelParamsFeatureSupported(gcsFuseSidecarImage, vs) {
		// Note: enable-gcsfuse-kernel-params *must* be delimeted with an "=" sign, since it's an internal CSI flag.
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.EnableGCSFuseKernelParams + "=true"})
	}

	disallowedFlags := s.driver.generateDisallowedFlagsMap(gcsFuseSidecarImage)
	args.fuseMountOptions = removeDisallowedMountOptions(args.fuseMountOptions, disallowedFlags)
	// Start to mount
	if err = s.mounter.Mount(args.bucketName, targetPath, FuseMountType, args.fuseMountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount volume %q to target path %q: %v", args.bucketName, targetPath, err)
	}

	// Start monitoring goroutine for new mounts.
	s.startGcsFuseKernelParamsMonitoring(targetPath, gcsFuseSidecarImage, vs)
	klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q", args.bucketName, targetPath)

	return &csi.NodePublishVolumeResponse{}, nil
}

// startGcsFuseKernelParamsMonitoring starts a single long lived goroutine to monitor the
// GCSFuse kernel parameters file per target path if the feature is enabled and supported.
// It uses sync.Once to ensure the monitor is started only once. This monitoring goroutine is
// stopped in the NodeUnpublishVolume operation.
func (s *nodeServer) startGcsFuseKernelParamsMonitoring(targetPath, gcsFuseSidecarImage string, vs *util.VolumeState) {
	if s.isGcsFuseKernelParamsFeatureSupported(gcsFuseSidecarImage, vs) {
		vs.GCSFuseKernelMonitorState.StartKernelParamsFileMonitorOnce.Do(func() {
			ctx, cancel := context.WithCancel(context.Background())
			vs.GCSFuseKernelMonitorState.CancelFunc = cancel
			// Fire kernel params monitoring goroutine.
			go util.MonitorKernelParamsFile(ctx, targetPath, GCSFuseKernelParamsFilePollInterval)
		})
	}
}

// isGcsFuseKernelParamsFeatureSupported returns true if the GCSFuse kernel parameters feature is enabled and supported by sidecar version.
// GCSFuse KernelParams Feature is not supported when volumeState is nil meaning it's Dynamic mount.
func (s *nodeServer) isGcsFuseKernelParamsFeatureSupported(gcsFuseSidecarImage string, vs *util.VolumeState) bool {
	return vs != nil &&
		s.driver.config.FeatureOptions.EnableGCSFuseKernelParams &&
		gcsFuseSidecarImage != "" &&
		s.driver.isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, GCSFuseKernelParamsMinVersion)
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

	// Stop GCSFuse Kernel Params monitoring.
	if vs, ok := s.volumeStateStore.Load(targetPath); ok {
		if vs.GCSFuseKernelMonitorState.CancelFunc != nil {
			vs.GCSFuseKernelMonitorState.CancelFunc()
		}
		s.volumeStateStore.Delete(targetPath)
	}

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
				mounted, isMountedErr := s.isDirMounted(targetPath)
				if mounted || isMountedErr != nil {
					// If it's still mounted OR we couldn't check, return the original error
					return nil, status.Errorf(codes.Internal, "failed to force unmount target path %q: %v", targetPath, err)
				}
				// The mount is gone, so we treat the unmountErr as a false positive (warning only).
				klog.Warningf("failed to force unmount target path (%q) because mount is already gone. Proceeding.", targetPath)
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

// setupMultiNIC updates args with options for multi NIC configuration.
func (s *nodeServer) setupMultiNIC(args *requestArgs, pod *corev1.Pod, sidecarSupport bool) error {
	if args.multiNICIndex < 0 {
		klog.V(4).Infof("No multi NIC to configure")
		return nil
	}

	if !sidecarSupport {
		// Error rather than silently ignore to avoid difficult performance regressions.
		s.driver.RecordEventf(pod, corev1.EventTypeWarning, "MultiNICError", "multi NIC not supported by gcs fuse sidecar version")
		return fmt.Errorf("multi NIC request, but not supported by gcs fuse sidecar version")
	}

	device, message, err := GetDeviceForNumaNode(s.nwMgr, args.multiNICIndex)
	if err != nil {
		s.driver.RecordEventf(pod, corev1.EventTypeWarning, "MultiNICIgnored", "No device found for numa index %d, ignoring multi-NIC (%s): %v", args.multiNICIndex, message, err)
		klog.Errorf("No device found for numa index %d, ignoring: %s %v", args.multiNICIndex, message, err)
		return nil // just ignore rather than block mount
	}
	klog.V(4).Infof("Multi NIC %d chose %s: %s", args.multiNICIndex, device, message)
	source, err := AddSourceRouteForDevice(s.nwMgr, device)
	if err != nil {
		s.driver.RecordEventf(pod, corev1.EventTypeWarning, "MultiNICIgnored", "Not able to add source route, ignoring multi-NIC: %v", err)
		klog.Errorf("Not able to add source route. Will ignore multi-nic configuration: %v", err)
		return nil
	}

	args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{
		fmt.Sprintf("%s=%s", LocalSocketAddressArg, source),
		fmt.Sprintf("%s=%d", util.GCSFuseNumaNodeArg, args.multiNICIndex),
	})

	return nil
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
			sidecarVersionSupported = s.driver.isSidecarVersionSupportedForGivenFeature(container.Image, TokenServerSidecarMinVersion)

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
	for _, container := range pod.Spec.Containers {
		if container.Name == webhook.GcsFuseSidecarName {
			return container.Image
		}
	}
	return ""
}
