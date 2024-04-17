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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cloud.google.com/go/compute/metadata"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/klog/v2"
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

func NewMetadataService(identityPool, identityProvider string, clientset clientset.Interface) (Service, error) {
	projectID, err := metadata.ProjectID()
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	identityPool, identityProvider, err = gkeWorkloadIdentity(projectID, identityPool, identityProvider, clientset)
	if err != nil {
		err2 := err
		identityPool, identityProvider, err = fleetWorkloadIdentity()
		if err != nil {
			return nil, err2
		}
	}

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

func getIdentityProvider(ds *appsv1.DaemonSet) string {
	for _, c := range ds.Spec.Template.Spec.Containers[0].Command {
		l := strings.Split(c, "=")
		if len(l) == 2 && l[0] == "--identity-provider" {
			return l[1]
		}
	}

	return ""
}

// JSON key file types.
const (
	externalAccountKey = "external_account"
)

// credentialsFile is the unmarshalled representation of a credentials file.
type credentialsFile struct {
	Type string `json:"type"`
	// External Account fields
	Audience string `json:"audience"`
}

func gkeWorkloadIdentity(projectID string, identityPool string, identityProvider string, clientset clientset.Interface) (string, string, error) {
	if identityPool == "" {
		klog.Infof("got empty identityPool, constructing the identityPool using projectID")
		identityPool = projectID + ".svc.id.goog"
	}

	if identityProvider == "" {
		klog.Infof("got empty identityProvider, constructing the identityProvider using the gke-metadata-server flags")
		ds, err := clientset.GetDaemonSet(context.TODO(), "kube-system", "gke-metadata-server")
		if err != nil {
			return "", "", fmt.Errorf("failed to get gke-metadata-server DaemonSet spec: %w", err)
		}

		identityProvider = getIdentityProvider(ds)
	}
	return identityPool, identityProvider, nil
}

func fleetWorkloadIdentity() (string, string, error) {
	const envVar = "GOOGLE_APPLICATION_CREDENTIALS"
	var jsonData []byte
	var err error
	if filename := os.Getenv(envVar); filename != "" {
		jsonData, err = os.ReadFile(filepath.Clean(filename))
		if err != nil {
			return "", "", fmt.Errorf("google: error getting credentials using %v environment variable: %v", envVar, err)
		}
	}

	// Parse jsonData as one of the other supported credentials files.
	var f credentialsFile
	if err := json.Unmarshal(jsonData, &f); err != nil {
		return "", "", err
	}

	if f.Type != externalAccountKey {
		return "", "", fmt.Errorf("google: unexpected credentials type: %v, expected: %v", f.Type, externalAccountKey)
	}

	split := strings.SplitN(f.Audience, ":", 3)
	if split == nil || len(split) < 3 {
		return "", "", fmt.Errorf("google: unexpected audience value: %v", f.Audience)
	}
	idPool := split[1]
	idProvider := split[2]

	return idPool, idProvider, nil
}
