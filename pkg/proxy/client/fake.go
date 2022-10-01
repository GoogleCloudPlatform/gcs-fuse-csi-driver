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
	"time"

	"golang.org/x/oauth2"
	mount "k8s.io/mount-utils"
)

type FakeProxyClient struct {
	fm *mount.FakeMounter
}

// NewFakeProxyClient returns a fake ProxyClient
func NewFakeProxyClient(fm *mount.FakeMounter) ProxyClient {
	return &FakeProxyClient{fm: fm}
}

func (c *FakeProxyClient) PutGCPToken(ctx context.Context, podID string, token *oauth2.Token) error {
	return nil
}

func (c *FakeProxyClient) DeleteGCPToken(ctx context.Context, targetPath string) error {
	return nil
}

func (c *FakeProxyClient) GetGCPTokenExpiry(ctx context.Context, podID string) (time.Time, error) {
	return time.Time{}, nil
}

func (c *FakeProxyClient) MountGCS(ctx context.Context, source, target, fstype string, options, sensitiveOptions []string) error {
	return c.fm.MountSensitive(source, target, fstype, options, sensitiveOptions)
}
