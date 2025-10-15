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
	"cloud.google.com/go/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/metadata"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	sts "google.golang.org/api/sts/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/klog/v2"
)

// GCPTokenSource generates a GCP IAM SA token with a Kubernetes Service Account token.
type GCPTokenSource struct {
	meta               metadata.Service
	k8sSAName          string
	k8sSANamespace     string
	k8sSAToken         string
	k8sClients         clientset.Interface
	audience           string
	fetchTokenfromFile bool // This is set for sidecar where pod doesn't have access to get service account details
}

// Token exchanges a GCP IAM SA Token with a Kubernetes Service Account token.
func (ts *GCPTokenSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	var k8sSAToken string
	var err error
	// If fetchTokenfromFile is set we assume the request is coming from sidecar, as the sidecar (user pod) doesn't have access to get service account details we fetch the service account from file.
	if ts.fetchTokenfromFile {
		tokenPath := webhook.SidecarContainerSATokenVolumeMountPath + "/" + webhook.K8STokenPath
		k8sSAToken, err = util.FetchK8sTokenFromFile(tokenPath)
		if err != nil {
			return nil, fmt.Errorf("k8s service account token fetch error: %w from path %s", err, tokenPath)
		}
	} else {
		k8sSAToken, err = ts.fetchK8sSAToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("k8s service account token fetch error: %w", err)
		}
	}

	identityBindingToken, err := ts.FetchIdentityBindingToken(ctx, k8sSAToken, ts.audience)
	if err != nil {
		return nil, fmt.Errorf("identity binding token fetch error: %w", err)
	}
	// GCP SA Token was needed in pre WI world where we linked GCP SA Token and K8s SA token through annotation.
	// In the fetchGCPSAToken we check if there's any annotation on the SA and return IdentityBindingToken if no annotation specified.
	// If however fetchTokenfromFile is set we assume the request is coming from sidecar, as it doesn't have access to Get SA API we skip the check assuming the sidecar follows new setup.
	// The new setup is the default with WI
	if ts.fetchTokenfromFile {
		return identityBindingToken, nil
	}
	token, err := ts.fetchGCPSAToken(ctx, identityBindingToken)
	if err != nil {
		return nil, fmt.Errorf("GCP service account token fetch error: %w", err)
	}
	return token, nil
}

// fetch Kubernetes Service Account token by calling Kubernetes API.
// Skip this code path by using skipCSIBucketAccessCheck: "true". This path is still called
// with Host Network + sidecar bucket access check logic enabled (--enable-sidecar-bucket-access-check=true).
func (ts *GCPTokenSource) fetchK8sSAToken(ctx context.Context) (string, error) {
	audience := ts.meta.GetIdentityPool()
	identityProvider := ts.meta.GetIdentityProvider()

	// If identity provider is not a GKE identity provider, we use the identity provider as the
	// audience for the token request. For GKE, we use the identity pool.
	if !util.IsGKEIdentityProvider(identityProvider) {
		audience = identityProvider
	}

	if ts.k8sSAToken != "" {
		tokenMap := make(map[string]*authenticationv1.TokenRequestStatus)
		if err := json.Unmarshal([]byte(ts.k8sSAToken), &tokenMap); err != nil {
			return "", fmt.Errorf("failed to unmarshal TokenRequestStatus: %w", err)
		}

		if trs, ok := tokenMap[audience]; ok {
			return trs.Token, nil
		}

		return "", fmt.Errorf("could not find token for %q", audience)
	}

	ttl := int64(10 * time.Minute.Seconds())
	if ts.k8sClients == nil {
		return "", fmt.Errorf("failed to get Kubernetes client, cannot to create SA token")
	}
	resp, err := ts.k8sClients.CreateServiceAccountToken(
		ctx,
		ts.k8sSANamespace,
		ts.k8sSAName,
		&authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: &ttl,
				Audiences:         []string{audience},
			},
		})
	if err != nil {
		return "", fmt.Errorf("failed to call Kubernetes ServiceAccount.CreateToken API: %w", err)
	}
	return resp.Status.Token, nil

}

// fetch GCP IdentityBindingToken using the Kubernetes Service Account token
// by calling Security Token Service (STS) API.
func (ts *GCPTokenSource) FetchIdentityBindingToken(ctx context.Context, k8sSAToken string, audience string) (*oauth2.Token, error) {
	stsService, err := sts.NewService(ctx, option.WithHTTPClient(&http.Client{}))
	if err != nil {
		return nil, fmt.Errorf("new STS service error: %w", err)
	}

	identityProvider := ts.meta.GetIdentityProvider()
	if audience == "" {
		if util.IsGKEIdentityProvider(identityProvider) {
			audience = fmt.Sprintf(
				"identitynamespace:%s:%s",
				ts.meta.GetIdentityPool(),
				identityProvider,
			)
		} else {
			audience = identityProvider
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
		return nil, fmt.Errorf("IdentityBindingToken exchange error with audience %q: and request %v: error %w", audience, stsRequest, err)
	}

	return &oauth2.Token{
		AccessToken: stsResponse.AccessToken,
		TokenType:   stsResponse.TokenType,
		Expiry:      time.Now().Add(time.Second * time.Duration(stsResponse.ExpiresIn)),
	}, nil
}

// fetch GCP service account token by calling the IAM credentials endpoint using an IdentityBindingToken.
func (ts *GCPTokenSource) fetchGCPSAToken(ctx context.Context, identityBindingToken *oauth2.Token) (*oauth2.Token, error) {
	gcpSAName, err := ts.k8sClients.GetGCPServiceAccountName(ctx, ts.k8sSANamespace, ts.k8sSAName)
	if err != nil {
		return nil, fmt.Errorf("failed to get GCP SA from Kubernetes SA [%s/%s] annotation: %w", ts.k8sSANamespace, ts.k8sSAName, err)
	}
	if gcpSAName == "" {
		klog.V(4).Infof("Kubernetes SA [%s/%s] is not bound with a GCP SA, proceed with the IdentityBindingToken", ts.k8sSANamespace, ts.k8sSAName)

		return identityBindingToken, nil
	}

	gcpSAClient, err := credentials.NewIamCredentialsClient(
		ctx,
		option.WithTokenSource(oauth2.StaticTokenSource(identityBindingToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("create credentials client error: %w", err)
	}
	defer gcpSAClient.Close()

	resp, err := gcpSAClient.GenerateAccessToken(
		ctx,
		&credentialspb.GenerateAccessTokenRequest{
			Name: "projects/-/serviceAccounts/" + gcpSAName,
			Scope: []string{
				storage.ScopeFullControl,
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
