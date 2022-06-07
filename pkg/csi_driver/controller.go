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
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/auth"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/storage"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/metrics"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/util"
)

const (
	MinimumVolumeSizeInBytes int64 = 1 * util.Mb
)

// CreateVolume parameters
const (
	// Keys for PV and PVC parameters as reported by external-provisioner
	ParameterKeyPVCName      = "csi.storage.k8s.io/pvc/name"
	ParameterKeyPVCNamespace = "csi.storage.k8s.io/pvc/namespace"
	ParameterKeyPVName       = "csi.storage.k8s.io/pv/name"

	// User provided labels
	ParameterKeyLabels = "labels"

	// Keys for tags to attach to the provisioned disk.
	tagKeyCreatedForClaimNamespace = "kubernetes_io_created-for_pvc_namespace"
	tagKeyCreatedForClaimName      = "kubernetes_io_created-for_pvc_name"
	tagKeyCreatedForVolumeName     = "kubernetes_io_created-for_pv_name"
	tagKeyCreatedBy                = "storage_gke_io_created-by"
)

// controllerServer handles volume provisioning
type controllerServer struct {
	driver                *GCSDriver
	storageServiceManager storage.ServiceManager
	volumeLocks           *util.VolumeLocks
	metricsManager        *metrics.Manager
}

func newControllerServer(driver *GCSDriver, storageServiceManager storage.ServiceManager, metricsManager *metrics.Manager) csi.ControllerServer {
	return &controllerServer{
		driver:                driver,
		storageServiceManager: storageServiceManager,
		volumeLocks:           util.NewVolumeLocks(),
		metricsManager:        metricsManager,
	}
}

func (s *controllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: s.driver.cscap,
	}, nil
}

func (s *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	// Validate arguments
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ValidateVolumeCapabilities volumeID must be provided")
	}
	caps := req.GetVolumeCapabilities()
	if len(caps) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ValidateVolumeCapabilities volume capabilities must be provided")
	}

	storageService, err := s.prepareStorageService(ctx, req.GetSecrets())
	if err != nil {
		return nil, err
	}

	// Check that the volume exists
	newBucket, err := storageService.GetBucket(ctx, &storage.ServiceBucket{Name: volumeID})
	if err != nil && !storage.IsNotExistErr(err) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if newBucket == nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("volume %v doesn't exist", volumeID))
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
	klog.V(5).Infof("Using capacity bytes %q for volume %q", capBytes, name)

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
		Project:   projectID,
		Name:      name,
		SizeBytes: capBytes,
		Labels:    param,
	}

	storageService, err := s.prepareStorageService(ctx, secrets)
	if err != nil {
		return nil, err
	}

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

		// Create the instance
		var createErr error
		bucket, createErr = storageService.CreateBucket(ctx, newBucket)
		if createErr != nil {
			klog.Errorf("Create volume for volume Id %s failed: %v", volumeID, createErr)
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
		return nil, err
	}

	// Check that the volume exists
	bucket, err := storageService.GetBucket(ctx, &storage.ServiceBucket{Name: volumeID})
	if err != nil {
		if storage.IsNotExistErr(err) {
			return &csi.DeleteVolumeResponse{}, nil
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	err = storageService.DeleteBucket(ctx, bucket)
	if err != nil {
		klog.Errorf("Delete volume for volume Id %s failed: %v", volumeID, err)
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &csi.DeleteVolumeResponse{}, nil
}

// prepareStorageService prepares the GCS Storage Service using CreateVolume/DeleteVolume sercets
func (s *controllerServer) prepareStorageService(ctx context.Context, secrets map[string]string) (storage.Service, error) {
	serviceAccountName, ok := secrets["serviceAccountName"]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "serviceAccountName must be provided in secret")
	}
	serviceAccountNamespace, ok := secrets["serviceAccountNamespace"]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "serviceAccountNamespace must be provided in secret")
	}
	sa := &auth.K8sServiceAccountInfo{
		Name:      serviceAccountName,
		Namespace: serviceAccountNamespace,
	}
	ts, err := s.driver.config.TokenManager.GetTokenSourceFromK8sServiceAccount(ctx, sa)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "token manager failed to get token source: %v", err)
	}

	storageService, err := s.storageServiceManager.SetupService(ctx, ts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "storage service manager failed to setup service: %v", err)
	}

	return storageService, nil
}

// bucketToCSIVolume generates a CSI volume spec from the Google Cloud Storage Bucket
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
	scLables := make(map[string]string)
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
			scLables, err = util.ConvertLabelsStringToMap(v)
			if err != nil {
				return nil, fmt.Errorf("parameters contain invalid labels parameter: %w", err)
			}
		}
	}

	labels[tagKeyCreatedBy] = strings.ReplaceAll(driverName, ".", "_")
	return mergeLabels(scLables, labels)
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
