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

	mount "k8s.io/mount-utils"
	mount_gcs "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/gcsfuse_proxy/pb"
)

type FakeProxyClient struct {
	fm *mount.FakeMounter
}

// NewFakeProxyClient returns a fake ProxyClient
func NewFakeProxyClient(fm *mount.FakeMounter) ProxyClient {
	return &FakeProxyClient{fm: fm}
}

func (c *FakeProxyClient) UpdateGCPToken(ctx context.Context, vc map[string]string) error {
	return nil
}

func (c *FakeProxyClient) CleanupGCPToken(ctx context.Context, targetPath string) error {
	return nil
}

func (c *FakeProxyClient) MountGCS(ctx context.Context, req *mount_gcs.MountGCSRequest) error {
	source := req.GetSource()
	target := req.GetTarget()
	fstype := req.GetFstype()
	options := req.GetOptions()
	sensitiveOptions := req.GetSensitiveOptions()

	return c.fm.MountSensitive(source, target, fstype, options, sensitiveOptions)
}
