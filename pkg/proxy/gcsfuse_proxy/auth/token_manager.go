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
	"fmt"
	"time"

	"golang.org/x/oauth2"
)

type TokenManager struct {
	tokenStore map[string]*oauth2.Token
}

func NewTokenManager() *TokenManager {
	return &TokenManager{tokenStore: map[string]*oauth2.Token{}}
}

func (tm *TokenManager) PutToken(podID, token string, expiry time.Time) error {
	tm.tokenStore[podID] = &oauth2.Token{AccessToken: token, Expiry: expiry}
	return nil
}

func (tm *TokenManager) GetToken(podID string) (*oauth2.Token, error) {
	if token, ok := tm.tokenStore[podID]; ok {
		return token, nil
	}
	return nil, fmt.Errorf("podID %v does not exist", podID)
}

func (tm *TokenManager) DeleteToken(podID string) error {
	if _, ok := tm.tokenStore[podID]; ok {
		delete(tm.tokenStore, podID)
		return nil
	}
	return fmt.Errorf("podID %v does not exist", podID)
}
