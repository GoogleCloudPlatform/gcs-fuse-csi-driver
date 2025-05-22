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

package metadata

import (
	"context"
	"fmt"

	"cloud.google.com/go/compute/metadata"
	"k8s.io/klog/v2"
)

type Service interface {
	GetProjectID() string
	GetIdentityPool() string
	GetIdentityProvider() string
	GetCustomAudience() string
}

type metadataServiceManager struct {
	projectID        string
	identityPool     string
	identityProvider string
	customAudience   string
}

var _ Service = &metadataServiceManager{}

func NewMetadataService(identityPool, identityProvider string, customAudience string) (Service, error) {
	projectID, err := metadata.ProjectIDWithContext(context.Background())

	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	if len(customAudience) != 0 {
		projectID, err = metadata.NumericProjectIDWithContext(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to get project: %w", err)
		}
	}

	if identityPool == "" {
		klog.Infof("got empty identityPool, constructing the identityPool using projectID")
		identityPool = projectID + ".svc.id.goog"
	}

	return &metadataServiceManager{
		projectID:        projectID,
		identityPool:     identityPool,
		identityProvider: identityProvider,
		customAudience:   customAudience,
	}, nil
}

func (manager *metadataServiceManager) GetProjectID() string {
	return manager.projectID
}

func (manager *metadataServiceManager) GetCustomAudience() string {
	return manager.customAudience
}

func (manager *metadataServiceManager) GetIdentityPool() string {
	return manager.identityPool
}

func (manager *metadataServiceManager) GetIdentityProvider() string {
	return manager.identityProvider
}
