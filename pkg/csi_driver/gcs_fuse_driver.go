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

package driver

import (
	"errors"
	"fmt"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/auth"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

const DefaultName = "gcsfuse.csi.storage.gke.io"

type GCSDriverConfig struct {
	Name                  string // Driver name
	Version               string // Driver version
	NodeID                string // Node name
	RunController         bool   // Run CSI controller service
	RunNode               bool   // Run CSI node service
	StorageServiceManager storage.ServiceManager
	TokenManager          auth.TokenManager
	Mounter               mount.Interface
	K8sClients            clientset.Interface
	MetricsManager        metrics.Manager
}

type GCSDriver struct {
	config *GCSDriverConfig

	// CSI RPC servers
	ids csi.IdentityServer
	ns  csi.NodeServer
	cs  csi.ControllerServer

	// Plugin capabilities
	vcap  map[csi.VolumeCapability_AccessMode_Mode]*csi.VolumeCapability_AccessMode
	cscap []*csi.ControllerServiceCapability
	nscap []*csi.NodeServiceCapability
}

func NewGCSDriver(config *GCSDriverConfig) (*GCSDriver, error) {
	if config.Name == "" {
		return nil, errors.New("driver name missing")
	}
	if config.Version == "" {
		return nil, errors.New("driver version missing")
	}
	if !config.RunController && !config.RunNode {
		return nil, errors.New("must run at least one controller or node service")
	}

	driver := &GCSDriver{
		config: config,
		vcap:   map[csi.VolumeCapability_AccessMode_Mode]*csi.VolumeCapability_AccessMode{},
	}

	vcam := []csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	}
	driver.addVolumeCapabilityAccessModes(vcam)

	// Setup RPC servers
	driver.ids = newIdentityServer(driver)
	if config.RunNode {
		nscap := []csi.NodeServiceCapability_RPC_Type{
			csi.NodeServiceCapability_RPC_VOLUME_MOUNT_GROUP,
		}
		driver.ns = newNodeServer(driver, config.Mounter)
		driver.addNodeServiceCapabilities(nscap)
	}
	if config.RunController {
		csc := []csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		}
		driver.addControllerServiceCapabilities(csc)

		// Configure controller server
		driver.cs = newControllerServer(driver, config.StorageServiceManager)
	}

	return driver, nil
}

func (driver *GCSDriver) addVolumeCapabilityAccessModes(vc []csi.VolumeCapability_AccessMode_Mode) {
	for _, c := range vc {
		klog.Infof("Enabling volume access mode: %v", c.String())
		mode := NewVolumeCapabilityAccessMode(c)
		driver.vcap[mode.GetMode()] = mode
	}
}

func (driver *GCSDriver) validateVolumeCapabilities(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return errors.New("volume capabilities must be provided")
	}

	for _, c := range caps {
		if err := driver.validateVolumeCapability(c); err != nil {
			return err
		}
	}

	return nil
}

func (driver *GCSDriver) validateVolumeCapability(c *csi.VolumeCapability) error {
	if c == nil {
		return errors.New("volume capability must be provided")
	}

	// Validate access mode
	accessMode := c.GetAccessMode()
	if accessMode == nil {
		return errors.New("volume capability access mode not set")
	}
	if driver.vcap[accessMode.GetMode()] == nil {
		return fmt.Errorf("driver does not support access mode: %v", accessMode.GetMode().String())
	}

	// Validate access type
	accessType := c.GetAccessType()
	if accessType == nil {
		return errors.New("volume capability access type not set")
	}

	if c.GetMount() == nil {
		return errors.New("driver only supports mount access type volume capability")
	}

	return nil
}

func (driver *GCSDriver) addControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) {
	csc := []*csi.ControllerServiceCapability{}
	for _, c := range cl {
		klog.Infof("Enabling controller service capability: %v", c.String())
		csc = append(csc, NewControllerServiceCapability(c))
	}
	driver.cscap = csc
}

func (driver *GCSDriver) addNodeServiceCapabilities(nl []csi.NodeServiceCapability_RPC_Type) {
	nsc := []*csi.NodeServiceCapability{}
	for _, n := range nl {
		klog.Infof("Enabling node service capability: %v", n.String())
		nsc = append(nsc, NewNodeServiceCapability(n))
	}
	driver.nscap = nsc
}

func (driver *GCSDriver) ValidateControllerServiceRequest(c csi.ControllerServiceCapability_RPC_Type) error {
	if c == csi.ControllerServiceCapability_RPC_UNKNOWN {
		return nil
	}

	for _, cap := range driver.cscap {
		if c == cap.GetRpc().GetType() {
			return nil
		}
	}

	return status.Error(codes.InvalidArgument, "Invalid controller service request")
}

func (driver *GCSDriver) Run(endpoint string) {
	klog.Infof("Running driver: %v", driver.config.Name)

	s := NewNonBlockingGRPCServer()
	s.Start(endpoint, driver.ids, driver.cs, driver.ns)
	s.Wait()
}
