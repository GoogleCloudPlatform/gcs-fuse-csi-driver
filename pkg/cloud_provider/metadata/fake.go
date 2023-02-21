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

import "fmt"

var envAPIMap = map[string]string{
	"prod":     "container.googleapis.com",
	"staging":  "staging-container.sandbox.googleapis.com",
	"staging2": "staging2-container.sandbox.googleapis.com",
	"test":     "test-container.sandbox.googleapis.com",
}

type fakeServiceManager struct {
	projectID        string
	identityPool     string
	identityProvider string
}

var _ Service = &fakeServiceManager{}

func NewFakeService(projectID, location, clusterName, gkeEnv string) (Service, error) {
	if _, ok := envAPIMap[gkeEnv]; !ok {
		return nil, fmt.Errorf("invalid GKE environment: %v", gkeEnv)
	}
	s := fakeServiceManager{
		projectID:    projectID,
		identityPool: fmt.Sprintf("%s.svc.id.goog", projectID),
		identityProvider: fmt.Sprintf(
			"https://%s/v1"+
				"/projects/%s/locations/%s/clusters/%s",
			envAPIMap[gkeEnv],
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
