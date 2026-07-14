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
	"context"
	"fmt"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles"
	putil "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
)

const (
	MinimumVolumeSizeInBytes int64 = 1 * util.Mb
)

// CreateVolume parameters.
const (
	// Keys for PV and PVC parameters as reported by external-provisioner.
	ParameterKeyPVCName      = "csi.storage.k8s.io/pvc/name"
	ParameterKeyPVCNamespace = "csi.storage.k8s.io/pvc/namespace"
	ParameterKeyPVName       = "csi.storage.k8s.io/pv/name"

	// User provided labels.
	ParameterKeyLabels = "labels"

	// Keys for tags to attach to the provisioned disk.
	tagKeyCreatedForClaimNamespace = "kubernetes_io_created-for_pvc_namespace"
	tagKeyCreatedForClaimName      = "kubernetes_io_created-for_pvc_name"
	tagKeyCreatedForVolumeName     = "kubernetes_io_created-for_pv_name"
	tagKeyCreatedBy                = "storage_gke_io_created-by"
)

// controllerServer handles volume provisioning.
type controllerServer struct {
	csi.UnimplementedControllerServer
	driver                *GCSDriver
	storageServiceManager storage.ServiceManager
	volumeLocks           *util.VolumeLocks
	scanner               *profiles.Scanner
	features              *GCSDriverFeatureOptions
}

func newControllerServer(driver *GCSDriver, storageServiceManager storage.ServiceManager, featureOptions *GCSDriverFeatureOptions) (csi.ControllerServer, error) {
	cs := &controllerServer{
		driver:                driver,
		storageServiceManager: storageServiceManager,
		volumeLocks:           util.NewVolumeLocks(),
		features:              featureOptions,
	}
	return cs, nil
}

func (s *controllerServer) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: s.driver.cscap,
	}, nil
}

func (s *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	// Validate arguments
	volumeID := req.GetVolumeId()
	if req.GetVolumeContext()[VolumeContextKeyEphemeral] != util.TrueStr {
		volumeID = util.ParseVolumeID(volumeID)
	}
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ValidateVolumeCapabilities volumeID must be provided")
	}
	caps := req.GetVolumeCapabilities()
	if len(caps) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ValidateVolumeCapabilities volume capabilities must be provided")
	}

	storageService, err := s.prepareStorageService(ctx, req.GetSecrets())
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "failed to prepare storage service: %v", err)
	}
	defer storageService.Close()

	// Check that the volume exists
	if exist, err := storageService.CheckBucketExists(ctx, &storage.ServiceBucket{Name: volumeID}); !exist {
		return nil, status.Errorf(storage.ParseErrCode(err), "volume %v doesn't exist: %v", volumeID, err)
	}

	// Validate that the volume matches the capabilities
	// Note that there is nothing in the bucket that we actually need to validate
	if err := s.driver.validateVolumeCapabilities(caps); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{
			Message: err.Error(),
		}, status.Error(codes.InvalidArgument, err.Error())
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeContext:      req.GetVolumeContext(),
			VolumeCapabilities: req.GetVolumeCapabilities(),
			Parameters:         req.GetParameters(),
		},
	}, nil
}

func (s *controllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	// Validate arguments
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ControllerPublishVolume Volume ID must be provided")
	}
	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	nodeID := req.GetNodeId()
	if len(nodeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ControllerPublishVolume Node ID must be provided")
	}

	// Acquires the lock for the volume on that node only, because we need to support the ability
	// to publish the same volume onto different nodes concurrently
	lockingVolumeID := fmt.Sprintf("%s/%s", nodeID, volumeID)
	if acquired := s.volumeLocks.TryAcquire(lockingVolumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, lockingVolumeID)
	}
	defer s.volumeLocks.Release(lockingVolumeID)

	// Skip ControllerPublishVolume if the volume is not using the shared mount feature.
	vc := req.GetVolumeContext()
	if !s.driver.sharedMount(vc) {
		return &csi.ControllerPublishVolumeResponse{}, nil
	}

	// Find the workload namespace where the mounter pod should be created by identifying the PVC
	// bound to this volume's PV.
	clientset := s.driver.config.K8sClients
	pvName := vc[util.VolumeContextKeyPVName]
	if pvName == "" {
		return nil, status.Errorf(codes.Internal, "PV name not found in VolumeContext (key %q)", util.VolumeContextKeyPVName)
	}
	klog.V(6).Infof("ControllerPublishVolume: directly fetching PV %q from VolumeContext for volume %q, context: %v", pvName, volumeID, vc)
	pv, err := clientset.GetPV(pvName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	if pv.Spec.ClaimRef == nil {
		// The PV should be bound if ControllerPublishVolume is called. Return error since this is unexpected.
		return nil, status.Errorf(codes.Internal, "pv %q is not bound to any pvc", pv.Name)
	}
	pvcName := pv.Spec.ClaimRef.Name
	pvcNamespace := pv.Spec.ClaimRef.Namespace
	if pvcNamespace == "" || pvcName == "" {
		return nil, status.Errorf(codes.Internal, "pv claimRef namespace and name can't be empty, namespace: %q, name: %q", pvcNamespace, pvcName)
	}
	podNamespace := pvcNamespace

	// Find the PodTemplate from the PVC to allow mounter pod config overrides.
	pvc, err := clientset.GetPVC(pvcNamespace, pvcName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	podTemplate, err := mounterPodTemplate(clientset, pvc)
	if err != nil {
		return nil, err
	}
	if podTemplate == nil {
		return nil, status.Error(codes.Internal, "pod template can't be nil")
	}

	// Extract overrides specifically from the container named "gcsfusecsi-mount".
	var containerResources *corev1.ResourceRequirements
	var containerImage string
	if s.features != nil && s.features.SharedMountOptions != nil {
		containerImage = s.features.SharedMountOptions.MounterPodImage
	}
	for i := range podTemplate.Template.Spec.Containers {
		container := &podTemplate.Template.Spec.Containers[i]
		if container.Name != util.MounterPodNamePrefix {
			continue
		}
		containerResources = &container.Resources
		if container.Image != "" && container.Image != mounterPodManagedImageKeyword {
			// If the image isn't the placeholder managed keyword, the user is trying to
			// override the mounter pod's image.
			// TODO(urielguzman): Somehow validate image so that only trustworthy repositories are allowed.
			containerImage = container.Image
		}
		break
	}
	if containerImage == "" {
		return nil, status.Error(codes.Internal, "mounter pod image cannot be empty")
	}

	var profilesEnabled bool
	if s.features != nil && s.features.FeatureGCSFuseProfiles.Enabled && pv.Spec.StorageClassName != "" {
		sc, err := clientset.GetSC(pv.Spec.StorageClassName)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.Internal, "failed to get StorageClass %q: %v", pv.Spec.StorageClassName, err)
		}
		if sc != nil {
			profilesEnabled = putil.IsProfile(sc)
		} else {
			klog.V(6).Infof("failed to determine if StorageClass %q uses profiles: not found", pv.Spec.StorageClassName)
		}
	}

	// Prepare mounter pod config.
	podName := createMounterPodName(nodeID, volumeID)
	podConfig := &mounterPodConfig{
		podName:            podName,
		namespace:          podNamespace,
		serviceAccountName: podTemplate.Template.Spec.ServiceAccountName,
		resources:          containerResources,
		nodeID:             nodeID,
		image:              containerImage,
		volumes:            podTemplate.Template.Spec.Volumes,
		profilesEnabled:    profilesEnabled,
	}

	if err := createMounterPod(clientset, ctx, podConfig); err != nil {
		return nil, err
	}

	// Wait until the mounter pod is scheduled to a node.
	// This ensures that the mounter pod can be scheduled even if there are resource constraints on the node,
	// such as needing to wait for a regular pod to be evicted first.
	// Without this check, NodeStageVolume could return an internal error because the mounter pod is not yet scheduled.
	if err := waitForMounterPodScheduled(clientset, ctx, podNamespace, podName, nodeID); err != nil {
		return nil, err
	}

	klog.Infof("ControllerPublishVolume succeeded for mounter pod %s/%s. Node: %q, volume %q", podConfig.namespace, podConfig.podName, nodeID, volumeID)
	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			// Pass the mounter pod's namespace and name to subsequent NodeStageVolume and
			// NodePublishVolume calls to avoid re-computation.
			PublishContextKeyMounterPodNamespace: podNamespace,
			PublishContextKeyMounterPodName:      podName,
		},
	}, nil
}

// TODO(urielguzman): implement ControllerUnpublishVolume.
func (s *controllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	// Validate arguments.
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ControllerUnpublishVolume Volume ID must be provided")
	}
	nodeID := req.NodeId
	if len(nodeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ControllerUnpublishVolume Node ID must be provided")
	}

	// Acquires the lock for the volume on that node only, because we need to support the ability
	// to publish the same volume onto different nodes concurrently
	lockingVolumeID := fmt.Sprintf("%s/%s", nodeID, volumeID)
	if acquired := s.volumeLocks.TryAcquire(lockingVolumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, lockingVolumeID)
	}
	defer s.volumeLocks.Release(lockingVolumeID)

	// Delete the mounter pod, if it exists.
	podName := createMounterPodName(nodeID, volumeID)
	mounterPods, err := s.driver.config.K8sClients.GetPodsByName(podName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list pods: %v", err)
	}

	if len(mounterPods) == 0 {
		klog.Infof("ControllerUnpublishVolume succeeded for volume %q on node %q", volumeID, nodeID)
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}
	if len(mounterPods) > 1 {
		// Each nodeID/volumeID pair should match exactly to 1 mounter pod. If there are more than 1 in that node,
		// this signals an internal bug on how the mounter pods are being handled by the CSI Driver.
		return nil, status.Errorf(codes.Internal, "expected 1 mounter pod for volume %q on node %q, found: %d", volumeID, nodeID, len(mounterPods))
	}

	podNamespace := mounterPods[0].Namespace
	if err := deleteMounterPod(ctx, s.driver.config.K8sClients, podNamespace, podName); err != nil {
		return nil, err
	}

	klog.Infof("ControllerUnpublishVolume succeeded for volume %q on node %q", volumeID, nodeID)
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (s *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	// Validate arguments
	name := req.GetName()
	if len(name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume name must be provided")
	}
	volumeID := strings.ToLower(name)

	if err := s.driver.validateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	capBytes, err := getRequestCapacity(req.GetCapacityRange())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	secrets := req.GetSecrets()
	projectID, ok := secrets["projectID"]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "projectID must be provided in secret")
	}

	if acquired := s.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, volumeID)
	}
	defer s.volumeLocks.Release(volumeID)

	param := req.GetParameters()
	newBucket := &storage.ServiceBucket{
		Project:                        projectID,
		Name:                           volumeID,
		SizeBytes:                      capBytes,
		EnableUniformBucketLevelAccess: true,
	}

	storageService, err := s.prepareStorageService(ctx, secrets)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "failed to prepare storage service: %v", err)
	}
	defer storageService.Close()

	// Check if the bucket already exists
	bucket, err := storageService.GetBucket(ctx, newBucket)
	if err != nil && !storage.IsNotExistErr(err) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if bucket != nil {
		klog.V(4).Infof("Found existing bucket %+v, current bucket %+v\n", bucket, newBucket)
		// Bucket already exists, check if it meets the request
		if err = storage.CompareBuckets(newBucket, bucket); err != nil {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}
	} else {
		// Add labels
		labels, err := extractLabels(param, s.driver.config.Name)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		newBucket.Labels = labels

		// Create the bucket
		var createErr error
		bucket, createErr = storageService.CreateBucket(ctx, newBucket)
		if createErr != nil {
			return nil, status.Error(codes.Internal, createErr.Error())
		}
	}
	resp := &csi.CreateVolumeResponse{Volume: bucketToCSIVolume(bucket)}

	return resp, nil
}

func (s *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	// Validate arguments
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "DeleteVolume volumeID must be provided")
	}

	if acquired := s.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, volumeID)
	}
	defer s.volumeLocks.Release(volumeID)

	storageService, err := s.prepareStorageService(ctx, req.GetSecrets())
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "failed to prepare storage service: %v", err)
	}

	// Delete the volume
	err = storageService.DeleteBucket(ctx, &storage.ServiceBucket{Name: volumeID})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.DeleteVolumeResponse{}, nil
}

// prepareStorageService prepares the GCS Storage Service using CreateVolume/DeleteVolume sercets.
func (s *controllerServer) prepareStorageService(ctx context.Context, secrets map[string]string) (storage.Service, error) {
	serviceAccountName, ok := secrets["serviceAccountName"]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "serviceAccountName must be provided in secret")
	}
	serviceAccountNamespace, ok := secrets["serviceAccountNamespace"]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "serviceAccountNamespace must be provided in secret")
	}

	ts := s.driver.config.TokenManager.GetTokenSourceFromK8sServiceAccount(serviceAccountNamespace, serviceAccountName, "", "" /*audience*/, false)
	// TODO(urielguzman): Dynamically fetch the Google Universe if custom endpoint isn't provided.
	storageService, err := s.storageServiceManager.SetupService(ctx, ts, "")
	if err != nil {
		return nil, fmt.Errorf("storage service manager failed to setup service: %w", err)
	}

	return storageService, nil
}

// bucketToCSIVolume generates a CSI volume spec from the Google Cloud Storage Bucket.
func bucketToCSIVolume(bucket *storage.ServiceBucket) *csi.Volume {
	resp := &csi.Volume{
		CapacityBytes: bucket.SizeBytes,
		VolumeId:      bucket.Name,
	}

	return resp
}

func getRequestCapacity(capRange *csi.CapacityRange) (int64, error) {
	var capBytes int64
	// Default case where nothing is set
	if capRange == nil {
		capBytes = MinimumVolumeSizeInBytes

		return capBytes, nil
	}

	rBytes := capRange.GetRequiredBytes()
	rSet := rBytes > 0
	lBytes := capRange.GetLimitBytes()
	lSet := lBytes > 0

	if lSet && rSet && lBytes < rBytes {
		return 0, fmt.Errorf("limit bytes %v is less than required bytes %v", lBytes, rBytes)
	}
	if lSet && lBytes < MinimumVolumeSizeInBytes {
		return 0, fmt.Errorf("limit bytes %v is less than minimum volume size: %v", lBytes, MinimumVolumeSizeInBytes)
	}

	// If Required set just set capacity to that which is Required
	if rSet {
		capBytes = rBytes
	}

	// Limit is more than Required, but larger than Minimum. So we just set capcity to Minimum
	// Too small, default
	if capBytes < MinimumVolumeSizeInBytes {
		capBytes = MinimumVolumeSizeInBytes
	}

	return capBytes, nil
}

func extractLabels(parameters map[string]string, driverName string) (map[string]string, error) {
	labels := make(map[string]string)
	scLabels := make(map[string]string)
	for k, v := range parameters {
		switch strings.ToLower(k) {
		case ParameterKeyPVCName:
			labels[tagKeyCreatedForClaimName] = v
		case ParameterKeyPVCNamespace:
			labels[tagKeyCreatedForClaimNamespace] = v
		case ParameterKeyPVName:
			labels[tagKeyCreatedForVolumeName] = v
		case ParameterKeyLabels:
			var err error
			scLabels, err = util.ConvertLabelsStringToMap(v)
			if err != nil {
				return nil, fmt.Errorf("parameters contain invalid labels parameter: %w", err)
			}
		}
	}

	labels[tagKeyCreatedBy] = strings.ReplaceAll(driverName, ".", "_")
	labels, err := mergeLabels(scLabels, labels)
	if err != nil {
		return nil, err
	}

	// TODO: validate labels: https://cloud.google.com/storage/docs/tags-and-labels#bucket-labels
	for k, v := range labels {
		labels[k] = strings.ReplaceAll(v, ".", "_")
	}

	return mergeLabels(scLabels, labels)
}

func mergeLabels(scLabels map[string]string, metedataLabels map[string]string) (map[string]string, error) {
	result := make(map[string]string)
	for k, v := range metedataLabels {
		result[k] = v
	}

	for k, v := range scLabels {
		if _, ok := result[k]; ok {
			return nil, fmt.Errorf("storage Class labels cannot contain metadata label key %s", k)
		}

		result[k] = v
	}

	return result, nil
}
