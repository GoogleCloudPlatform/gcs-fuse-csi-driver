

/*
Copyright 2018 The Kubernetes Authors.
Copyright 2022 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUTHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
	"net/http"
	"time"

	"github.com/go-logr/logr"
	wh "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	resyncDuration = time.Minute * 30
)

func RunWebhook(webhookVersion, sidecarImage, imagePullPolicy, cpuRequest, cpuLimit, memoryRequest, memoryLimit, ephemeralStorageRequest, ephemeralStorageLimit, metadataSidecarImage, metadataPrefetchCPURequest, metadataPrefetchCPULimit, metadataMemoryRequest, metadataMemoryLimit, metadataPrefetchEphemeralStorageRequest, metadataPrefetchEphemeralStorageLimit, certDir, certName, keyName, healthProbeBindAddress string, port int, injectSAVol, enableGcsfuseProfiles bool) {
	// Thanks to the PR https://github.com/solo-io/gloo/pull/8549
	// This line prevents controller-runtime from complaining about log.SetLogger never being called
	log.SetLogger(logr.New(log.NullLogSink{}))

	klog.Infof("Running Google Cloud Storage FUSE CSI driver admission webhook version %v, sidecar container image %v", webhookVersion, sidecarImage)

	// Load webhook config
	fuseSideCarConfig := wh.LoadConfig(sidecarImage, imagePullPolicy, cpuRequest, cpuLimit, memoryRequest, memoryLimit, ephemeralStorageRequest, ephemeralStorageLimit)
	fuseSideCarConfig.ShouldInjectSAVolume = injectSAVol
	klog.Infof("Webhook should inject SA volume: %t", fuseSideCarConfig.ShouldInjectSAVolume)

	fuseSideCarConfig.EnableGcsfuseProfiles = enableGcsfuseProfiles
	klog.Infof("Webhook should enable gcsfuse profiles: %t", fuseSideCarConfig.EnableGcsfuseProfiles)

	metadataPrefetchSideCarConfig := wh.LoadConfig(metadataSidecarImage, imagePullPolicy, metadataPrefetchCPURequest, metadataPrefetchCPULimit, metadataMemoryRequest, metadataMemoryLimit, metadataPrefetchEphemeralStorageRequest, metadataPrefetchEphemeralStorageLimit)

	// Load config for manager, informers, listers
	kubeConfig := config.GetConfigOrDie()

	// Setup client
	coreKubeConfig := rest.CopyConfig(kubeConfig)
	coreKubeConfig.ContentType = runtime.ContentTypeProtobuf
	client, err := kubernetes.NewForConfig(coreKubeConfig)
	if err != nil {
		klog.Warningf("Unable to get clientset: %v", err)
	}

	var serverVersion *version.Version
	// Get and format sever version.
	v, err := client.DiscoveryClient.ServerVersion()
	if err != nil || v == nil {
		klog.Warningf("Unable to get server version : %v", err)
	} else {
		serverVersion, err = version.ParseGeneric(v.String())
		if err != nil {
			klog.Warningf("Unable to parse server version \"%s\": %v", v.String(), err)
		}
	}

	// Setup stop channel
	context := signals.SetupSignalHandler()

	// Setup Informer
	informerFactory := informers.NewSharedInformerFactory(client, resyncDuration)
	nodeLister := informerFactory.Core().V1().Nodes().Lister()
	pvcLister := informerFactory.Core().V1().PersistentVolumeClaims().Lister()
	pvLister := informerFactory.Core().V1().PersistentVolumes().Lister()
	var scLister storagelisters.StorageClassLister
	if enableGcsfuseProfiles {
		scLister = informerFactory.Storage().V1().StorageClasses().Lister()
	}

	informerFactory.Start(context.Done())
	informerFactory.WaitForCacheSync(context.Done())

	// Setup a Manager
	klog.Info("Setting up manager.")
	mgr, err := manager.New(kubeConfig, manager.Options{
		HealthProbeBindAddress: healthProbeBindAddress,
		ReadinessEndpointName:  "/readyz",
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:     port,
			CertDir:  certDir,
			CertName: certName,
			KeyName:  keyName,
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
			Client:                 mgr.GetClient(),
			Config:                 fuseSideCarConfig,
			MetadataPrefetchConfig: metadataPrefetchSideCarConfig,
			Decoder:                admission.NewDecoder(runtime.NewScheme()),
			NodeLister:             nodeLister,
			PvLister:               pvLister,
			PvcLister:              pvcLister,
			ScLister:               scLister,
			ServerVersion:          serverVersion,
			K8SClient:              client,
		},
	})

	klog.Info("Starting manager.")
	if err := mgr.Start(context); err != nil {
		klog.Fatalf("Unable to run manager: %v", err)
	}
}
