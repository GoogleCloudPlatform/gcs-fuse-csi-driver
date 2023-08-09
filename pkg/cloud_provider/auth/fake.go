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
	"golang.org/x/oauth2"
)

type fakeTokenManager struct{}

func NewFakeTokenManager() TokenManager {
	return &fakeTokenManager{}
}

func (tm *fakeTokenManager) GetTokenSourceFromK8sServiceAccount(saNamespace, saName, _, _ string) oauth2.TokenSource {
	return &FakeGCPTokenSource{k8sSAName: saName, k8sSANamespace: saNamespace}
}

type FakeGCPTokenSource struct {
	k8sSAName      string
	k8sSANamespace string
}

func (ts *FakeGCPTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{}, nil
}
