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

package main

import (
	"flag"
	"net/http"
	"net/http/pprof"
	"os"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/auth"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/metadata"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	driver "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_driver"
	csimounter "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_mounter"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

var (
	endpoint                  = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	nodeID                    = flag.String("nodeid", "", "node id")
	runController             = flag.Bool("controller", false, "run controller service")
	runNode                   = flag.Bool("node", false, "run node service")
	kubeconfigPath            = flag.String("kubeconfig-path", "", "The kubeconfig path.")
	identityPool              = flag.String("identity-pool", "", "The Identity Pool to authenticate with GCS API.")
	identityProvider          = flag.String("identity-provider", "", "The Identity Provider to authenticate with GCS API.")
	enableProfiling           = flag.Bool("enable-profiling", false, "enable the golang pprof at port 6060")
	informerResyncDurationSec = flag.Int("informer-resync-duration-sec", 1800, "informer resync duration in seconds")
	metricsEndpoint           = flag.String("metrics-endpoint", "", "The TCP network address where the prometheus metrics endpoint will listen (example: `:8080`). The default is empty string, which means metrics endpoint is disabled.")

	// These are set at compile time.
	version = "unknown"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	if *enableProfiling {
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

		go func() {
			server := &http.Server{
				Addr:         "localhost:6060",
				Handler:      mux,
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 10 * time.Second,
			}
			if err := server.ListenAndServe(); err != nil {
				klog.Fatalf("Failed to start the golang pprof server: %v", err)
			}
		}()
	}

	clientset, err := clientset.New(*kubeconfigPath, *informerResyncDurationSec)
	if err != nil {
		klog.Fatalf("Failed to configure k8s client: %v", err)
	}

	meta, err := metadata.NewMetadataService(*identityPool, *identityProvider)
	if err != nil {
		klog.Fatalf("Failed to set up metadata service: %v", err)
	}

	tm := auth.NewTokenManager(meta, clientset)
	ssm, err := storage.NewGCSServiceManager()
	if err != nil {
		klog.Fatalf("Failed to set up storage service manager: %v", err)
	}

	var mounter mount.Interface
	var mm metrics.Manager
	if *runNode {
		if *nodeID == "" {
			klog.Fatalf("NodeID cannot be empty for node service")
		}

		clientset.ConfigurePodLister(*nodeID)

		mounter, err = csimounter.New("")
		if err != nil {
			klog.Fatalf("Failed to prepare CSI mounter: %v", err)
		}

		if *metricsEndpoint != "" {
			mm = metrics.NewMetricsManager(*metricsEndpoint)
			mm.InitializeHTTPHandler()
		}
	}

	config := &driver.GCSDriverConfig{
		Name:                  driver.DefaultName,
		Version:               version,
		NodeID:                *nodeID,
		RunController:         *runController,
		RunNode:               *runNode,
		StorageServiceManager: ssm,
		TokenManager:          tm,
		Mounter:               mounter,
		K8sClients:            clientset,
		MetricsManager:        mm,
	}

	gcfsDriver, err := driver.NewGCSDriver(config)
	if err != nil {
		klog.Fatalf("Failed to initialize Google Cloud Storage FUSE CSI Driver: %v", err)
	}

	klog.Infof("Running Google Cloud Storage FUSE CSI driver version %v", version)
	gcfsDriver.Run(*endpoint)

	os.Exit(0)
}
