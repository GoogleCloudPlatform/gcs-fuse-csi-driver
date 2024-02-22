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
	"time"

	"github.com/go-logr/logr"
	wh "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	port                    = flag.Int("port", 443, "The port that the webhook server serves at.")
	healthProbeBindAddress  = flag.String("health-probe-bind-address", ":8080", "The TCP address that the controller should bind to for serving health probes.")
	certDir                 = flag.String("cert-dir", "/etc/tls-certs", "The directory that contains the server key and certificate.")
	certName                = flag.String("cert-name", "cert.pem", "The server certificate name.")
	keyName                 = flag.String("key-name", "key.pem", "The server key name.")
	imagePullPolicy         = flag.String("sidecar-image-pull-policy", "IfNotPresent", "The default image pull policy for gcsfuse sidecar container.")
	cpuRequest              = flag.String("sidecar-cpu-request", "250m", "The default CPU request for gcsfuse sidecar container.")
	cpuLimit                = flag.String("sidecar-cpu-limit", "250m", "The default CPU limit for gcsfuse sidecar container.")
	memoryRequest           = flag.String("sidecar-memory-request", "256Mi", "The default memory request for gcsfuse sidecar container.")
	memoryLimit             = flag.String("sidecar-memory-limit", "256Mi", "The default memory limit for gcsfuse sidecar container.")
	ephemeralStorageRequest = flag.String("sidecar-ephemeral-storage-request", "5Gi", "The default ephemeral storage request for gcsfuse sidecar container.")
	ephemeralStorageLimit   = flag.String("sidecar-ephemeral-storage-limit", "5Gi", "The default ephemeral storage limit for gcsfuse sidecar container.")
	sidecarImage            = flag.String("sidecar-image", "", "The gcsfuse sidecar container image.")

	// These are set at compile time.
	version = "unknown"
)

const (
	resyncDuration = time.Minute * 30
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// Thanks to the PR https://github.com/solo-io/gloo/pull/8549
	// This line prevents controller-runtime from complaining about log.SetLogger never being called
	log.SetLogger(logr.New(log.NullLogSink{}))

	klog.Infof("Running Google Cloud Storage FUSE CSI driver admission webhook version %v, sidecar container image %v", version, *sidecarImage)

	// Load webhook config
	c := wh.LoadConfig(*sidecarImage, *imagePullPolicy, *cpuRequest, *cpuLimit, *memoryRequest, *memoryLimit, *ephemeralStorageRequest, *ephemeralStorageLimit)

	// Load config for manager, informers, listers
	kubeConfig := config.GetConfigOrDie()

	// Setup client
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		klog.Fatalf("Unable to get clientset: %v", err)
	}

	// Setup stop channel
	context := signals.SetupSignalHandler()

	// Setup Informer
	informerFactory := informers.NewSharedInformerFactory(client, resyncDuration)
	nodeLister := informerFactory.Core().V1().Nodes().Lister()

	informerFactory.Start(context.Done())
	informerFactory.WaitForCacheSync(context.Done())

	// Setup a Manager
	klog.Info("Setting up manager.")
	mgr, err := manager.New(kubeConfig, manager.Options{
		HealthProbeBindAddress: *healthProbeBindAddress,
		ReadinessEndpointName:  "/readyz",
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:     *port,
			CertDir:  *certDir,
			CertName: *certName,
			KeyName:  *keyName,
		}),
	})
	if err != nil {
		klog.Fatalf("Unable to set up overall controller manager: %v", err)
	}

	if err = mgr.AddReadyzCheck("readyz", func(_ *http.Request) error {
		return nil
	}); err != nil {
		klog.Errorf("Unable to set up readyz endpoint: %v", err)
	}

	// Setup Webhooks
	klog.Info("Setting up webhook server.")
	hookServer := mgr.GetWebhookServer()

	klog.Info("Registering webhooks to the webhook server.")
	hookServer.Register("/inject", &webhook.Admission{
		Handler: &wh.SidecarInjector{
			Client:     mgr.GetClient(),
			Config:     c,
			Decoder:    admission.NewDecoder(runtime.NewScheme()),
			NodeLister: nodeLister,
		},
	})

	klog.Info("Starting manager.")
	if err := mgr.Start(context); err != nil {
		klog.Fatalf("Unable to run manager: %v", err)
	}
}
