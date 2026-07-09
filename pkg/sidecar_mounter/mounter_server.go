/*
Copyright 2026 Google LLC

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

package sidecarmounter

import (
	"context"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/proto/mounter"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type MounterServer struct {
	mounter.UnimplementedMounterServer
	Mounter *Mounter
}

func NewMounterServer(mounter *Mounter) *MounterServer {
	return &MounterServer{
		Mounter: mounter,
	}
}

func (ms *MounterServer) Mount(ctx context.Context, req *mounter.MountRequest) (*mounter.MountResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Mount not implemented")
}
