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
	GetTokenSourceFromK8sServiceAccount(saNamespace, saName, saToken, tsEndpoint string) oauth2.TokenSource
}

type tokenManager struct {
	meta       metadata.Service
	k8sClients clientset.Interface
}

func NewTokenManager(meta metadata.Service, clientset clientset.Interface) TokenManager {
	tm := tokenManager{
		meta:       meta,
		k8sClients: clientset,
	}

	return &tm
}

func (tm *tokenManager) GetTokenSourceFromK8sServiceAccount(saNamespace, saName, saToken, tsEndpoint string) oauth2.TokenSource {
	return &GCPTokenSource{
		meta:           tm.meta,
		k8sSAName:      saName,
		k8sSANamespace: saNamespace,
		k8sSAToken:     saToken,
		k8sClients:     tm.k8sClients,
		endpoint:       tsEndpoint,
	}
}
