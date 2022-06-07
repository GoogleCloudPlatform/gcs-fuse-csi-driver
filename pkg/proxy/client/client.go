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

package proxyclient

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/auth"
	mount_gcs "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/pb"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/util"
)

const (
	// Interval of logging connection errors
	connectionLoggingInterval = 10 * time.Second

	// NodePublishVolume VolumeContext pod UID key
	VolumeContextKeyPodUID = "csi.storage.k8s.io/pod.uid"
)

type ProxyClient interface {
	UpdateGCPToken(ctx context.Context, vc map[string]string) error
	CleanupGCPToken(ctx context.Context, targetPath string) error
	MountGCS(ctx context.Context, req *mount_gcs.MountGCSRequest) error
}

type GCSFuseProxyClient struct {
	gcsfuseProxyClient mount_gcs.GCSFuseProxyServiceClient
	tokenManager       auth.TokenManager
}

// NewGCSFuseProxyClient returns a new ProxyServiceClient for GCS Fuse Proxy
func NewGCSFuseProxyClient(proxyEndpoint string, tm auth.TokenManager) (ProxyClient, error) {
	conn, err := connect(proxyEndpoint)
	if err != nil {
		return nil, err
	}
	return &GCSFuseProxyClient{
		gcsfuseProxyClient: mount_gcs.NewGCSFuseProxyServiceClient(conn),
		tokenManager:       tm,
	}, nil
}

func (c *GCSFuseProxyClient) UpdateGCPToken(ctx context.Context, vc map[string]string) error {
	podID := vc[VolumeContextKeyPodUID]
	if len(podID) == 0 {
		return fmt.Errorf("NodePublishVolume VolumeContext %s must be provided", VolumeContextKeyPodUID)
	}
	sa, err := c.tokenManager.GetK8sServiceAccountFromVolumeContext(vc)
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes Service Account with error: %v", err)
	}
	getReq := mount_gcs.GetGCPTokenExpiryRequest{
		PodID: podID,
	}
	klog.V(2).Infof("calling GCSfuseProxy: GetGCPTokenExpiry function")
	getResp, err := c.gcsfuseProxyClient.GetGCPTokenExpiry(ctx, &getReq)
	if err == nil && getResp.Expiry.AsTime().After(time.Now().Add(time.Minute*5)) {
		klog.V(2).Infof("no need to update token for Pod %v", podID)
		return nil
	}

	ts, err := c.tokenManager.GetTokenSourceFromK8sServiceAccount(ctx, sa)
	if err != nil {
		return err
	}

	gcpToken, err := ts.Token()
	if err != nil {
		return err
	}

	putReq := mount_gcs.PutGCPTokenRequest{
		PodID:  podID,
		Token:  gcpToken.AccessToken,
		Expiry: timestamppb.New(gcpToken.Expiry),
	}
	klog.V(2).Infof("calling GCSfuseProxy: PutGCPToken function to update GCP token for Pod %v", podID)
	_, err = c.gcsfuseProxyClient.PutGCPToken(ctx, &putReq)
	if err != nil {
		return fmt.Errorf("PutGCPToken failed with error: %w", err)
	}
	return nil
}

func (c *GCSFuseProxyClient) CleanupGCPToken(ctx context.Context, targetPath string) error {
	podID, _, err := util.ParsePodIDVolume(targetPath)
	if err != nil {
		return fmt.Errorf("failed to extract Pod ID: %v", err)
	}

	req := mount_gcs.DeleteGCPTokenRequest{
		PodID: podID,
	}
	klog.V(2).Infof("calling GCSfuseProxy: DeleteGCPToken function to cleanup GCP token for Pod %v", podID)
	_, err = c.gcsfuseProxyClient.DeleteGCPToken(ctx, &req)
	if err != nil {
		return fmt.Errorf("DeleteGCPToken failed with error: %v", err)
	}
	return nil
}

func (c *GCSFuseProxyClient) MountGCS(ctx context.Context, req *mount_gcs.MountGCSRequest) error {
	_, err := c.gcsfuseProxyClient.MountGCS(ctx, req)
	return err
}

func connect(address string) (*grpc.ClientConn, error) {
	scheme, addr, err := util.ParseEndpoint(address, false)
	if err != nil {
		return nil, fmt.Errorf("failed to parse endpoint %v", err.Error())
	}

	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()), // Don't use TLS, it's usually local Unix domain socket in a container.
		grpc.WithBlock(), // Block until connection succeeds.
	}

	// state variables for the custom dialer
	haveConnected := false
	lostConnection := false

	dialOptions = append(dialOptions, grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		if haveConnected && !lostConnection {
			// We have detected a loss of connection.
			klog.Errorf("Lost connection to %s.", address)
			lostConnection = true
		}

		conn, err := net.DialTimeout(scheme, addr, time.Second*60)
		if err == nil {
			// Connection reestablished.
			haveConnected = true
			lostConnection = false
		}
		return conn, err
	}))

	klog.V(5).Infof("Connecting to %s", address)

	// Connect in background.
	var conn *grpc.ClientConn
	ready := make(chan bool)
	go func() {
		conn, err = grpc.Dial(addr, dialOptions...)
		close(ready)
	}()

	// Log error every connectionLoggingInterval
	ticker := time.NewTicker(connectionLoggingInterval)
	defer ticker.Stop()

	// Wait until Dial() succeeds.
	for {
		select {
		case <-ticker.C:
			klog.Warningf("Still connecting to %s", address)

		case <-ready:
			return conn, err
		}
	}
}
