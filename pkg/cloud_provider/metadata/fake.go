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
	"fmt"
	"os"
)

var envAPIMap = map[string]string{
	"prod":     "https://container.googleapis.com/",
	"staging":  "https://staging-container.sandbox.googleapis.com/",
	"staging2": "https://staging2-container.sandbox.googleapis.com/",
	"test":     "https://test-container.sandbox.googleapis.com/",
}

type fakeServiceManager struct {
	projectID        string
	identityPool     string
	identityProvider string
}

var _ Service = &fakeServiceManager{}

func NewFakeService(projectID, location, clusterName, gkeEnv string) (Service, error) {
	var gkeAPIEndpoint string
	if _, ok := envAPIMap[gkeEnv]; !ok {
		if gkeEnv == "sandbox" {
			gkeAPIEndpoint = os.Getenv("CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER")
		} else {
			return nil, fmt.Errorf("invalid GKE environment: %v", gkeEnv)
		}
	} else {
		gkeAPIEndpoint = envAPIMap[gkeEnv]
	}

	// TODO(amacaskill): Add support for non-GKE identity providers once we have e2e tests setup for OSS K8s.
	s := fakeServiceManager{
		projectID:    projectID,
		identityPool: projectID + ".svc.id.goog",
		identityProvider: fmt.Sprintf(
			"%sv1/projects/%s/locations/%s/clusters/%s",
			gkeAPIEndpoint,
			projectID,
			location,
			clusterName,
		),
	}

	return &s, nil
}

func (manager *fakeServiceManager) GetProjectID() string {
	return manager.projectID
}

func (manager *fakeServiceManager) GetIdentityPool() string {
	return manager.identityPool
}

func (manager *fakeServiceManager) GetIdentityProvider() string {
	return manager.identityProvider
}
