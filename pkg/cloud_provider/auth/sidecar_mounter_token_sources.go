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
	"context"
	"fmt"
	"net/http"
	"time"

	"cloud.google.com/go/compute/metadata"
	credentials "cloud.google.com/go/iam/credentials/apiv1"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	csimetadata "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/metadata"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/api/sts/v1"
	"k8s.io/klog/v2"
)

type GCPSidecarTokenSource struct {
	meta         csimetadata.Service
	k8sSAToken   string
	k8sClients   clientset.Interface
	tokenManager TokenManager
}

// Token exchanges a GCP IAM SA Token with a Kubernetes Service Account token.
func (ts *GCPSidecarTokenSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	k8stoken, err := util.FetchK8sTokenFromFile(webhook.SidecarContainerSATokenVolumeMountPath + "/" + webhook.K8STokenPath)
	if err != nil {
		return nil, fmt.Errorf("k8s service account token fetch error: %w", err)
	}

	identityBindingToken, err := FetchIdentityBindingToken(ctx, k8stoken, ts.tokenManager)
	if err != nil {
		return nil, fmt.Errorf("identity binding token fetch error: %w", err)
	}
	return identityBindingToken, nil
}

func FetchIdentityBindingToken(ctx context.Context, k8sSAToken string, tm TokenManager) (*oauth2.Token, error) {
	stsService, err := sts.NewService(ctx, option.WithHTTPClient(&http.Client{}))
	if err != nil {
		return nil, fmt.Errorf("new STS service error: %w", err)
	}
	identityProvider := tm.GetIdentityProvider()
	audience := fmt.Sprintf(
		"identitynamespace:%s:%s",
		tm.GetIdentityPool(),
		identityProvider,
	)
	if identityProvider != "" {
		audience, err = getAudienceFromContextAndIdentityProvider(ctx, identityProvider)
		if err != nil {
			return nil, fmt.Errorf("failed to get audience from the context: %w", err)
		}
	}

	stsRequest := &sts.GoogleIdentityStsV1ExchangeTokenRequest{
		Audience:           audience,
		GrantType:          "urn:ietf:params:oauth:grant-type:token-exchange",
		Scope:              credentials.DefaultAuthScopes()[0],
		RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
		SubjectTokenType:   "urn:ietf:params:oauth:token-type:jwt",
		SubjectToken:       k8sSAToken,
	}

	stsResponse, err := stsService.V1.Token(stsRequest).Do()
	if err != nil {
		klog.Errorf("IdentityBindingToken exchange error with audience %q: %w", audience, err)
		return nil, fmt.Errorf("IdentityBindingToken exchange error with audience %q: %w", audience, err)
	}

	return &oauth2.Token{
		AccessToken: stsResponse.AccessToken,
		TokenType:   stsResponse.TokenType,
		Expiry:      time.Now().Add(time.Second * time.Duration(stsResponse.ExpiresIn)),
	}, nil
}

func getAudienceFromContextAndIdentityProvider(ctx context.Context, identityProvider string) (string, error) {
	projectID, err := metadata.ProjectIDWithContext(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get project ID: %w", err)
	}

	audience := fmt.Sprintf(
		"identitynamespace:%s.svc.id.goog:%s",
		projectID,
		identityProvider,
	)
	klog.Infof("audience: %s", audience)

	return audience, nil
}
