/*
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

	credentials "cloud.google.com/go/iam/credentials/apiv1"
	"cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/metadata"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	sts "google.golang.org/api/sts/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// GCPTokenSource generates a GCP IAM SA token with a Kubernetes Service Account token.
type GCPTokenSource struct {
	ctx            context.Context
	meta           metadata.Service
	k8sSAName      string
	k8sSANamespace string
	k8sClients     clientset.Interface
}

// Token exchanges a GCP IAM SA Token with a Kubernetes Service Account token.
func (ts *GCPTokenSource) Token() (*oauth2.Token, error) {
	k8sSAToken, err := ts.fetchK8sSAToken(ts.k8sSANamespace, ts.k8sSAName)
	if err != nil {
		return nil, fmt.Errorf("k8s service account token fetch error: %v", err)
	}

	identityBindingToken, err := ts.fetchIdentityBindingToken(k8sSAToken)
	if err != nil {
		return nil, fmt.Errorf("identity binding token fetch error: %v", err)
	}

	token, err := ts.fetchGCPSAToken(identityBindingToken)
	if err != nil {
		return nil, fmt.Errorf("GCP service account token fetch error: %v", err)
	}
	return token, nil
}

// fetch Kubernetes Service Account token by calling Kubernetes API
func (ts *GCPTokenSource) fetchK8sSAToken(saNamespace, saName string) (*oauth2.Token, error) {
	ttl := int64(1 * time.Hour.Seconds())
	resp, err := ts.k8sClients.CreateServiceAccountToken(
		ts.ctx,
		saNamespace,
		saName,
		&authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: &ttl,
				Audiences:         []string{ts.meta.GetIdentityPool()},
			},
		},
		v1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to call Kubernetes ServiceAccount.CreateToken API: %v", err)
	}

	return &oauth2.Token{
		AccessToken: resp.Status.Token,
		Expiry:      resp.Status.ExpirationTimestamp.Time,
	}, nil
}

// fetch GCP IdentityBindingToken using the Kubernetes Service Account token
// by calling Security Token Service (STS) API.
func (ts *GCPTokenSource) fetchIdentityBindingToken(k8sSAToken *oauth2.Token) (*oauth2.Token, error) {
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
		SubjectToken:       k8sSAToken.AccessToken,
	}

	stsResponse, err := stsService.V1.Token(stsRequest).Do()
	if err != nil {
		return nil, fmt.Errorf("IdentityBindingToken exchange error: %v", err)
	}

	return &oauth2.Token{
		AccessToken: stsResponse.AccessToken,
		TokenType:   stsResponse.TokenType,
		Expiry:      time.Now().Add(time.Second * time.Duration(stsResponse.ExpiresIn)),
	}, nil
}

// fetch GCP service account token by calling the IAM credentials endpoint using an IdentityBindingToken.
func (ts *GCPTokenSource) fetchGCPSAToken(identityBindingToken *oauth2.Token) (*oauth2.Token, error) {
	gcpSAClient, err := credentials.NewIamCredentialsClient(
		ts.ctx,
		option.WithTokenSource(oauth2.StaticTokenSource(identityBindingToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("create credentials client error: %v", err)
	}

	gcpSAName, err := ts.k8sClients.GetGCPServiceAccountName(ts.ctx, ts.k8sSANamespace, ts.k8sSAName, v1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get GCP SA from Kubernetes SA [%s/%s] annotation: %v.", ts.k8sSANamespace, ts.k8sSAName, err)
	}
	if gcpSAName == "" {
		klog.V(4).Infof("Kubernetes SA [%s/%s] is not bound with a GCP SA, proceed with the IdentityBindingToken", ts.k8sSANamespace, ts.k8sSAName)
		return identityBindingToken, nil
	}

	resp, err := gcpSAClient.GenerateAccessToken(
		ts.ctx,
		&credentialspb.GenerateAccessTokenRequest{
			Name: fmt.Sprintf(
				"projects/-/serviceAccounts/%s",
				gcpSAName,
			),
			Scope: []string{
				"https://www.googleapis.com/auth/devstorage.full_control",
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("fetch GCP service account token error: %v", err)
	}

	token := &oauth2.Token{AccessToken: resp.GetAccessToken()}
	if t := resp.GetExpireTime(); t != nil {
		token.Expiry = time.Unix(int64(t.GetSeconds()), int64(t.GetNanos())).UTC()
	}
	return token, nil
}
