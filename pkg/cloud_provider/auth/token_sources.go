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
	"encoding/json"
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
	"k8s.io/klog/v2"
)

// GCPTokenSource generates a GCP IAM SA token with a Kubernetes Service Account token.
type GCPTokenSource struct {
	meta           metadata.Service
	k8sSAName      string
	k8sSANamespace string
	k8sSAToken     string
	k8sClients     clientset.Interface
	endpoint 			 string
}

// Token exchanges a GCP IAM SA Token with a Kubernetes Service Account token.
func (ts *GCPTokenSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	k8sSAToken, err := ts.fetchK8sSAToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s service account token fetch error: %w", err)
	}

	identityBindingToken, err := ts.fetchIdentityBindingToken(ctx, k8sSAToken)
	if err != nil {
		return nil, fmt.Errorf("identity binding token fetch error: %w", err)
	}

	token, err := ts.fetchGCPSAToken(ctx, identityBindingToken)
	if err != nil {
		return nil, fmt.Errorf("GCP service account token fetch error: %w", err)
	}

	return token, nil
}

// fetch Kubernetes Service Account token by calling Kubernetes API.
func (ts *GCPTokenSource) fetchK8sSAToken(ctx context.Context) (*oauth2.Token, error) {
	if ts.k8sSAToken != "" {
		tokenMap := make(map[string]*authenticationv1.TokenRequestStatus)
		if err := json.Unmarshal([]byte(ts.k8sSAToken), &tokenMap); err != nil {
			return nil, fmt.Errorf("failed to unmarshal TokenRequestStatus: %w", err)
		}
		if trs, ok := tokenMap[ts.meta.GetIdentityPool()]; ok {
			return &oauth2.Token{
				AccessToken: trs.Token,
				Expiry:      trs.ExpirationTimestamp.Time,
			}, nil
		}

		return nil, fmt.Errorf("could not find token for the identity pool %q", ts.meta.GetIdentityPool())
	}

	ttl := int64(10 * time.Minute.Seconds())
	resp, err := ts.k8sClients.CreateServiceAccountToken(
		ctx,
		ts.k8sSANamespace,
		ts.k8sSAName,
		&authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: &ttl,
				Audiences:         []string{ts.meta.GetIdentityPool()},
			},
		})
	if err != nil {
		return nil, fmt.Errorf("failed to call Kubernetes ServiceAccount.CreateToken API: %w", err)
	}

	return &oauth2.Token{
		AccessToken: resp.Status.Token,
		Expiry:      resp.Status.ExpirationTimestamp.Time,
	}, nil
}

// fetch GCP IdentityBindingToken using the Kubernetes Service Account token
// by calling Security Token Service (STS) API.
func (ts *GCPTokenSource) fetchIdentityBindingToken(ctx context.Context, k8sSAToken *oauth2.Token) (*oauth2.Token, error) {

	stsOpts := []option.ClientOption{option.WithHTTPClient(&http.Client{})}
	if ts.endpoint != "" {
		stsOpts = append(stsOpts, option.WithEndpoint(ts.endpoint))
	}

	stsService, err := sts.NewService(ctx, stsOpts...)
	if err != nil {
		return nil, fmt.Errorf("new STS service error: %w", err)
	}

	audience := fmt.Sprintf(
		"identitynamespace:%s:%s",
		ts.meta.GetIdentityPool(),
		ts.meta.GetIdentityProvider(),
	)
	stsRequest := &sts.GoogleIdentityStsV1ExchangeTokenRequest{
		Audience:           audience,
		GrantType:          "urn:ietf:params:oauth:grant-type:token-exchange",
		Scope:              credentials.DefaultAuthScopes()[0],
		RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
		SubjectTokenType:   "urn:ietf:params:oauth:token-type:jwt",
		SubjectToken:       k8sSAToken.AccessToken,
	}

	stsResponse, err := stsService.V1.Token(stsRequest).Do()
	if err != nil {
		return nil, fmt.Errorf("IdentityBindingToken exchange error with audience %q: %w", audience, err)
	}

	return &oauth2.Token{
		AccessToken: stsResponse.AccessToken,
		TokenType:   stsResponse.TokenType,
		Expiry:      time.Now().Add(time.Second * time.Duration(stsResponse.ExpiresIn)),
	}, nil
}

// fetch GCP service account token by calling the IAM credentials endpoint using an IdentityBindingToken.
func (ts *GCPTokenSource) fetchGCPSAToken(ctx context.Context, identityBindingToken *oauth2.Token) (*oauth2.Token, error) {
	gcpSAClient, err := credentials.NewIamCredentialsClient(
		ctx,
		option.WithTokenSource(oauth2.StaticTokenSource(identityBindingToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("create credentials client error: %w", err)
	}

	gcpSAName, err := ts.k8sClients.GetGCPServiceAccountName(ctx, ts.k8sSANamespace, ts.k8sSAName)
	if err != nil {
		return nil, fmt.Errorf("failed to get GCP SA from Kubernetes SA [%s/%s] annotation: %w", ts.k8sSANamespace, ts.k8sSAName, err)
	}
	if gcpSAName == "" {
		klog.V(4).Infof("Kubernetes SA [%s/%s] is not bound with a GCP SA, proceed with the IdentityBindingToken", ts.k8sSANamespace, ts.k8sSAName)

		return identityBindingToken, nil
	}

	resp, err := gcpSAClient.GenerateAccessToken(
		ctx,
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
		return nil, fmt.Errorf("fetch GCP service account token error: %w", err)
	}

	token := &oauth2.Token{AccessToken: resp.GetAccessToken()}
	if t := resp.GetExpireTime(); t != nil {
		token.Expiry = time.Unix(t.GetSeconds(), int64(t.GetNanos())).UTC()
	}

	return token, nil
}
