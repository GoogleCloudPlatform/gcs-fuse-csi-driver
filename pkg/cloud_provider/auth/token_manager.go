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

package auth

import (
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/metadata"
	"golang.org/x/oauth2"
)

// NodePublishVolume VolumeContext keys.
const (
	VolumeContextKeyServiceAccountName = "csi.storage.k8s.io/serviceAccount.name"
	VolumeContextKeyPodNamespace       = "csi.storage.k8s.io/pod.namespace"
)

type TokenManager interface {
	GetTokenSourceFromK8sServiceAccount(saNamespace, saName, saToken string) oauth2.TokenSource
}

type tokenManager struct {
	meta             metadata.Service
	k8sClients       clientset.Interface
	tokenSourceStore *TokenSourceStore
}

func NewTokenManager(meta metadata.Service, clientset clientset.Interface, ttl, cleanupFreq time.Duration) TokenManager {
	tm := tokenManager{
		meta:             meta,
		k8sClients:       clientset,
		tokenSourceStore: NewTokenSourceStore(ttl, cleanupFreq),
	}

	return &tm
}

func (tm *tokenManager) GetTokenSourceFromK8sServiceAccount(saNamespace, saName, saTokenStr string) oauth2.TokenSource {
	ts := tm.tokenSourceStore.Get(saNamespace, saName)
	if ts == nil {
		ts = &GCPTokenSource{
			meta:           tm.meta,
			k8sSAName:      saName,
			k8sSANamespace: saNamespace,
			k8sSATokenStr:  saTokenStr,
			k8sClients:     tm.k8sClients,
		}

		tm.tokenSourceStore.Set(saNamespace, saName, ts)
	}

	// always use the latest Kubernetes Service Account token
	ts.k8sSATokenStr = saTokenStr

	return ts
}
