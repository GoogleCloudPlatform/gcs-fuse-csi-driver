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

package driver

import (
	"fmt"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog"
	mount "k8s.io/mount-utils"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/auth"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/storage"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/metrics"
	proxyclient "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/client"
)

type GCSDriverConfig struct {
	Name                  string // Driver name
	Version               string // Driver version
	NodeID                string // Node name
	RunController         bool   // Run CSI controller service
	RunNode               bool   // Run CSI node service
	GCSFuseProxyClient    proxyclient.ProxyClient
	StorageServiceManager storage.ServiceManager
	TokenManager          auth.TokenManager
	Metrics               *metrics.Manager
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
		return nil, fmt.Errorf("driver name missing")
	}
	if config.Version == "" {
		return nil, fmt.Errorf("driver version missing")
	}
	if !config.RunController && !config.RunNode {
		return nil, fmt.Errorf("must run at least one controller or node service")
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
	err := driver.addVolumeCapabilityAccessModes(vcam)
	if err != nil {
		return nil, err
	}

	// Setup RPC servers
	driver.ids = newIdentityServer(driver)
	if config.RunNode {
		nscap := []csi.NodeServiceCapability_RPC_Type{}
		driver.ns = newNodeServer(driver, mount.New(""), config.GCSFuseProxyClient, config.TokenManager)
		err = driver.addNodeServiceCapabilities(nscap)
		if err != nil {
			return nil, err
		}
	}
	if config.RunController {
		csc := []csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		}
		err = driver.addControllerServiceCapabilities(csc)
		if err != nil {
			return nil, err
		}

		// Configure controller server
		driver.cs = newControllerServer(driver, config.StorageServiceManager, config.Metrics)
	}

	return driver, nil
}

func (driver *GCSDriver) addVolumeCapabilityAccessModes(vc []csi.VolumeCapability_AccessMode_Mode) error {
	for _, c := range vc {
		klog.Infof("Enabling volume access mode: %v", c.String())
		mode := NewVolumeCapabilityAccessMode(c)
		driver.vcap[mode.Mode] = mode
	}
	return nil
}

func (driver *GCSDriver) validateVolumeCapabilities(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return fmt.Errorf("volume capabilities must be provided")
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
		return fmt.Errorf("volume capability must be provided")
	}

	// Validate access mode
	accessMode := c.GetAccessMode()
	if accessMode == nil {
		return fmt.Errorf("volume capability access mode not set")
	}
	if driver.vcap[accessMode.Mode] == nil {
		return fmt.Errorf("driver does not support access mode: %v", accessMode.Mode.String())
	}

	// Validate access type
	accessType := c.GetAccessType()
	if accessType == nil {
		return fmt.Errorf("volume capability access type not set")
	}
	mountType := c.GetMount()
	if mountType == nil {
		return fmt.Errorf("driver only supports mount access type volume capability")
	}
	// if mountType.FsType != "" {
	// 	return fmt.Errorf("driver does not support fstype %v", mountType.FsType)
	// }
	// TODO: check if we want to whitelist/blacklist certain mount options
	return nil
}

func (driver *GCSDriver) addControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) error {
	var csc []*csi.ControllerServiceCapability
	for _, c := range cl {
		klog.Infof("Enabling controller service capability: %v", c.String())
		csc = append(csc, NewControllerServiceCapability(c))
	}
	driver.cscap = csc
	return nil
}

func (driver *GCSDriver) addNodeServiceCapabilities(nl []csi.NodeServiceCapability_RPC_Type) error {
	var nsc []*csi.NodeServiceCapability
	for _, n := range nl {
		klog.Infof("Enabling node service capability: %v", n.String())
		nsc = append(nsc, NewNodeServiceCapability(n))
	}
	driver.nscap = nsc
	return nil
}

func (driver *GCSDriver) ValidateControllerServiceRequest(c csi.ControllerServiceCapability_RPC_Type) error {
	if c == csi.ControllerServiceCapability_RPC_UNKNOWN {
		return nil
	}

	for _, cap := range driver.cscap {
		if c == cap.GetRpc().Type {
			return nil
		}
	}

	return status.Error(codes.InvalidArgument, "Invalid controller service request")
}

func (driver *GCSDriver) Run(endpoint string) {
	klog.Infof("Running driver: %v", driver.config.Name)

	//Start the nonblocking GRPC
	s := NewNonBlockingGRPCServer()
	s.Start(endpoint, driver.ids, driver.cs, driver.ns)
	s.Wait()
}
