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

	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog"
	mount_gcs "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/pb"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/util"
)

const (
	// Interval of logging connection errors
	connectionLoggingInterval = 10 * time.Second
)

type ProxyClient interface {
	PutGCPToken(ctx context.Context, podID string, token *oauth2.Token) error
	DeleteGCPToken(ctx context.Context, targetPath string) error
	GetGCPTokenExpiry(ctx context.Context, podID string) (time.Time, error)
	MountGCS(ctx context.Context, source, target, fstype string, options, sensitiveOptions []string) error
}

type GCSFuseProxyClient struct {
	gcsfuseProxyClient mount_gcs.GCSFuseProxyServiceClient
}

// NewGCSFuseProxyClient returns a new ProxyServiceClient for GCS Fuse Proxy
func NewGCSFuseProxyClient(proxyEndpoint string) (ProxyClient, error) {
	conn, err := connect(proxyEndpoint)
	if err != nil {
		return nil, err
	}
	return &GCSFuseProxyClient{
		gcsfuseProxyClient: mount_gcs.NewGCSFuseProxyServiceClient(conn),
	}, nil
}

func (c *GCSFuseProxyClient) PutGCPToken(ctx context.Context, podID string, token *oauth2.Token) error {
	if len(podID) == 0 {
		return fmt.Errorf("NodePublishVolume VolumeContext Pod ID must be provided")
	}

	req := mount_gcs.PutGCPTokenRequest{
		PodID:  podID,
		Token:  token.AccessToken,
		Expiry: timestamppb.New(token.Expiry),
	}
	klog.V(2).Infof("calling GCSfuseProxy: PutGCPToken function to update GCP token for Pod %v", podID)
	_, err := c.gcsfuseProxyClient.PutGCPToken(ctx, &req)
	if err != nil {
		return fmt.Errorf("PutGCPToken failed with error: %w", err)
	}
	return nil
}

func (c *GCSFuseProxyClient) DeleteGCPToken(ctx context.Context, targetPath string) error {
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

func (c *GCSFuseProxyClient) GetGCPTokenExpiry(ctx context.Context, podID string) (time.Time, error) {
	if len(podID) == 0 {
		return time.Time{}, fmt.Errorf("NodePublishVolume VolumeContext Pod ID must be provided")
	}

	req := mount_gcs.GetGCPTokenExpiryRequest{
		PodID: podID,
	}
	klog.V(2).Infof("calling GCSfuseProxy: DeleteGCPToken function to cleanup GCP token for Pod %v", podID)
	resp, err := c.gcsfuseProxyClient.GetGCPTokenExpiry(ctx, &req)
	if err != nil {
		return time.Time{}, fmt.Errorf("DeleteGCPToken failed with error: %v", err)
	}
	return resp.GetExpiry().AsTime(), nil
}

func (c *GCSFuseProxyClient) MountGCS(ctx context.Context, source, target, fstype string, options, sensitiveOptions []string) error {
	req := mount_gcs.MountGCSRequest{
		Source:           source,
		Target:           target,
		Fstype:           fstype,
		Options:          options,
		SensitiveOptions: sensitiveOptions,
	}
	_, err := c.gcsfuseProxyClient.MountGCS(ctx, &req)
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
