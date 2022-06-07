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

package sanitytest

import (
	"os"
	"testing"

	sanity "github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/mount-utils"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/auth"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/storage"
	driver "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/csi_driver"
	proxyclient "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/client"
)

const (
	driverName    = "test-driver"
	driverVersion = "test-driver-version"
	nodeID        = "io.kubernetes.storage.mock"
	endpoint      = "unix:/tmp/csi.sock"
	mountPath     = "/tmp/csi/mount"
	tmpDir        = "/tmp/csi"
)

func TestSanity(t *testing.T) {
	// Set up temp working dir
	cleanUp := func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Fatalf("Failed to clean up sanity temp working dir %s: %v", tmpDir, err)
		}
	}
	err := os.MkdirAll(tmpDir, 0755)
	if err != nil {
		if err.Error() == "file exists" {
			cleanUp()
		} else {
			t.Fatalf("Failed to create sanity temp working dir %s: %v", tmpDir, err)
		}
	}
	defer cleanUp()

	// Set up driver and env
	driverConfig := &driver.GCSDriverConfig{
		Name:                  driverName,
		Version:               driverVersion,
		NodeID:                nodeID,
		RunController:         true,
		RunNode:               true,
		StorageServiceManager: storage.NewFakeServiceManager(),
		TokenManager:          auth.NewFakeTokenManager(),
		GCSFuseProxyClient:    proxyclient.NewFakeProxyClient(&mount.FakeMounter{MountPoints: []mount.MountPoint{}}),
	}

	gcfsDriver, err := driver.NewGCSDriver(driverConfig)
	if err != nil {
		t.Fatalf("Failed to initialize GCS CSI Driver: %v", err)
	}

	go func() {
		gcfsDriver.Run(endpoint)
	}()

	// Run test
	testConfig := sanity.TestConfig{
		TargetPath:      mountPath,
		Address:         endpoint,
		DialOptions:     []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
		IDGen:           &sanity.DefaultIDGenerator{},
		SecretsFile:     "secret.yaml",
		TestVolumeSize:  int64(1 * 1024 * 1024),
		IdempotentCount: 2,
	}
	sanity.Test(t, testConfig)
}
