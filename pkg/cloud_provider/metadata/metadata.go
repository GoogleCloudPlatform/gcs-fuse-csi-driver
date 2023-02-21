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
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/compute/metadata"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	appsv1 "k8s.io/api/apps/v1"
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

func NewMetadataService(clientset clientset.Interface) (Service, error) {
	projectID, err := metadata.ProjectID()
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %v", err)
	}

	ds, err := clientset.GetDaemonSet(context.TODO(), "kube-system", "gke-metadata-server")
	if err != nil {
		return nil, fmt.Errorf("failed to get gke-metadata-server DaemonSet spec: %v", err)
	}

	identityPool := fmt.Sprintf("%s.svc.id.goog", projectID)

	return &metadataServiceManager{
		projectID:        projectID,
		identityPool:     identityPool,
		identityProvider: getIdentityProvider(ds),
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

func getIdentityProvider(ds *appsv1.DaemonSet) string {
	for _, c := range ds.Spec.Template.Spec.Containers[0].Command {
		l := strings.Split(c, "=")
		if len(l) == 2 && l[0] == "--identity-provider" {
			return l[1]
		}
	}
	return ""
}
