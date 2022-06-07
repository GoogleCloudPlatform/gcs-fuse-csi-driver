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

package main

import (
	"flag"
	"os"

	"k8s.io/klog"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/auth"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/metadata"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/cloud_provider/storage"
	driver "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/csi_driver"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/metrics"
	proxyclient "sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/proxy/client"
)

var (
	endpoint             = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	gcsfuseProxyEndpoint = flag.String("gcsfuse-proxy-endpoint", "unix:/tmp/gcsfuse-proxy.sock", "gcsfuse-proxy endpoint")
	nodeID               = flag.String("nodeid", "", "node id")
	runController        = flag.Bool("controller", false, "run controller service")
	runNode              = flag.Bool("node", false, "run node service")
	httpEndpoint         = flag.String("http-endpoint", "", "The TCP network address where the prometheus metrics endpoint will listen (example: `:8080`). The default is empty string, which means metrics endpoint is disabled.")
	metricsPath          = flag.String("metrics-path", "/metrics", "The HTTP path where prometheus metrics will be exposed. Default is `/metrics`.")
	kubeconfigPath       = flag.String("kubeconfig-path", "", "The kubeconfig path.")
	// This is set at compile time
	version = "unknown"
)

const driverName = "cloudstorage.csi.storage.gke.io"

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	meta, err := metadata.NewMetadataService()
	if err != nil {
		klog.Fatalf("Failed to set up metadata service: %v", err)
	}

	tm, err := auth.NewTokenManager(meta, *kubeconfigPath)
	if err != nil {
		klog.Fatalf("Failed to set up token manager: %v", err)
	}

	var mm *metrics.Manager
	var gcsfuseProxyClient proxyclient.ProxyClient
	var ssm storage.ServiceManager
	if *runController {
		if *httpEndpoint != "" && metrics.IsGKEComponentVersionAvailable() {
			mm = metrics.NewMetricsManager()
			mm.InitializeHTTPHandler(*httpEndpoint, *metricsPath)
			err = mm.EmitGKEComponentVersion()
			if err != nil {
				klog.Fatalf("Failed to emit GKE compoent version: %v", err)
			}
		}

		ssm, err = storage.NewGCSServiceManager()
		if err != nil {
			klog.Fatalf("Failed to set up storage service manager: %v", err)
		}
	} else {
		if *nodeID == "" {
			klog.Fatalf("nodeid cannot be empty for node service")
		}

		gcsfuseProxyClient, err = proxyclient.NewGCSFuseProxyClient(*gcsfuseProxyEndpoint, tm)
		if err != nil {
			klog.Fatalf("Failed to set up gcsfuse proxy client: %v", err)
		}
	}

	if err != nil {
		klog.Fatalf("Failed to initialize cloud provider: %v", err)
	}

	config := &driver.GCSDriverConfig{
		Name:                  driverName,
		Version:               version,
		NodeID:                *nodeID,
		RunController:         *runController,
		RunNode:               *runNode,
		GCSFuseProxyClient:    gcsfuseProxyClient,
		StorageServiceManager: ssm,
		TokenManager:          tm,
		Metrics:               mm,
	}

	gcfsDriver, err := driver.NewGCSDriver(config)
	if err != nil {
		klog.Fatalf("Failed to initialize Cloud Storage CSI Driver: %v", err)
	}

	klog.Infof("Running Google Cloud Storage CSI driver version %v", version)
	gcfsDriver.Run(*endpoint)

	os.Exit(0)
}
