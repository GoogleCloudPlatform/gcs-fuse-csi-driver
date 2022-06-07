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
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	sts "google.golang.org/api/sts/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/metadata"
)

type K8sServiceAccountInfo struct {
	Namespace          string
	Name               string
	TokenRequestStatus *authenticationv1.TokenRequestStatus
}

// GCPTokenSource generates a GCP IAM SA token with a Kubernetes Service Account token.
type GCPTokenSource struct {
	ctx        context.Context
	meta       metadata.Service
	k8sSA      K8sServiceAccountInfo
	k8sClients *kubernetes.Clientset
}

// Token exchanges a GCP IAM SA Token with a Kubernetes Service Account token
// using Security Token Service (STS) API.
func (ts *GCPTokenSource) Token() (*oauth2.Token, error) {
	err := ts.fetchK8sSAToken()
	if err != nil {
		return nil, fmt.Errorf("k8s service account token fetch error: %v", err)
	}

	k8sToken := &oauth2.Token{
		AccessToken: ts.k8sSA.TokenRequestStatus.Token,
		Expiry:      ts.k8sSA.TokenRequestStatus.ExpirationTimestamp.Time,
	}

	stsService, err := sts.NewService(ts.ctx, option.WithHTTPClient(&http.Client{}))
	if err != nil {
		return nil, fmt.Errorf("new STS service error: %v", err)
	}

	audience := fmt.Sprintf(
		"identitynamespace:%s:%s",
		ts.meta.GetIdentityPool(),
		ts.meta.GetIdentityProvider(),
	)
	stsRequest := &sts.GoogleIdentityStsV1ExchangeTokenRequest{
		Audience:           audience,
		GrantType:          "urn:ietf:params:oauth:grant-type:token-exchange",
		Scope:              "https://www.googleapis.com/auth/cloud-platform",
		RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
		SubjectTokenType:   "urn:ietf:params:oauth:token-type:jwt",
		SubjectToken:       k8sToken.AccessToken,
	}

	stsResponse, err := stsService.V1.Token(stsRequest).Do()
	if err != nil {
		return nil, fmt.Errorf("token exchange error: %v", err)
	}

	token := &oauth2.Token{
		AccessToken: stsResponse.AccessToken,
		TokenType:   stsResponse.TokenType,
		Expiry:      time.Now().Add(time.Second * time.Duration(stsResponse.ExpiresIn)),
	}
	return token, nil
}

func (ts *GCPTokenSource) fetchK8sSAToken() error {
	if ts.k8sSA.TokenRequestStatus != nil {
		return nil
	}
	ttl := int64(1 * time.Hour.Seconds())
	resp, err := ts.k8sClients.
		CoreV1().
		ServiceAccounts(ts.k8sSA.Namespace).
		CreateToken(
			ts.ctx,
			ts.k8sSA.Name,
			&authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					ExpirationSeconds: &ttl,
					Audiences:         []string{ts.meta.GetIdentityPool()},
				},
			},
			v1.CreateOptions{},
		)
	if err != nil {
		return fmt.Errorf("failed to call Kubernetes ServiceAccount.CreateToken API: %v", err)
	}

	ts.k8sSA.TokenRequestStatus = &resp.Status
	return nil
}
