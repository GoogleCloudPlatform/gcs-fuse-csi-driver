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
	"encoding/json"
	"fmt"

	"golang.org/x/oauth2"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/metadata"
)

// NodePublishVolume VolumeContext keys
const (
	VolumeContextKeyServiceAccountToken = "csi.storage.k8s.io/serviceAccount.tokens"
	VolumeContextKeyServiceAccountName  = "csi.storage.k8s.io/serviceAccount.name"
	VolumeContextKeyPodNamespace        = "csi.storage.k8s.io/pod.namespace"
)

type TokenManager interface {
	GetTokenSourceFromK8sServiceAccount(ctx context.Context, sa *K8sServiceAccountInfo) (oauth2.TokenSource, error)
	GetK8sServiceAccountFromVolumeContext(volumeContext map[string]string) (*K8sServiceAccountInfo, error)
}

type tokenManager struct {
	meta       metadata.Service
	k8sClients *kubernetes.Clientset
}

func NewTokenManager(meta metadata.Service, kubeconfig string) (TokenManager, error) {
	var rc *rest.Config
	var err error
	if kubeconfig != "" {
		klog.V(5).InfoS("using kubeconfig", "path", kubeconfig)
		rc, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		klog.V(5).InfoS("using in-cluster kubeconfig")
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		klog.ErrorS(err, "failed to read kubeconfig")
		klog.Fatal("failed to read kubeconfig")
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(rc)
	if err != nil {
		klog.ErrorS(err, "failed to configure k8s client")
		klog.Fatal("failed to configure k8s client")
		return nil, err
	}

	tm := tokenManager{
		meta:       meta,
		k8sClients: clientset,
	}
	return &tm, nil
}

func (tm *tokenManager) GetK8sServiceAccountFromVolumeContext(vc map[string]string) (*K8sServiceAccountInfo, error) {
	tokenString, ok := vc[VolumeContextKeyServiceAccountToken]
	if !ok {
		return nil, fmt.Errorf("VolumeContext %s must be provided", VolumeContextKeyServiceAccountToken)
	}
	tokenMap := make(map[string]*authenticationv1.TokenRequestStatus)
	if err := json.Unmarshal([]byte(tokenString), &tokenMap); err != nil {
		return nil, err
	}

	saName, ok := vc[VolumeContextKeyServiceAccountName]
	if !ok {
		return nil, fmt.Errorf("VolumeContext %s must be provided", VolumeContextKeyServiceAccountName)
	}

	saNamespace, ok := vc[VolumeContextKeyPodNamespace]
	if !ok {
		return nil, fmt.Errorf("VolumeContext %s must be provided", VolumeContextKeyPodNamespace)
	}

	sa := &K8sServiceAccountInfo{
		Name:      saName,
		Namespace: saNamespace,
	}
	if trs, ok := tokenMap[tm.meta.GetIdentityPool()]; ok {
		sa.Token = &oauth2.Token{
			AccessToken: trs.Token,
			Expiry:      trs.ExpirationTimestamp.Time,
		}
	}
	return sa, nil
}

func (tm *tokenManager) GetTokenSourceFromK8sServiceAccount(ctx context.Context, sa *K8sServiceAccountInfo) (oauth2.TokenSource, error) {
	tokenSource := &GCPTokenSource{
		ctx:        ctx,
		meta:       tm.meta,
		k8sSA:      sa,
		k8sClients: tm.k8sClients,
	}
	return tokenSource, nil
}
