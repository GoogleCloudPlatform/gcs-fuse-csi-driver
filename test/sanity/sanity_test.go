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

package sanitytest

import (
	"os"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/auth"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	driver "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_driver"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	sanity "github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/mount-utils"
)

const (
	driverName    = "test-driver"
	driverVersion = "test-driver-version"
	nodeID        = "io.kubernetes.storage.mock"
	endpoint      = "unix:/tmp/csi.sock"
	mountPath     = "/tmp/var/lib/kubelet/pods/test-pod-id/volumes/kubernetes.io~csi/test-volume/mount"
	tmpDir        = "/tmp/var/lib/kubelet/pods/test-pod-id/volumes/kubernetes.io~csi/test-volume"
)

func TestSanity(t *testing.T) {
	t.Parallel()
	// Set up temp working dir
	cleanUp := func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Fatalf("Failed to clean up sanity temp working dir %s: %v", tmpDir, err)
		}
	}
	err := os.MkdirAll(tmpDir, 0o755)
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
		Mounter:               mount.NewFakeMounter([]mount.MountPoint{}),
		K8sClients:            &clientset.FakeClientset{},
		MetricsManager:        &metrics.FakeMetricsManager{},
	}

	gcfsDriver, err := driver.NewGCSDriver(driverConfig)
	if err != nil {
		t.Fatalf("Failed to initialize Cloud Storage FUSE CSI Driver: %v", err)
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
