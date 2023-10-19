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
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *controllerServer) ControllerPublishVolume(_ context.Context, _ *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerPublishVolume unsupported")
}

func (s *controllerServer) ControllerUnpublishVolume(_ context.Context, _ *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerUnpublishVolume unsupported")
}

func (s *controllerServer) ControllerExpandVolume(_ context.Context, _ *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerExpandVolume unsupported")
}

func (s *controllerServer) CreateSnapshot(_ context.Context, _ *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "CreateSnapshot unsupported")
}

func (s *controllerServer) DeleteSnapshot(_ context.Context, _ *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "DeleteSnapshot unsupported")
}

func (s *controllerServer) ListSnapshots(_ context.Context, _ *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListSnapshots unsupported")
}

func (s *controllerServer) ListVolumes(_ context.Context, _ *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListVolumes unsupported")
}

func (s *controllerServer) GetCapacity(_ context.Context, _ *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetCapacity unsupported")
}

func (s *controllerServer) ControllerGetVolume(_ context.Context, _ *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerGetVolume unsupported")
}

func (s *controllerServer) ControllerModifyVolume(_ context.Context, _ *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume unsupported")
}
