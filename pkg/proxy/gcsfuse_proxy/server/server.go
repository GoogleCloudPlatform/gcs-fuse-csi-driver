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

package server

import (
	"context"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog"
	"k8s.io/mount-utils"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/auth"
	gcsfuse_proxy "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/pb"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/util"
)

var (
	mutex sync.Mutex
)

type GCSFuseProxyServiceServer struct {
	gcsfuse_proxy.UnimplementedGCSFuseProxyServiceServer
	tm                 *auth.TokenManager
	authServerEndpoint string
	mounter            mount.Interface
}

// NewGCSFuseProxyServiceServer returns a new GCSFuseProxyServiceServer
func NewGCSFuseProxyServiceServer(tm *auth.TokenManager, endpoint string, mounter mount.Interface) *GCSFuseProxyServiceServer {
	return &GCSFuseProxyServiceServer{
		tm:                 tm,
		authServerEndpoint: endpoint,
		mounter:            mounter,
	}
}

// MountGCS mounts a GCS bucket to given location
func (server *GCSFuseProxyServiceServer) MountGCS(ctx context.Context,
	req *gcsfuse_proxy.MountGCSRequest,
) (*emptypb.Empty, error) {
	mutex.Lock()
	defer mutex.Unlock()

	resp := &emptypb.Empty{}

	source := req.GetSource()
	target := req.GetTarget()
	fstype := req.GetFstype()
	options := req.GetOptions()
	sensitiveOptions := req.GetSensitiveOptions()

	options = append(options, "implicit_dirs")
	options = append(options, "app_name=gke-gcs-csi")

	podID, volume, err := util.ParsePodIDVolume(target)
	if err != nil {
		return resp, fmt.Errorf("failed to extract Pod ID and volume: %w", err)
	}
	options = append(options, fmt.Sprintf("log_file=/var/log/gcsfuse/%s_%s.log", podID, volume))
	// options = append(options, fmt.Sprintf("temp_dir=/var/lib/kubelet/pods/%s/volumes/kubernetes.io~empty-dir/%s-cache", podID, volume))
	sensitiveOptions = append(sensitiveOptions, fmt.Sprintf("token_url=%s?podID=%s", server.authServerEndpoint, podID))

	klog.V(2).Infof("received MountGCS request: mounting with source %s, target %s, fstype %s, options %v", source, target, fstype, options)

	err = server.mounter.MountSensitive(source, target, fstype, options, sensitiveOptions)
	if err != nil {
		return resp, fmt.Errorf("MountGCS error: %w", err)
	}
	return resp, nil
}

// PutGCPToken puts the GCP token in the token store for the given Pod ID.
func (server *GCSFuseProxyServiceServer) PutGCPToken(ctx context.Context,
	req *gcsfuse_proxy.PutGCPTokenRequest,
) (*emptypb.Empty, error) {
	mutex.Lock()
	defer mutex.Unlock()

	podID := req.GetPodID()
	token := req.GetToken()
	expiry := req.GetExpiry()
	klog.V(2).Infof("received PutGCPToken request: podID %v", podID)

	resp := &emptypb.Empty{}
	err := server.tm.PutToken(podID, token, expiry.AsTime())
	if err != nil {
		return resp, fmt.Errorf("PutGCPToken error: %w", err)
	}

	return resp, nil
}

// GetGCPToken gets the GCP token expiry for the given Pod ID.
func (server *GCSFuseProxyServiceServer) GetGCPTokenExpiry(ctx context.Context,
	req *gcsfuse_proxy.GetGCPTokenExpiryRequest,
) (*gcsfuse_proxy.GetGCPTokenExpiryResponse, error) {
	mutex.Lock()
	defer mutex.Unlock()

	podID := req.GetPodID()
	klog.V(2).Infof("received GetGCPTokenExpiry request: podID %v", podID)

	resp := &gcsfuse_proxy.GetGCPTokenExpiryResponse{}
	token, err := server.tm.GetToken(podID)
	if err != nil {
		return resp, fmt.Errorf("GetGCPTokenExpiry error: %w", err)
	}
	resp.Expiry = timestamppb.New(token.Expiry)

	return resp, nil
}

// DeleteGCPToken deletes the GCP token from the token store.
func (server *GCSFuseProxyServiceServer) DeleteGCPToken(ctx context.Context,
	req *gcsfuse_proxy.DeleteGCPTokenRequest,
) (*emptypb.Empty, error) {
	mutex.Lock()
	defer mutex.Unlock()

	podID := req.GetPodID()
	klog.V(2).Infof("received DeleteGCPToken request: podID %v", podID)

	resp := &emptypb.Empty{}
	err := server.tm.DeleteToken(podID)
	if err != nil {
		return resp, fmt.Errorf("PutGCPToken error: %w", err)
	}

	return resp, nil
}

func RunGRPCServer(
	mountServer gcsfuse_proxy.GCSFuseProxyServiceServer,
	enableTLS bool,
	listener net.Listener,
) error {
	serverOptions := []grpc.ServerOption{}
	grpcServer := grpc.NewServer(serverOptions...)

	gcsfuse_proxy.RegisterGCSFuseProxyServiceServer(grpcServer, mountServer)

	klog.V(2).Infof("Start GRPC server at %s, TLS = %t", listener.Addr().String(), enableTLS)
	return grpcServer.Serve(listener)
}
