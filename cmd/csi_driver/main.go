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
	"os"
	"time"

	"github.com/containerd/containerd"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/auth"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/metadata"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	driver "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_driver"
	csimounter "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_mounter"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

var (
	containerRuntimeEndpoint = flag.String("container-runtime-endpoint", "/run/containerd/containerd.sock", "container runtime endpoint")
	endpoint                 = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	nodeID                   = flag.String("nodeid", "", "node id")
	runController            = flag.Bool("controller", false, "run controller service")
	runNode                  = flag.Bool("node", false, "run node service")
	kubeconfigPath           = flag.String("kubeconfig-path", "", "The kubeconfig path.")
	identityPool             = flag.String("identity-pool", "", "The Identity Pool to authenticate with GCS API.")
	identityProvider         = flag.String("identity-provider", "", "The Identity Provider to authenticate with GCS API.")

	// These are set at compile time.
	version = "unknown"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	containerdClient, err := containerd.New(*containerRuntimeEndpoint)
	if err != nil {
		klog.Fatal("Failed to create containerd client")
	}

	clientset, err := clientset.New(*kubeconfigPath)
	if err != nil {
		klog.Fatal("Failed to configure k8s client")
	}

	meta, err := metadata.NewMetadataService(*identityPool, *identityProvider, clientset)
	if err != nil {
		klog.Fatalf("Failed to set up metadata service: %v", err)
	}

	tm := auth.NewTokenManager(meta, clientset)
	ssm, err := storage.NewGCSServiceManager()
	if err != nil {
		klog.Fatalf("Failed to set up storage service manager: %v", err)
	}

	var mounter mount.Interface
	if *runNode {
		if *nodeID == "" {
			klog.Fatalf("NodeID cannot be empty for node service")
		}

		mounter, err = csimounter.New("")
		if err != nil {
			klog.Fatalf("Failed to prepare CSI mounter: %v", err)
		}

		// Monitor all the gcsfuse volumes,
		// send an exit notification to the sidecar container when gcsfuse is no longer needed.
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			for {
				<-ticker.C
				driver.CheckVolumesAndPutExitFile(containerdClient, clientset, mounter)
			}
		}()
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
	}

	gcfsDriver, err := driver.NewGCSDriver(config)
	if err != nil {
		klog.Fatalf("Failed to initialize Google Cloud Storage FUSE CSI Driver: %v", err)
	}

	klog.Infof("Running Google Cloud Storage FUSE CSI driver version %v", version)
	gcfsDriver.Run(*endpoint)

	os.Exit(0)
}
