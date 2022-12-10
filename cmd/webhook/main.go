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

package main

import (
	"flag"
	"net/http"

	wh "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	port                  = flag.Int("port", 443, "The port that the webhook server serves at.")
	certDir               = flag.String("cert-dir", "/etc/gcs-fuse-csi-driver-webhook/certs", "The directory that contains the server key and certificate.")
	certName              = flag.String("cert-name", "cert.pem", "The server certificate name.")
	keyName               = flag.String("key-name", "key.pem", "The server key name.")
	sidecarContainerImage = flag.String("container-image", "jiaxun/gcs-fuse-csi-driver-sidecar-mounter", "The gcsfuse sidecar container image name.")
	sidecarImageVersion   = flag.String("image-version", "v2.0.0", "The gcsfuse sidecar container image version.")
	cpuLimit              = flag.String("cpu-limit", "500m", "The default CPU limit for gcsfuse sidecar container.")
	memoryLimit           = flag.String("memory-limit", "300Mi", "The default memory limit for gcsfuse sidecar container.")
	ephemeralStorageLimit = flag.String("ephemeral-storage-limit", "5Gi", "The default ephemeral storage limit for gcsfuse sidecar container.")
	// This is set at compile time
	version = "unknown"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	klog.Infof("Running Google Cloud Storage FUSE CSI driver admission webhook version %v", version)

	// Load webhook config
	c, err := wh.LoadConfig(*sidecarContainerImage, *sidecarImageVersion, *cpuLimit, *memoryLimit, *ephemeralStorageLimit)
	if err != nil {
		klog.Fatalf("Unable to load webhook config: %v", err)
	}

	// Setup a Manager
	klog.Info("Setting up manager.")
	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{
		Port:                   *port,
		CertDir:                *certDir,
		MetricsBindAddress:     ":8081",
		HealthProbeBindAddress: ":8080",
		ReadinessEndpointName:  "/readyz",
		LivenessEndpointName:   "/healthz",
	})
	if err != nil {
		klog.Fatalf("Unable to set up overall controller manager: %v", err)
	}

	if err = mgr.AddReadyzCheck("readyz", func(req *http.Request) error {
		return nil
	}); err != nil {
		klog.Errorf("Unable to set up readyz endpoint: %v", err)
	}
	if err = mgr.AddHealthzCheck("healthz", func(req *http.Request) error {
		return nil
	}); err != nil {
		klog.Errorf("Unable to set up healthz endpoint: %v", err)
	}

	// Setup Webhooks
	klog.Info("Setting up webhook server.")
	hookServer := mgr.GetWebhookServer()
	hookServer.CertName = *certName
	hookServer.KeyName = *keyName

	klog.Info("Registering webhooks to the webhook server.")
	hookServer.Register("/inject", &webhook.Admission{
		Handler: &wh.SidecarInjector{
			Client: mgr.GetClient(),
			Config: c,
		},
	})

	klog.Info("Starting manager.")
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		klog.Fatalf("Unable to run manager: %v", err)
	}
}
