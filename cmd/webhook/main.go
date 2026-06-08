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

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/go-logr/logr"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	wh "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	port                                    = flag.Int("port", 443, "The port that the webhook server serves at.")
	healthProbeBindAddress                  = flag.String("health-probe-bind-address", ":8080", "The TCP address that the controller should bind to for serving health probes.")
	certDir                                 = flag.String("cert-dir", "/etc/tls-certs", "The directory that contains the server key and certificate.")
	certName                                = flag.String("cert-name", "cert.pem", "The server certificate name.")
	keyName                                 = flag.String("key-name", "key.pem", "The server key name.")
	imagePullPolicy                         = flag.String("sidecar-image-pull-policy", "IfNotPresent", "The default image pull policy for gcsfuse sidecar container.")
	cpuRequest                              = flag.String("sidecar-cpu-request", "250m", "The default CPU request for gcsfuse sidecar container.")
	cpuLimit                                = flag.String("sidecar-cpu-limit", "250m", "The default CPU limit for gcsfuse sidecar container.")
	memoryRequest                           = flag.String("sidecar-memory-request", "256Mi", "The default memory request for gcsfuse sidecar container.")
	memoryLimit                             = flag.String("sidecar-memory-limit", "256Mi", "The default memory limit for gcsfuse sidecar container.")
	ephemeralStorageRequest                 = flag.String("sidecar-ephemeral-storage-request", "5Gi", "The default ephemeral storage request for gcsfuse sidecar container.")
	ephemeralStorageLimit                   = flag.String("sidecar-ephemeral-storage-limit", "5Gi", "The default ephemeral storage limit for gcsfuse sidecar container.")
	sidecarImage                            = flag.String("sidecar-image", "", "The gcsfuse sidecar container image.")
	metadataSidecarImage                    = flag.String("metadata-sidecar-image", "", "The metadata prefetch sidecar container image.")
	injectSAVol                             = flag.Bool("should-inject-sa-vol", false, "Inject projected service account volume when true")
	enableGcsfuseProfiles                   = flag.Bool("enable-gcsfuse-profiles", false, "Enable gcsfuse profiles when true")
	enableSharedNodeMount                   = flag.Bool("enable-shared-node-mount", false, "Enable shared node mount feature when true")
	enableAutoGoMemLimit                    = flag.Bool("enable-auto-gomemlimit", false, "Automatically set GOMEMLIMIT to a percentage of the container's cgroup memory limit.")
	autoGoMemLimitRatio                     = flag.Float64("auto-gomemlimit-ratio", util.GoMemLimitCgroupPercentage, "The ratio of the container's cgroup memory limit to set as GOMEMLIMIT when enable-auto-gomemlimit is enabled.")
	metadataMemoryRequest                   = flag.String("metadata-sidecar-memory-request", "10Mi", "Flag to use default value for gcsfuse memory prefetch sidecar container memory request.")
	metadataMemoryLimit                     = flag.String("metadata-sidecar-memory-limit", "250Mi", "Flag to use default value for gcsfuse memory prefetch sidecar container memory limit.")
	metadataPrefetchCPURequest              = flag.String("metadata-sidecar-cpu-request", "10m", "The default cpu request for gcsfuse memory prefetch sidecar container cpu request.")
	metadataPrefetchCPULimit                = flag.String("metadata-sidecar-cpu-limit", "50m", "Flag to use default value for gcsfuse memory prefetch sidecar container cpu limit.")
	metadataPrefetchEphemeralStorageRequest = flag.String("metadata-sidecar-ephemeral-storage-request", "10Mi", "The default value for gcsfuse memory prefetch sidecar ephemeral storage request.")
	metadataPrefetchEphemeralStorageLimit   = flag.String("metadata-sidecar-ephemeral-storage-limit", "0", "The default value for gcsfuse memory prefetch sidecar ephemeral storage limit.")
	requireWIFCredentialConfigMap          = flag.Bool("require-wif-credential-configmap", false, "When true, the webhook denies the creation of pods with GCS FUSE volumes if the WIF credential ConfigMap annotation is absent, preventing fallback to the node's identity.")
	requireApplicationCredentials          = flag.Bool("require-application-credentials", false, "When true, the sidecar container is started with --require-application-credentials=true, causing it to refuse to start if GOOGLE_APPLICATION_CREDENTIALS is unset.")
	// These are set at compile time.
	webhookVersion = "unknown"
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

	if *enableAutoGoMemLimit {
		// Sets GOMEMLIMIT to a percentage of the cgroup limit.
		// If cgroup limits are unavailable, it falls back to doing nothing
		// (no limit).
		if _, err := memlimit.SetGoMemLimitWithOpts(
			memlimit.WithRatio(*autoGoMemLimitRatio),
			memlimit.WithProvider(memlimit.FromCgroup),
		); err != nil {
			// This can happen if the gcs-fuse-csi-driver-webhook container
			// memory limit is not set.
			klog.Warningf("Failed to automatically set GOMEMLIMIT: %v", err)
		}
	}

	klog.Infof("Running Google Cloud Storage FUSE CSI driver admission webhook version %v, sidecar container image %v", webhookVersion, *sidecarImage)

	// Load webhook config
	fuseSideCarConfig := wh.LoadConfig(*sidecarImage, *imagePullPolicy, *cpuRequest, *cpuLimit, *memoryRequest, *memoryLimit, *ephemeralStorageRequest, *ephemeralStorageLimit)
	fuseSideCarConfig.ShouldInjectSAVolume = *injectSAVol
	klog.Infof("Webhook should inject SA volume: %t", fuseSideCarConfig.ShouldInjectSAVolume)

	fuseSideCarConfig.EnableGcsfuseProfiles = *enableGcsfuseProfiles
	klog.Infof("Webhook should enable gcsfuse profiles: %t", fuseSideCarConfig.EnableGcsfuseProfiles)

	fuseSideCarConfig.EnableSharedNodeMount = *enableSharedNodeMount
	klog.Infof("Webhook should enable shared node mount: %t", fuseSideCarConfig.EnableSharedNodeMount)

	fuseSideCarConfig.RequireApplicationCredentials = *requireApplicationCredentials
	klog.Infof("Webhook should require application credentials: %t", fuseSideCarConfig.RequireApplicationCredentials)
	klog.Infof("Webhook should require WIF credential ConfigMap: %t", *requireWIFCredentialConfigMap)

	metadataPrefetchSideCarConfig := wh.LoadConfig(*metadataSidecarImage, *imagePullPolicy, *metadataPrefetchCPURequest, *metadataPrefetchCPULimit, *metadataMemoryRequest, *metadataMemoryLimit, *metadataPrefetchEphemeralStorageRequest, *metadataPrefetchEphemeralStorageLimit)
	metadataPrefetchSideCarConfig.EnableGcsfuseProfiles = *enableGcsfuseProfiles

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
			klog.Warningf(`Unable to parse server version "%s": %v`, v.String(), err)
		}
	}

	// Setup stop channel
	context := signals.SetupSignalHandler()

	// Setup Informer
	informerFactory := informers.NewSharedInformerFactory(client, resyncDuration)
	nodeLister := informerFactory.Core().V1().Nodes().Lister()
	pvcLister := informerFactory.Core().V1().PersistentVolumeClaims().Lister()
	pvLister := informerFactory.Core().V1().PersistentVolumes().Lister()
	scLister := informerFactory.Storage().V1().StorageClasses().Lister()
	podTemplateLister := informerFactory.Core().V1().PodTemplates().Lister()

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
			Client:                        mgr.GetClient(),
			Config:                        fuseSideCarConfig,
			MetadataPrefetchConfig:        metadataPrefetchSideCarConfig,
			Decoder:                       admission.NewDecoder(runtime.NewScheme()),
			NodeLister:                    nodeLister,
			PvLister:                      pvLister,
			PvcLister:                     pvcLister,
			ScLister:                      scLister,
			PodTemplateLister:             podTemplateLister,
			ServerVersion:                 serverVersion,
			K8SClient:                     client,
			RequireWIFCredentialConfigMap: *requireWIFCredentialConfigMap,
		},
	})

	klog.Info("Starting manager.")
	if err := mgr.Start(context); err != nil {
		klog.Fatalf("Unable to run manager: %v", err)
	}
}
