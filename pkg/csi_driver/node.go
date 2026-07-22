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
	"path/filepath"
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/proto/mounter"
)

const (
	UmountTimeout = time.Second * 5
	// GCSFuseKernelParamsFilePollInterval is the interval at which the GCSFuse kernel
	// parameters file is polled and any changes to kernel parameter files are applied.
	GCSFuseKernelParamsFilePollInterval = time.Second * 5
	FuseMountType                       = "fuse"
	maxGCSFuseVolumesForMetrics         = 10
	unmountRetryInterval                = 100 * time.Millisecond
	unmountRetryBackoffFactor           = 2.0
	unmountRetryJitter                  = 0.1
	// Standard unmount has no built-in timeout, so we use a short timeout to fail fast.
	standardUnmountRetryTimeout = 2 * time.Second
	standardUnmountRetrySteps   = 5
	// Force unmount attempts include an initial non-force unmount with a
	// 5 second timeout (as defined in vendor/k8s.io/mount-utils/mount_linux.go).
	// We then retry force unmounting for 2 seconds.
	// Thus the full timeout is 7 seconds.
	forceUnmountRetryTimeout = 7 * time.Second
	forceUnmountRetrySteps   = 6
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

func (s *nodeServer) NodePublishVolumeForSharedMount(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	// Validate arguments
	stagingPath := req.GetStagingTargetPath()
	if len(stagingPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Staging Target Path must be provided")
	}
	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Target Path must be provided")
	}
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Volume ID must be provided")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Volume capability must be provided")
	}
	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	publishContext := req.GetPublishContext()
	if publishContext == nil {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Publish Context must be provided")
	}
	mounterPodNamespace := publishContext[PublishContextKeyMounterPodNamespace]
	if len(mounterPodNamespace) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Mounter Pod Namespace must be provided")
	}
	mounterPodName := publishContext[PublishContextKeyMounterPodName]
	if len(mounterPodName) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Mounter Pod Name must be provided")
	}

	// Acquire a lock on the target path instead of volumeID, since we do not want to serialize multiple node publish calls on the same volume.
	if acquired := s.volumeLocks.TryAcquire(targetPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, targetPath)
	}
	defer s.volumeLocks.Release(targetPath)

	// Verify mounter pod exists.
	mounterPod, err := s.k8sClients.GetPod(mounterPodNamespace, mounterPodName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.FailedPrecondition, "failed to get mounter pod: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get mounter pod: %v", err)
	}
	if mounterPod == nil {
		return nil, status.Errorf(codes.Internal, "mounter pod %s/%s cannot be nil", mounterPodNamespace, mounterPodName)
	}

	// Surface any GCSFuse error to the user.
	if s.driver.config.FeatureOptions == nil || s.driver.config.FeatureOptions.SharedMountOptions == nil {
		return nil, status.Error(codes.Internal, "shared mount options can't be nil")
	}
	config := s.driver.config.FeatureOptions.SharedMountOptions
	if config.EmptyDirBasePath == nil {
		return nil, status.Error(codes.Internal, "empty dir base path function can't be nil")
	}
	emptyDirPath := config.EmptyDirBasePath(string(mounterPod.UID))
	code, err := checkMounterPodErrorFile(emptyDirPath)
	if err != nil {
		return nil, status.Error(code, err.Error())
	}

	// NodePublish is guaranteed to be called after NodeStage has waited for mounter pod to become running.
	// Mounter pod not running at this stage means that the pod started and failed (e.g. killed by Kubelet
	// during OOMKILL).
	if mounterPod.Status.Phase != corev1.PodRunning {
		return nil, status.Errorf(codes.Internal, "mounter pod %s/%s was found with an unexpected status: %+v", mounterPodNamespace, mounterPodName, mounterPod.Status)
	}

	// Verify staging path mounted.
	mounted, err := s.isDirMounted(stagingPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if path %q is already mounted: %v", stagingPath, err)
	}
	if !mounted {
		return nil, status.Errorf(codes.Internal, "staging path %s was not found mounted", stagingPath)
	}

	// Succeed if the target path was already mounted.
	targetPathMounted, err := s.isDirMounted(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if path %q is already mounted: %v", targetPath, err)
	}
	if targetPathMounted {
		klog.Infof("NodePublishVolume succeeded on staging path %q to target path %q, mount already exists.", stagingPath, targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Create target path dir.
	klog.Infof("NodePublishVolume attempting mkdir for target path %q", targetPath)
	if err := os.MkdirAll(targetPath, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir failed for path %q: %v", targetPath, err)
	}

	// Prepare mount options specific to the bind mount.
	bindMountOptions := []string{"bind"}
	if req.GetReadonly() {
		bindMountOptions = append(bindMountOptions, "ro")
	}

	// Bind mount staging path to target path.
	klog.Infof("NodePublishVolume attempting to bind mount staging path %q to target path %q", stagingPath, targetPath)
	if err = s.mounter.MountSensitiveWithoutSystemd(stagingPath, targetPath, "", bindMountOptions, nil); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to bind mount staging path %q to target path %q: %v", stagingPath, targetPath, err)
	}

	return &csi.NodePublishVolumeResponse{}, nil
}
func (s *nodeServer) populateDriverFlagsForDefaulting(node *corev1.Node, mounterImage string, emptyDirBasePath string) error {
	if node == nil {
		return fmt.Errorf("node cannot be nil")
	}
	if !s.driver.isSidecarVersionSupportedForGivenFeature(mounterImage, MachineTypeAutoConfigSidecarMinVersion) {
		return nil
	}

	shouldDisableAutoConfig := s.driver.config.DisableAutoconfig
	machineType, ok := node.Labels[clientset.MachineTypeKey]
	if !ok {
		klog.Warningf("Unable to fetch target node %v's machine type", node.Name)
		return nil
	}

	flagMap := map[string]string{
		"machine-type":       machineType,
		"disable-autoconfig": strconv.FormatBool(shouldDisableAutoConfig),
	}

	return writeDriverFlagsFile(flagMap, emptyDirBasePath)
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

	// Switch to shared mount logic if the volume context indicates that shared mount is enabled.
	vc := req.GetVolumeContext()
	if s.driver.sharedMount(vc) {
		return s.NodePublishVolumeForSharedMount(ctx, req)
	}

	// Get the Pod object.
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
			ContainerName:       webhook.GcsFuseSidecarName,
			Clientset:           s.k8sClients,
			VolumeAttributeKeys: transformKeysToSet(volumeAttributesToMountOptionsMapping),
			NodeName:            s.driver.config.NodeID,
			IsInitContainer:     isInitContainer,
			Pod:                 pod,
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
	args, err := parseRequestArguments(req.GetVolumeId(), req.GetReadonly(), req.GetVolumeCapability(), vc)
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
	// We initialize volumeState if bucket name is NOT "_", I.e. It's not a dynamic mount.
	var vs *util.VolumeState
	if args.bucketName != "_" {
		var ok bool
		if vs, ok = s.volumeStateStore.Load(targetPath); !ok {
			vs = &util.VolumeState{}
			s.volumeStateStore.Store(targetPath, vs)
		}
	}
	// volumeState is safe to access if its not nil for remaining of function since volumeLock prevents
	// Node Publish/Unpublish Volume calls from running more than once at a time per volume.

	// Check if the given Service Account has the access to the GCS bucket, and the bucket exists.
	// skip check if it has ever succeeded
	storageEndpoint := storageEndpointFromUniverseDomain(s.driver.config.UniverseDomain)
	if vs != nil && !args.skipCSIBucketAccessCheck {
		if !vs.BucketAccessCheckPassed {
			storageService, err := s.prepareStorageService(ctx, vc, storageEndpoint)
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

	s.populateTokenAndBucketAccessCheckOptions(pod, gcsFuseSidecarImage, vc, &args, vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyServiceAccountName])
	args.fuseMountOptions = s.appendCloudProfilerOptions(gcsFuseSidecarImage, args.enableCloudProfilerForSidecar, vc[VolumeContextKeyPodName], string(pod.UID), args.fuseMountOptions)
	args.fuseMountOptions = s.appendAutoGoMemLimitOptions(gcsFuseSidecarImage, args.fuseMountOptions)

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
		gcsFuseVolumeCount, err := s.countGcsFuseVolumes(pod)

		if err != nil {
			klog.Errorf("Metrics collection is disabled for Pod %s/%s as counting the number of GCS FUSE volumes failed with error: %v", pod.Namespace, pod.Name, err)
		} else if gcsFuseVolumeCount > maxGCSFuseVolumesForMetrics {
			klog.Warningf("Metrics collection is disabled for Pod %s/%s as the number of GCS FUSE volumes is %d, which is greater than the limit of %d.", pod.Namespace, pod.Name, gcsFuseVolumeCount, maxGCSFuseVolumesForMetrics)
		} else {
			klog.V(4).Infof("NodePublishVolume enabling metrics collector for target path %q", targetPath)
			s.driver.config.MetricsManager.RegisterMetricsCollector(targetPath, pod.Namespace, pod.Name, args.bucketName, s.driver.config.NodeID)
		}
	}

	// Check if the sidecar container is still required,
	// if not, put an exit file to the emptyDir path to
	// notify the sidecar container to exit.
	if !isInitContainer {
		if err := putExitFile(pod, targetPath); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	emptyDirBasePath, err := util.PrepareEmptyDir(targetPath, true)
	if err != nil {
		klog.Warningf("Failed to prepare empty dir from target path %q: %v", targetPath, err)
	}

	var podUID, volumeName string
	if s.isGcsFuseKernelParamsFeatureSupported(gcsFuseSidecarImage, vs) {
		var err error
		if podUID, volumeName, err = util.ParsePodIDVolumeFromTargetpath(targetPath); err != nil {
			klog.Warningf("Failed to parse pod ID and volume name from target path %q for kernel params monitor: %v. Monitor will not be started.", targetPath, err)
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
		if emptyDirBasePath != "" {
			s.startGcsFuseKernelParamsMonitoring(targetPath, emptyDirBasePath, podUID, volumeName, gcsFuseSidecarImage, vs)
		}

		klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q, mount already exists.", args.bucketName, targetPath)

		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Only pass mountOptions flags for defaulting if sidecar container is managed and satisfies min version requirement
	if emptyDirBasePath != "" {
		if err := s.populateDriverFlagsForDefaulting(node, gcsFuseSidecarImage, filepath.Dir(emptyDirBasePath)); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
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

	// Pass the universe-aware storage endpoint if the sidecar version supports it.
	if s.driver.isSidecarVersionSupportedForGivenFeature(gcsFuseSidecarImage, StorageEndpointInternalMinVersion) {
		args.fuseMountOptions = overrideStorageEndpointInternal(args.fuseMountOptions, storageEndpoint)
	}

	// Start to mount
	if err = s.mounter.Mount(args.bucketName, targetPath, FuseMountType, args.fuseMountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount volume %q to target path %q: %v", args.bucketName, targetPath, err)
	}

	// Start monitoring goroutine for new mounts.
	if emptyDirBasePath != "" {
		s.startGcsFuseKernelParamsMonitoring(targetPath, emptyDirBasePath, podUID, volumeName, gcsFuseSidecarImage, vs)
	}
	klog.V(4).Infof("NodePublishVolume succeeded on volume %q to target path %q", args.bucketName, targetPath)

	return &csi.NodePublishVolumeResponse{}, nil
}

// startGcsFuseKernelParamsMonitoring starts a single long lived goroutine to monitor the
// GCSFuse kernel parameters file per mount point if the feature is enabled and supported.
// It uses sync.Once to ensure the monitor is started only once.
func (s *nodeServer) startGcsFuseKernelParamsMonitoring(mountPoint, emptyDirBasePath, podUID, volumeName, gcsFuseContainerImage string, vs *util.VolumeState) {
	if s.isGcsFuseKernelParamsFeatureSupported(gcsFuseContainerImage, vs) {
		vs.GCSFuseKernelMonitorState.StartKernelParamsFileMonitorOnce.Do(func() {
			ctx, cancel := context.WithCancel(context.Background())
			vs.GCSFuseKernelMonitorState.CancelFunc = cancel
			logPrefix := fmt.Sprintf("Kernel Params Monitor[Pod %v, Volume %v]", podUID, volumeName)

			// Fire kernel params monitoring goroutine.
			go util.MonitorKernelParamsFile(ctx, mountPoint, emptyDirBasePath, logPrefix, GCSFuseKernelParamsFilePollInterval)
		})
	}
}

// isGcsFuseKernelParamsFeatureSupported returns true if the GCSFuse kernel parameters feature is enabled and supported by sidecar/mounter version.
// GCSFuse Kernel Params feature is not applicable for dynamic mounts as a single kernel parameter setting doesn’t apply for different type
// of buckets that a dynamic mount serves. This feature is only applicable for non-dynamic mounts i.e when volumeState(vs) is not nil.
func (s *nodeServer) isGcsFuseKernelParamsFeatureSupported(gcsFuseContainerImage string, vs *util.VolumeState) bool {
	return vs != nil &&
		s.driver.config.FeatureOptions != nil &&
		s.driver.config.FeatureOptions.EnableGCSFuseKernelParams &&
		s.driver.isSidecarVersionSupportedForGivenFeature(gcsFuseContainerImage, GCSFuseKernelParamsMinVersion)
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

	// Unregister metrics collecter.
	// It is idempotent to unregister the same collector.
	if s.driver.config.MetricsManager != nil {
		s.driver.config.MetricsManager.UnregisterMetricsCollector(targetPath, s.driver.config.NodeID)
	}

	// Stop GCSFuse Kernel Params monitoring.
	if vs, ok := s.volumeStateStore.Load(targetPath); ok && vs != nil {
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
			if err = s.unmountWithRetry(ctx, targetPath, forceUnmountRetryTimeout, forceUnmountRetrySteps, func() error {
				return forceUnmounter.UnmountWithForce(targetPath, UmountTimeout)
			}); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to force unmount target path %q: %v", targetPath, err)
			}
		} else {
			klog.Warningf("failed to cast the mounter to a forceUnmounter, proceed with the default mounter Unmount")
			if err = s.unmountWithRetry(ctx, targetPath, standardUnmountRetryTimeout, standardUnmountRetrySteps, func() error {
				return s.mounter.Unmount(targetPath)
			}); err != nil {
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

// unmountWithRetry retries the unmount operation using exponential backoff.
// It uses a child context with a timeout to limit the total retry duration,
// in addition to being limited to the defined number of steps.
//
// If the OS unmount system call itself hangs, this function will block
// on the hung call. It won't be able to interrupt the hung call until the Kubelet cancels the parent
// context (NodeUnpublishVolume context) and retries the entire operation.
func (s *nodeServer) unmountWithRetry(ctx context.Context, targetPath string, timeout time.Duration, steps int, unmountFn func() error) error {
	var lastErr error
	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	backoff := wait.Backoff{
		Duration: unmountRetryInterval,
		Factor:   unmountRetryBackoffFactor,
		Jitter:   unmountRetryJitter,
		Steps:    steps,
	}

	pollErr := wait.ExponentialBackoffWithContext(childCtx, backoff, func(ctx context.Context) (bool, error) {
		lastErr = unmountFn()
		if lastErr == nil {
			return true, nil
		}
		klog.Warningf("Failed to unmount %q: %v. Retrying...", targetPath, lastErr)
		return false, nil
	})

	if pollErr != nil {
		if lastErr != nil {
			return lastErr
		}
		return pollErr
	}
	return nil
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
func (s *nodeServer) prepareStorageService(ctx context.Context, vc map[string]string, customEndpoint string) (storage.Service, error) {
	ts := s.driver.config.TokenManager.GetTokenSourceFromK8sServiceAccount(vc[VolumeContextKeyPodNamespace], vc[VolumeContextKeyServiceAccountName], vc[VolumeContextKeyServiceAccountToken], "" /*audience*/, false)
	storageService, err := s.storageServiceManager.SetupService(ctx, ts, customEndpoint)
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
	for _, container := range pod.Spec.Containers {
		if strings.HasPrefix(container.Name, util.MounterPodNamePrefix) {
			sidecarVersionSupported = s.driver.isSidecarVersionSupportedForGivenFeature(container.Image, TokenServerSidecarMinVersion)

			break
		}
	}

	return tokenVolumeInjected && (sidecarVersionSupported || userInput)
}

// populateTokenAndBucketAccessCheckOptions populates fuseMountOptions with token server identity provider
// for host network workloads and bucket access check parameters for both sidecar and shared mount modes.
func (s *nodeServer) populateTokenAndBucketAccessCheckOptions(pod *corev1.Pod, image string, vc map[string]string, args *requestArgs, podNamespace, serviceAccountName string) {
	sidecarCheckVal := getInternalMountOptionValue(args.fuseMountOptions, util.EnableSidecarBucketAccessCheckConst)
	userExplicitlyEnabled := sidecarCheckVal == util.TrueStr
	userExplicitlyDisabled := sidecarCheckVal == util.FalseStr
	enableBucketAccessCheckForVersion := s.driver.isSidecarVersionSupportedForGivenFeature(image, SidecarBucketAccessCheckMinVersion) &&
		(userExplicitlyEnabled || (s.driver.config.EnableSidecarBucketAccessCheck && !userExplicitlyDisabled))
	identityProvider := ""
	userSpecifiedIDP := args.userSpecifiedIdentityProvider
	if userSpecifiedIDP == "" {
		userSpecifiedIDP = vc[util.TokenServerIdentityProviderConst]
	}
	if s.shouldPopulateIdentityProvider(pod, args.optInHostnetworkKSA, userSpecifiedIDP != "") {
		if userSpecifiedIDP != "" {
			identityProvider = userSpecifiedIDP
		} else {
			identityProvider = s.driver.config.TokenManager.GetIdentityProvider()
		}
		klog.V(6).Infof("Populating identity provider %q in mount options", identityProvider)
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.OptInHnw + "=true", util.TokenServerIdentityProviderConst + "=" + identityProvider})
	} else if enableBucketAccessCheckForVersion && !userExplicitlyEnabled {
		// Enable sidecar bucket access check only for Workload Identity workloads. This feature consumes additional quota for Host Network pods as we do not have token caching.
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.EnableSidecarBucketAccessCheckConst + "=true"})
	}

	if enableBucketAccessCheckForVersion {
		if identityProvider == "" {
			identityProvider = s.driver.config.TokenManager.GetIdentityProvider()
			args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.TokenServerIdentityProviderConst + "=" + identityProvider})
		}
		klog.Infof("Got identity provider %s", identityProvider)

		identityPool := s.driver.config.TokenManager.GetIdentityPool()
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{
			util.PodNamespaceConst + "=" + podNamespace,
			util.ServiceAccountNameConst + "=" + serviceAccountName,
			util.TokenServerIdentityPoolConst + "=" + identityPool})
	}
}

// appendCloudProfilerOptions evaluates and appends enable-cloud-profiler-for-sidecar, pod-name, and pod-uid
// mount options if enabled in driver options and supported by the container image.
func (s *nodeServer) appendCloudProfilerOptions(mounterImage string, enableCloudProfiler bool, podName string, podUID string, mountOptions []string) []string {
	if !enableCloudProfiler || !s.driver.isSidecarVersionSupportedForGivenFeature(mounterImage, SidecarCloudProfilerMinVersion) {
		return mountOptions
	}
	if podName == "" {
		klog.Warning("Pod name is empty, skipping cloud profiler mount options")
		return mountOptions
	}
	if podUID == "" {
		klog.Warning("Pod UID is empty, skipping cloud profiler mount options")
		return mountOptions
	}
	return joinMountOptions(mountOptions, []string{
		util.EnableCloudProfilerForSidecarConst + "=true",
		util.PodNameConst + "=" + podName,
		util.PodUIDConst + "=" + podUID,
	})
}

// appendAutoGoMemLimitOptions evaluates and appends enable-auto-gomemlimit and auto-gomemlimit-ratio
// mount options if supported by the container image and enabled in driver options, unless explicitly overridden.
func (s *nodeServer) appendAutoGoMemLimitOptions(mounterImage string, mountOptions []string) []string {
	// Check if the user explicitly provided GOMEMLIMIT flags in their
	// volume's mountOptions.
	userProvidedAutoMemLimit := false
	userProvidedRatio := false
	for _, opt := range mountOptions {
		if opt == util.EnableAutoGoMemLimitConst || strings.HasPrefix(opt, util.EnableAutoGoMemLimitConst+"=") {
			userProvidedAutoMemLimit = true
		}
		if strings.HasPrefix(opt, util.AutoGoMemLimitRatioConst+"=") {
			userProvidedRatio = true
		}
	}

	// Inject driver defaults if the sidecar supports the GOMEMLIMIT feature.
	// We evaluate the ratio independently so users who manually opt-in via
	// mountOptions without specifying a ratio still receive the driver's
	// default.
	if s.driver.isSidecarVersionSupportedForGivenFeature(mounterImage, SidecarAutoGoMemLimitMinVersion) {
		var extraOpts []string
		enableAutoGoMemLimit := s.driver.config.FeatureOptions != nil && s.driver.config.FeatureOptions.GoMemLimitOptions != nil && s.driver.config.FeatureOptions.GoMemLimitOptions.EnableAutoGoMemLimit
		if enableAutoGoMemLimit && !userProvidedAutoMemLimit {
			extraOpts = append(extraOpts, util.EnableAutoGoMemLimitConst+"=true")
		}
		// We check this to prevent injecting the ratio and adding unnecessary
		// noise to the mount options when the feature is completely disabled.
		sidecarFeatureWillBeEnabled := enableAutoGoMemLimit || userProvidedAutoMemLimit
		if !userProvidedRatio && sidecarFeatureWillBeEnabled && s.driver.config.FeatureOptions != nil && s.driver.config.FeatureOptions.GoMemLimitOptions != nil {
			autoGoMemLimitRatio := s.driver.config.FeatureOptions.GoMemLimitOptions.AutoGoMemLimitRatio
			extraOpts = append(extraOpts, util.AutoGoMemLimitRatioConst+"="+strconv.FormatFloat(autoGoMemLimitRatio, 'f', -1, 64))
		}

		if len(extraOpts) > 0 {
			return joinMountOptions(mountOptions, extraOpts)
		}
	}
	return mountOptions
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

// countGcsFuseVolumes returns the number of GCSFuse CSI Ephemeral volumes or
// PersistentVolumeClaims in the given Pod spec. We intentionally do not
// check to see if the PVC is a GCSFuseCSI PVC because that requires additional
// API calls or a PVC informer which previously made the node driver OOM.
// We may end up counting some non-GCSFuse volumes, but that is acceptable
// since this is only used to determine whether to enable metrics collection
// on a given pod.
// TODO(amacaskill): Make sure the PVC is a GCSFuseCSI PVC, without
// causing an OOM in the node driver at scale.
func (s *nodeServer) countGcsFuseVolumes(pod *corev1.Pod) (int, error) {
	gcsFuseVolumeCount := 0

	if pod.Spec.Volumes == nil {
		return gcsFuseVolumeCount, nil
	}

	for _, v := range pod.Spec.Volumes {
		// Count ephemeral gcsfuse volumes
		if v.CSI != nil && v.CSI.Driver == s.driver.config.Name {
			gcsFuseVolumeCount++
			continue
		}

		if v.PersistentVolumeClaim != nil {
			gcsFuseVolumeCount++
			continue
		}
	}

	return gcsFuseVolumeCount, nil
}

func (s *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	// Validate arguments.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "NodeStageVolume Request cannot be nil")
	}
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeStageVolume Volume ID must be provided")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "NodeStageVolume Volume capability must be provided")
	}
	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	stagingPath := req.GetStagingTargetPath()
	if len(stagingPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeStageVolume Staging Target Path must be provided")
	}
	// Skip NodeStageVolume for sidecar mounted volumes.
	if !s.driver.sharedMount(req.GetVolumeContext()) {
		return &csi.NodeStageVolumeResponse{}, nil
	}

	publishContext := req.GetPublishContext()
	if publishContext == nil {
		return nil, status.Error(codes.InvalidArgument, "publishContext must be provided")
	}

	podName, ok := publishContext[PublishContextKeyMounterPodName]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "publishContext must contain mounter pod name")
	}
	if podName == "" {
		return nil, status.Error(codes.InvalidArgument, "mounter pod name in publishContext cannot be empty")
	}

	podNamespace, ok := publishContext[PublishContextKeyMounterPodNamespace]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "publishContext must contain mounter pod namespace")
	}
	if podNamespace == "" {
		return nil, status.Error(codes.InvalidArgument, "mounter pod namespace in publishContext cannot be empty")
	}

	// Acquire a lock on the staging path.
	if acquired := s.volumeLocks.TryAcquire(stagingPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, stagingPath)
	}
	defer s.volumeLocks.Release(stagingPath)

	resp, err := s.executeNodeStageVolume(ctx, req)

	if err != nil {
		klog.Errorf("NodeStageVolume failed on staging path %q for volume %q: %v)", stagingPath, volumeID, err)
	}

	return resp, err
}

func (s *nodeServer) executeNodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	if s.driver.config.FeatureOptions == nil ||
		s.driver.config.FeatureOptions.SharedMountOptions == nil ||
		s.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath == nil {
		return nil, status.Errorf(codes.Internal, "shared mount options are not fully configured")
	}

	clientset := s.driver.config.K8sClients
	stagingPath := req.GetStagingTargetPath()

	// Validate staging path and mounter pod information from the request.
	publishContext := req.GetPublishContext()
	podName := publishContext[PublishContextKeyMounterPodName]
	podNamespace := publishContext[PublishContextKeyMounterPodNamespace]

	klog.Infof("Executing NodeStageVolume. Mounter pod: %s/%s, node: %q, volume: %q, staging path: %q", podNamespace, podName, s.driver.config.NodeID, req.GetVolumeId(), stagingPath)

	// Verify mounter pod exists.
	pod, err := clientset.GetPod(podNamespace, podName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.FailedPrecondition, "mounter pod %s/%s expected to exist but was not found", podNamespace, podName)
		}
		return nil, status.Errorf(codes.Internal, "failed to get mounter pod %s/%s: %v", podNamespace, podName, err)
	}
	if pod == nil {
		return nil, status.Errorf(codes.Internal, "mounter pod %s/%s can't be nil", podNamespace, podName)
	}

	volumeID := req.GetVolumeId()
	vc := req.GetVolumeContext()
	pvName := vc[util.VolumeContextKeyPVName]
	if pvName == "" {
		return nil, status.Errorf(codes.Internal, "PV name not found in VolumeContext (key %q)", util.VolumeContextKeyPVName)
	}

	// Build profile config and merge recommended mount options if storage profiles are enabled.
	profilesEnabled := s.driver.config.FeatureOptions.FeatureGCSFuseProfiles.Enabled
	var profile *profiles.ProfileConfig
	if profilesEnabled {
		klog.V(4).Infof("NodeStageVolume gcsfuse profiles feature is enabled for mounter pod %s/%s", podNamespace, podName)
		profile, err = profiles.BuildProfileConfig(&profiles.BuildProfileConfigParams{
			VolumeName:          pvName,
			Clientset:           s.k8sClients,
			ContainerName:       util.MounterPodNamePrefix,
			VolumeAttributeKeys: transformKeysToSet(volumeAttributesToMountOptionsMapping),
			NodeName:            s.driver.config.NodeID,
			Pod:                 pod,
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
	args, err := parseRequestArguments(volumeID, false /* readOnly */, req.GetVolumeCapability(), vc)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse request arguments: %v", err)
	}

	if profile != nil {
		args.fuseMountOptions, err = profile.MergeRecommendedMountOptionsOnMissingKeys(args.fuseMountOptions)
		if err != nil {
			return nil, fmt.Errorf("failed to recommend mount options: %w", err)
		}
	}

	podImage, err := mounterPodImage(pod)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get image of mounter pod %s/%s: %v", podNamespace, podName, err)
	}

	s.populateTokenAndBucketAccessCheckOptions(pod, podImage, vc, &args, podNamespace, pod.Spec.ServiceAccountName)
	args.fuseMountOptions = s.appendCloudProfilerOptions(podImage, args.enableCloudProfilerForSidecar, podName, string(pod.UID), args.fuseMountOptions)
	args.fuseMountOptions = s.appendAutoGoMemLimitOptions(podImage, args.fuseMountOptions)

	// We initialize volumeState if bucket name is NOT "_", I.e. It's not a dynamic mount.
	var vs *util.VolumeState
	if args.bucketName != "_" {
		var ok bool
		if vs, ok = s.volumeStateStore.Load(stagingPath); !ok {
			vs = &util.VolumeState{}
			s.volumeStateStore.Store(stagingPath, vs)
		}
	}

	podUID := string(pod.UID)
	emptyDirBasePath := s.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath(podUID)

	// Check if the staging path is already mounted.
	mounted, err := s.isDirMounted(stagingPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if path %q is already mounted: %v", stagingPath, err)
	}
	if mounted {
		// Restart monitoring goroutine if the driver restarts for existing mounts.
		s.startGcsFuseKernelParamsMonitoring(stagingPath, emptyDirBasePath, podUID, pvName, podImage, vs)

		klog.Infof("NodeStageVolume succeeded on staging path %q for volume %q, mount already exists.", stagingPath, req.GetVolumeId())
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Pass kernel params file flag to GCSFuse iff GCSFuse Kernel Params feature is supported.
	if s.isGcsFuseKernelParamsFeatureSupported(podImage, vs) {
		args.fuseMountOptions = joinMountOptions(args.fuseMountOptions, []string{util.EnableGCSFuseKernelParams + "=true"})
	}

	// Make the staging path.
	klog.Infof("NodeStageVolume attempting mkdir for staging path %q", stagingPath)
	if err := os.MkdirAll(stagingPath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir failed for path %q: %v", stagingPath, err)
	}

	// Fetch the node to pass its labels for auto-config
	node, err := s.k8sClients.GetNode(s.driver.config.NodeID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if node == nil {
		return nil, status.Errorf(codes.Internal, "Node %q not found", s.driver.config.NodeID)
	}

	// Only pass mountOptions flags for defaulting if sidecar container is managed and satisfies min version requirement
	if err := s.populateDriverFlagsForDefaulting(node, podImage, emptyDirBasePath); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Wait for the mounter pod grpc server to be ready.
	if err := waitForMounterServer(ctx, clientset, podNamespace, podName, podUID, s.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath); err != nil {
		return nil, err
	}

	// Send GRPC to mounter pod to start GCSFuse.
	if err := s.mountToNode(ctx, podUID, stagingPath, volumeID, args.fuseMountOptions); err != nil {
		klog.Errorf("Failed to mount volume %q to staging path %q: %v", volumeID, stagingPath, err)
		if code, err := checkMounterPodErrorFile(emptyDirBasePath); err != nil {
			return nil, status.Error(code, err.Error())
		}
		return nil, err
	}

	// Start monitoring goroutine for new shared mounts.
	s.startGcsFuseKernelParamsMonitoring(stagingPath, emptyDirBasePath, podUID, pvName, podImage, vs)

	klog.Infof("Mounter pod %s/%s is running and staging path %s is mounted", podNamespace, podName, stagingPath)

	klog.Infof("NodeStageVolume succeeded on staging path %q for volume %q", stagingPath, req.GetVolumeId())
	return &csi.NodeStageVolumeResponse{}, nil
}

// mountToNode connects to the mounter server, at which point it initializes the GCSFuse process.
func (s *nodeServer) mountToNode(ctx context.Context, podUID, stagingPath, volumeID string, mountOptions []string) error {
	if s.driver.config.FeatureOptions == nil || s.driver.config.FeatureOptions.SharedMountOptions == nil {
		return status.Errorf(codes.Internal, "shared mount options are not fully configured")
	}

	if s.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath == nil {
		return status.Errorf(codes.Internal, "empty dir base path must be provided for shared mount")
	}
	emptyDirBasePath := s.driver.config.FeatureOptions.SharedMountOptions.EmptyDirBasePath(podUID)
	socketFile := filepath.Join(emptyDirBasePath, MounterPodSocketFile)

	// Create a symlink to bypass the 108-character limit for Unix domain sockets
	// when dialing the connection from the Node Server.
	if s.driver.config.FeatureOptions.SharedMountOptions.FuseSocketDir == "" {
		return status.Errorf(codes.Internal, "fuse socket dir must be provided for shared mount")
	}
	symlink := filepath.Join(s.driver.config.FeatureOptions.SharedMountOptions.FuseSocketDir, mounterPodSocketDir, podUID)
	if err := os.MkdirAll(filepath.Dir(symlink), 0750); err != nil {
		return status.Errorf(codes.Internal, "failed to create dir for symlink %q: %v", symlink, err)
	}

	// Clear stale files from a previous mount attempt.
	if err := util.CheckAndDeleteStaleFile(emptyDirBasePath, util.ErrorFileName); err != nil {
		return status.Errorf(codes.Internal, "failed to check and delete stale error file: %v", err)
	}
	// Clear stale symlink from a previous mount attempt. os.Remove operates on symlinks, so it will not remove the target file if the symlink is stale.
	if err := os.Remove(symlink); err != nil && !os.IsNotExist(err) {
		return status.Errorf(codes.Internal, "failed to remove stale symlink %q: %v", symlink, err)
	}

	if err := os.Symlink(socketFile, symlink); err != nil {
		return status.Errorf(codes.Internal, "failed to create symlink to %q: %v", socketFile, err)
	}
	defer os.Remove(symlink)

	// Connect to the socket using the short symlink path.
	socketPath := fmt.Sprintf("unix:%s", symlink)
	conn, err := grpc.NewClient(socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		klog.Errorf("Failed to connect to the server: %v", err)
		return status.Errorf(codes.Internal, "failed to connect to the mounter pod grpc server: %v", err)
	}
	klog.Infof("Connected to MounterServer at %s", socketPath)
	defer conn.Close()

	c := mounter.NewMounterClient(conn)
	if _, err := c.Mount(ctx, &mounter.MountRequest{
		MountPoint:   stagingPath,
		VolumeId:     volumeID,
		MountOptions: mountOptions,
	}); err != nil {
		return err
	}

	klog.Infof("Mount succeeded at staging target path %s", stagingPath)
	return nil
}

func (s *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	// Validate arguments.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "NodeUnstageVolume Request cannot be nil")
	}
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeUnstageVolume Volume ID must be provided")
	}
	stagingPath := req.GetStagingTargetPath()
	if len(stagingPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeUnstageVolume Staging Target Path must be provided")
	}

	// Acquire a lock on the staging path instead of volumeID, since we do not want to serialize multiple node unstage calls on the same volume.
	if acquired := s.volumeLocks.TryAcquire(stagingPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, stagingPath)
	}
	defer s.volumeLocks.Release(stagingPath)

	if err := s.cleanupStagingPath(stagingPath); err != nil {
		return nil, err
	}

	klog.Infof("NodeUnstageVolume succeeded on staging path %q for volume %q", stagingPath, req.GetVolumeId())
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (s *nodeServer) cleanupStagingPath(stagingPath string) error {
	// Stop GCSFuse Kernel Params monitoring.
	if vs, ok := s.volumeStateStore.Load(stagingPath); ok && vs != nil {
		if vs.GCSFuseKernelMonitorState.CancelFunc != nil {
			vs.GCSFuseKernelMonitorState.CancelFunc()
		}
		s.volumeStateStore.Delete(stagingPath)
	}

	// Unmount staging path.
	mounted, err := s.isDirMounted(stagingPath)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to check if path %q is already mounted: %v", stagingPath, err)
	}

	if mounted {
		if err = s.mounter.Unmount(stagingPath); err != nil {
			return status.Errorf(codes.Internal, "failed to unmount staging path %q: %v", stagingPath, err)
		}
	} else {
		klog.Infof("staging path %q was already unmounted", stagingPath)
	}

	// Cleanup the mount point.
	if err := mount.CleanupMountPoint(stagingPath, s.mounter, false /* bind mount */); err != nil {
		return status.Errorf(codes.Internal, "failed to cleanup the mount point %q: %v", stagingPath, err)
	}

	return nil
}
