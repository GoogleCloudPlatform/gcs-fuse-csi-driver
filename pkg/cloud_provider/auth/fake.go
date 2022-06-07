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

package auth

import (
	"context"
	"time"

	"golang.org/x/oauth2"
)

type fakeTokenManager struct{}

func NewFakeTokenManager() TokenManager {
	return &fakeTokenManager{}
}

func (tm *fakeTokenManager) GetK8sServiceAccountFromVolumeContext(volumeContext map[string]string) (*K8sServiceAccountInfo, error) {
	sa := &K8sServiceAccountInfo{
		Name:               volumeContext["csi.storage.k8s.io/serviceAccount.name"],
		Namespace:          volumeContext["csi.storage.k8s.io/pod.namespace"],
		TokenRequestStatus: nil,
	}
	return sa, nil
}

func (tm *fakeTokenManager) GetTokenSourceFromK8sServiceAccount(ctx context.Context, sa *K8sServiceAccountInfo) (oauth2.TokenSource, error) {
	return &FakeGCPTokenSource{}, nil
}

type FakeGCPTokenSource struct{}

func (ts *FakeGCPTokenSource) Token() (*oauth2.Token, error) {
	token := &oauth2.Token{
		AccessToken: "test-token",
		Expiry:      time.Now().Add(time.Hour),
	}
	return token, nil
}
