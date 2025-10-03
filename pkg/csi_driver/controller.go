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
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	if cs.features.FeatureGCSFuseProfiles.Enabled {
		s, err := profiles.NewScanner(cs.features.FeatureGCSFuseProfiles.ScannerConfig)
		if err != nil {
			return nil, err
		}
		cs.scanner = s
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
	storageService, err := s.storageServiceManager.SetupService(ctx, ts)
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
