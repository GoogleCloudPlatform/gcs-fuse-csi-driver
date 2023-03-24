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

package e2etest

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/google/uuid"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/auth"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/metadata"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	driver "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_driver"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	e2eframework "k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
)

type GCSFuseCSITestDriver struct {
	driverInfo            storageframework.DriverInfo
	clientset             clientset.Interface
	meta                  metadata.Service
	storageServiceManager storage.ServiceManager
	volumeStore           []*gcsVolume
}

type gcsVolume struct {
	driver                  *GCSFuseCSITestDriver
	bucketName              string
	serviceAccountNamespace string
	mountOptions            string
	shared                  bool
	readOnly                bool
}

// InitGCSFuseCSITestDriver returns GCSFuseCSITestDriver that implements TestDriver interface.
func InitGCSFuseCSITestDriver(c clientset.Interface, m metadata.Service) storageframework.TestDriver {
	ssm, err := storage.NewGCSServiceManager()
	if err != nil {
		e2eframework.Failf("Failed to set up storage service manager: %v", err)
	}

	return &GCSFuseCSITestDriver{
		driverInfo: storageframework.DriverInfo{
			Name:        driver.DefaultName,
			MaxFileSize: storageframework.FileSizeLarge,
			SupportedFsType: sets.NewString(
				"", // Default fsType
			),
			Capabilities: map[storageframework.Capability]bool{
				storageframework.CapPersistence: true,
				storageframework.CapExec:        true,
			},
		},
		clientset:             c,
		meta:                  m,
		storageServiceManager: ssm,
		volumeStore:           []*gcsVolume{},
	}
}

var (
	_ storageframework.TestDriver                     = &GCSFuseCSITestDriver{}
	_ storageframework.PreprovisionedVolumeTestDriver = &GCSFuseCSITestDriver{}
	_ storageframework.PreprovisionedPVTestDriver     = &GCSFuseCSITestDriver{}
	_ storageframework.EphemeralTestDriver            = &GCSFuseCSITestDriver{}
	_ storageframework.DynamicPVTestDriver            = &GCSFuseCSITestDriver{}
)

func (n *GCSFuseCSITestDriver) GetDriverInfo() *storageframework.DriverInfo {
	return &n.driverInfo
}

func (n *GCSFuseCSITestDriver) SkipUnsupportedTest(pattern storageframework.TestPattern) {
	if pattern.VolType == storageframework.InlineVolume || pattern.VolType == storageframework.GenericEphemeralVolume {
		e2eskipper.Skipf("GCS CSI Fuse CSI Driver does not support %s -- skipping", pattern.VolType)
	}
}

func (n *GCSFuseCSITestDriver) PrepareTest(f *e2eframework.Framework) *storageframework.PerTestConfig {
	testGCPProjectIAMPolicyBinding := specs.NewTestGCPProjectIAMPolicyBinding(
		n.meta.GetProjectID(),
		fmt.Sprintf("serviceAccount:%v.svc.id.goog[%v/%v]", n.meta.GetProjectID(), f.Namespace.Name, specs.K8sServiceAccountName),
		"roles/storage.admin",
		"",
	)
	testGCPProjectIAMPolicyBinding.Create()

	testK8sSA := specs.NewTestKubernetesServiceAccount(f.ClientSet, f.Namespace, specs.K8sServiceAccountName, "")
	testK8sSA.Create()

	testSecret := specs.NewTestSecret(f.ClientSet, f.Namespace, specs.K8sSecretName, map[string]string{
		"projectID":               n.meta.GetProjectID(),
		"serviceAccountName":      specs.K8sServiceAccountName,
		"serviceAccountNamespace": f.Namespace.Name,
	})
	testSecret.Create()

	config := &storageframework.PerTestConfig{
		Driver:    n,
		Framework: f,
	}

	ginkgo.DeferCleanup(func() {
		for _, v := range n.volumeStore {
			v.driver.deleteBucket(v.serviceAccountNamespace, v.bucketName)
		}
		n.volumeStore = []*gcsVolume{}

		testSecret.Cleanup()
		testK8sSA.Cleanup()
		testGCPProjectIAMPolicyBinding.Cleanup()
	})

	return config
}

func (n *GCSFuseCSITestDriver) CreateVolume(config *storageframework.PerTestConfig, volType storageframework.TestVolType) storageframework.TestVolume {
	switch volType {
	case storageframework.PreprovisionedPV:
		bucketName := specs.FakeVolumePrefix
		if config.Prefix != specs.FakeVolumePrefix {
			bucketName = n.createBucket(config.Framework.Namespace.Name)
		}

		mountOptions := "debug_gcs,debug_fuse,debug_fs"
		switch config.Prefix {
		case specs.NonRootVolumePrefix:
			mountOptions += ",uid=1001,gid=3003"
		case specs.InvalidMountOptionsVolumePrefix:
			mountOptions += ",invalid-option"
		case specs.ImplicitDirsVolumePrefix:
			createImplicitDir(bucketName)
			mountOptions += ",implicit-dirs"
		}

		return &gcsVolume{
			driver:                  n,
			bucketName:              bucketName,
			serviceAccountNamespace: config.Framework.Namespace.Name,
			mountOptions:            mountOptions,
		}
	case storageframework.DynamicPV:
		// Do nothing
	default:
		e2eframework.Failf("Unsupported volType:%v is specified", volType)
	}

	return nil
}

func (v *gcsVolume) DeleteVolume() {
	v.driver.deleteBucket(v.serviceAccountNamespace, v.bucketName)
}

func (n *GCSFuseCSITestDriver) GetPersistentVolumeSource(readOnly bool, _ string, volume storageframework.TestVolume) (*v1.PersistentVolumeSource, *v1.VolumeNodeAffinity) {
	nv, _ := volume.(*gcsVolume)
	va := map[string]string{"mountOptions": nv.mountOptions}

	return &v1.PersistentVolumeSource{
		CSI: &v1.CSIPersistentVolumeSource{
			Driver:           n.driverInfo.Name,
			VolumeHandle:     nv.bucketName,
			VolumeAttributes: va,
			ReadOnly:         readOnly,
		},
	}, nil
}

func (n *GCSFuseCSITestDriver) GetVolume(config *storageframework.PerTestConfig, volumeNumber int) (map[string]string, bool, bool) {
	if config.Prefix == specs.FakeVolumePrefix {
		return map[string]string{
			"bucketName": specs.FakeVolumePrefix,
		}, true, false
	}

	for len(n.volumeStore) <= volumeNumber {
		bucketName := n.createBucket(config.Framework.Namespace.Name)
		n.volumeStore = append(n.volumeStore, &gcsVolume{
			driver:                  n,
			bucketName:              bucketName,
			serviceAccountNamespace: config.Framework.Namespace.Name,
			shared:                  true,
			readOnly:                false,
		})
	}

	volume := n.volumeStore[volumeNumber]
	attributes := map[string]string{
		"bucketName":   volume.bucketName,
		"mountOptions": "debug_gcs,debug_fuse,debug_fs",
	}
	switch config.Prefix {
	case specs.NonRootVolumePrefix:
		attributes["mountOptions"] += ",uid=1001,gid=3003"
	case specs.InvalidMountOptionsVolumePrefix:
		attributes["mountOptions"] += ",invalid-option"
	case specs.ImplicitDirsVolumePrefix:
		createImplicitDir(volume.bucketName)
		attributes["mountOptions"] += ",implicit-dirs"
	}

	return attributes, volume.shared, volume.readOnly
}

func (n *GCSFuseCSITestDriver) GetCSIDriverName(_ *storageframework.PerTestConfig) string {
	return n.driverInfo.Name
}

func (n *GCSFuseCSITestDriver) GetDynamicProvisionStorageClass(config *storageframework.PerTestConfig, _ string) *storagev1.StorageClass {
	parameters := map[string]string{
		"csi.storage.k8s.io/provisioner-secret-name":      specs.K8sSecretName,
		"csi.storage.k8s.io/provisioner-secret-namespace": "${pvc.namespace}",
	}
	generateName := "gcsfuse-csi-dynamic-test-sc-"
	defaultBindingMode := storagev1.VolumeBindingWaitForFirstConsumer

	mountOptions := []string{"debug_gcs", "debug_fuse", "debug_fs"}
	switch config.Prefix {
	case specs.NonRootVolumePrefix:
		mountOptions = append(mountOptions, "uid=1001", "gid=3003")
	case specs.InvalidMountOptionsVolumePrefix:
		mountOptions = append(mountOptions, "invalid-option")
	}

	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
		},
		Provisioner:       n.driverInfo.Name,
		MountOptions:      mountOptions,
		Parameters:        parameters,
		VolumeBindingMode: &defaultBindingMode,
	}
}

// prepareStorageService prepares the GCS Storage Service using a given Kubernetes service account
// There is an assumption that before this function is called, the Kubernetes service account is already created in the namespace.
func (n *GCSFuseCSITestDriver) prepareStorageService(ctx context.Context, serviceAccountNamespace string) (storage.Service, error) {
	tm := auth.NewTokenManager(n.meta, n.clientset)
	ts, err := tm.GetTokenSourceFromK8sServiceAccount(serviceAccountNamespace, specs.K8sServiceAccountName, "")
	if err != nil {
		return nil, fmt.Errorf("token manager failed to get token source: %w", err)
	}

	storageService, err := n.storageServiceManager.SetupService(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("storage service manager failed to setup service: %w", err)
	}

	return storageService, nil
}

// createBucket creates a GCS bucket.
func (n *GCSFuseCSITestDriver) createBucket(serviceAccountNamespace string) string {
	ctx := context.Background()
	storageService, err := n.prepareStorageService(ctx, serviceAccountNamespace)
	if err != nil {
		e2eframework.Failf("Failed to prepare storage service: %v", err)
	}
	// the GCS bucket name is always new and unique,
	// so there is no need to check if the bucket already exists
	newBucket := &storage.ServiceBucket{
		Project: n.meta.GetProjectID(),
		Name:    uuid.NewString(),
	}
	bucket, err := storageService.CreateBucket(ctx, newBucket)
	if err != nil {
		e2eframework.Failf("Failed to create a new GCS bucket: %v", err)
	}

	return bucket.Name
}

// deleteBucket deletes the GCS bucket.
func (n *GCSFuseCSITestDriver) deleteBucket(serviceAccountNamespace, bucketName string) {
	ctx := context.Background()
	storageService, err := n.prepareStorageService(ctx, serviceAccountNamespace)
	if err != nil {
		e2eframework.Failf("Failed to prepare storage service: %v", err)
	}
	err = storageService.DeleteBucket(ctx, &storage.ServiceBucket{Name: bucketName})
	if err != nil {
		e2eframework.Failf("Failed to delete the GCS bucket: %v", err)
	}
}

func createImplicitDir(bucketName string) {
	f, err := os.Create(bucketName)
	if err != nil {
		e2eframework.Failf("Failed to create an empty data file: %v", err)
	}
	f.Close()
	defer func() {
		err = os.Remove(bucketName)
		if err != nil {
			e2eframework.Failf("Failed to delete the empty data file: %v", err)
		}
	}()

	//nolint:gosec
	cmd := exec.Command("gsutil", "cp", bucketName, fmt.Sprintf("gs://%v/%v/", bucketName, specs.ImplicitDirsPath))
	if err := cmd.Run(); err != nil {
		e2eframework.Failf("Failed to create a implicit dir in GCS bucket: %v", err)
	}
}
