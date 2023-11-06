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
	pbSanitizer "github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

const (
	CreateVolumeCSIFullMethod      = "/csi.v1.Controller/CreateVolume"
	DeleteVolumeCSIFullMethod      = "/csi.v1.Controller/DeleteVolume"
	NodePublishVolumeCSIFullMethod = "/csi.v1.Node/NodePublishVolume"
)

func NewVolumeCapabilityAccessMode(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability_AccessMode {
	return &csi.VolumeCapability_AccessMode{Mode: mode}
}

func NewControllerServiceCapability(c csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: c,
			},
		},
	}
}

func NewNodeServiceCapability(c csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
	return &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: c,
			},
		},
	}
}

func logGRPC(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	var strippedReq string
	switch info.FullMethod {
	case CreateVolumeCSIFullMethod:
		strippedReq = pbSanitizer.StripSecrets(req).String()
	case DeleteVolumeCSIFullMethod:
		strippedReq = pbSanitizer.StripSecrets(req).String()
	case NodePublishVolumeCSIFullMethod:
		if nodePublishReq, ok := req.(*csi.NodePublishVolumeRequest); ok {
			if token, ok := nodePublishReq.VolumeContext[VolumeContextKeyServiceAccountToken]; ok {
				nodePublishReq.VolumeContext[VolumeContextKeyServiceAccountToken] = "***stripped***"
				strippedReq = fmt.Sprintf("%+v", nodePublishReq)
				nodePublishReq.VolumeContext[VolumeContextKeyServiceAccountToken] = token
			} else {
				strippedReq = fmt.Sprintf("%+v", req)
			}
		} else {
			klog.Errorf("failed to case req to *csi.NodePublishVolumeRequest")
		}
	default:
		strippedReq = fmt.Sprintf("%+v", req)
	}

	klog.V(4).Infof("%s called with request: %v", info.FullMethod, strippedReq)
	resp, err := handler(ctx, req)
	if err != nil {
		klog.Errorf("%s failed with error: %v", info.FullMethod, err)
	} else {
		if fmt.Sprintf("%v", resp) == "" {
			klog.V(4).Infof("%s succeeded.", info.FullMethod)
		} else {
			klog.V(4).Infof("%s succeeded with response: %s", info.FullMethod, resp)
		}
	}

	return resp, err
}

// joinMountOptions joins mount options eliminating duplicates.
func joinMountOptions(existingOptions []string, newOptions []string) []string {
	overwritableOptions := map[string]string{
		"gid":       "",
		"file-mode": "",
		"dir-mode":  "",
	}

	allMountOptions := sets.NewString()

	process := func(mountOption string) {
		if len(mountOption) > 0 {
			optionPair := strings.SplitN(mountOption, "=", 2)

			if len(optionPair) == 2 {
				if _, ok := overwritableOptions[optionPair[0]]; ok {
					overwritableOptions[optionPair[0]] = optionPair[1]

					return
				}
			}

			allMountOptions.Insert(mountOption)
		}
	}

	for _, mountOption := range existingOptions {
		process(mountOption)
	}

	for _, mountOption := range newOptions {
		process(mountOption)
	}

	for k, v := range overwritableOptions {
		if v != "" {
			allMountOptions.Insert(k + "=" + v)
		}
	}

	return allMountOptions.List()
}
