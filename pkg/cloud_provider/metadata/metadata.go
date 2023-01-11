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

package metadata

import (
	"fmt"

	"cloud.google.com/go/compute/metadata"
)

type Service interface {
	GetProjectID() string
	GetIdentityPool() string
	GetIdentityProvider() string
}

type metadataServiceManager struct {
	projectID        string
	identityPool     string
	identityProvider string
}

var _ Service = &metadataServiceManager{}

func NewMetadataService() (Service, error) {
	projectID, err := metadata.ProjectID()
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %v", err)
	}

	clusterName, err := metadata.InstanceAttributeValue("cluster-name")
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster name: %v", err)
	}
	location, err := metadata.InstanceAttributeValue("cluster-location")
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster location: %v", err)
	}

	identityPool := fmt.Sprintf("%s.svc.id.goog", projectID)
	identityProvider := fmt.Sprintf(
		"https://container.googleapis.com/v1"+
			"/projects/%s/locations/%s/clusters/%s",
		projectID,
		location,
		clusterName,
	)

	return &metadataServiceManager{
		projectID:        projectID,
		identityPool:     identityPool,
		identityProvider: identityProvider,
	}, nil
}

func (manager *metadataServiceManager) GetProjectID() string {
	return manager.projectID
}

func (manager *metadataServiceManager) GetIdentityPool() string {
	return manager.identityPool
}

func (manager *metadataServiceManager) GetIdentityProvider() string {
	return manager.identityProvider
}
