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
	"context"
	"flag"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/profiler"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/auth"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/metadata"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	driver "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_driver"
	csimounter "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/csi_mounter"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/profiles"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

var (
	endpoint                       = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint.")
	nodeID                         = flag.String("nodeid", "", "Node id.")
	runController                  = flag.Bool("controller", false, "Run controller service.")
	runNode                        = flag.Bool("node", false, "Run node service.")
	kubeconfigPath                 = flag.String("kubeconfig-path", "", "The kubeconfig path.")
	kubeAPIQPS                     = flag.Float64("kube-api-qps", 5, "QPS to use while communicating with the kubernetes apiserver. Defaults to 5.0.")
	kubeAPIBurst                   = flag.Int("kube-api-burst", 10, "Burst to use while communicating with the kubernetes apiserver. Defaults to 10.")
	identityPool                   = flag.String("identity-pool", "", "The Identity Pool to authenticate with GCS API.")
	identityProvider               = flag.String("identity-provider", "", "The Identity Provider to authenticate with GCS API.")
	enableProfiling                = flag.Bool("enable-profiling", false, "Enable the golang pprof at port 6060.")
	informerResyncDurationSec      = flag.Int("informer-resync-duration-sec", 1800, "Informer resync duration in seconds.")
	retryIntervalStart             = flag.Duration("retry-interval-start", time.Second, "Initial retry interval for a failed PV processing operation. It doubles with each failure, up to retry-interval-max.")
	retryIntervalMax               = flag.Duration("retry-interval-max", 5*time.Minute, "Maximum retry interval for a failed PV processing operation.")
	fuseSocketDir                  = flag.String("fuse-socket-dir", "/sockets", "FUSE socket directory.")
	metricsEndpoint                = flag.String("metrics-endpoint", "", "The TCP network address where the Prometheus metrics endpoint will listen (example: `:8080`). The default is empty string, which means that the metrics endpoint is disabled.")
	maximumNumberOfCollectors      = flag.Int("max-metric-collectors", -1, "Maximum number of prometheus metric collectors exporting metrics at a time, less than 0 (e.g -1) means no limit.")
	disableAutoconfig              = flag.Bool("disable-autoconfig", false, "Disable gcsfuse's defaulting based on machine type.")
	wiNodeLabelCheck               = flag.Bool("wi-node-label-check", true, "Workload Identity node label check.")
	enableSidecarBucketAccessCheck = flag.Bool("enable-sidecar-bucket-access-check", false, "Enable bucket access check on sidecar, this does not disable bucket access check in node driver.")
	enableCloudProfilerForDriver   = flag.Bool("enable-cloud-profiler-for-driver", false, "Enable cloud profiler to collect analysis data.")

	// GCSFuse profiles flags.
	enableGCSFuseProfiles        = flag.Bool("enable-gcsfuse-profiles", false, "Enable the gcsfuse profiles feature.")
	datafluxParallelism          = flag.Int("dataflux-parallelism", 0, "Number of go routines for Dataflux lister. Defaults to 0 (10X number of available vCPUs).")
	datafluxBatchSize            = flag.Int("dataflux-batch-size", 25000, "Batch size for Dataflux lister. Defaults to 25000.")
	datafluxSkipDirectoryObjects = flag.Bool("dataflux-skip-directory-objects", false, "Set to true to skip Dataflux listing objects that include files with names ending in '/'.")

	// Leader election flags.
	leaderElection              = flag.Bool("leader-election", false, "Enables leader election for stateful driver.")
	leaderElectionNamespace     = flag.String("leader-election-namespace", "", "The namespace where the leader election resource exists. Should be set in deployments to use the pod's namespace.")
	leaderElectionLeaseDuration = flag.Duration("leader-election-lease-duration", 15*time.Second, "Duration, in seconds, that non-leader candidates will wait to force acquire leadership. Defaults to 15 seconds.")
	leaderElectionRenewDeadline = flag.Duration("leader-election-renew-deadline", 10*time.Second, "Duration, in seconds, that the acting leader will retry refreshing leadership before giving up. Defaults to 10 seconds.")
	leaderElectionRetryPeriod   = flag.Duration("leader-election-retry-period", 5*time.Second, "Duration, in seconds, the LeaderElector clients should wait between tries of actions. Defaults to 5 seconds.")

	// These are set at compile time.
	version = "unknown"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// ctx enables graceful shutdown. Application components should
	// respect ctx.Done() for cleanup.
	// The first SIGINT or SIGTERM signal cancels ctx to initiate shutdown.
	// A second such signal results in immediate termination via os.Exit(1).
	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		klog.Infof("Received signal %v, initiating shutdown...", sig)
		cancel()  // Trigger graceful shutdown
		<-sigChan // Wait for a second signal
		klog.Infof("Received second signal, exiting forcefully...")
		os.Exit(1) // Force exit
	}()

	if *enableCloudProfilerForDriver {
		cfg := profiler.Config{
			Service: "gcs-fuse-csi-driver",
		}
		if err := profiler.Start(cfg); err != nil {
			klog.Errorf("Errored while starting cloud profiler, got %v", err)
		} else {
			klog.Infof("Running cloud profiler on %s", cfg.Service)
		}

	}

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

	featureOptions := &driver.GCSDriverFeatureOptions{
		FeatureGCSFuseProfiles: &driver.FeatureGCSFuseProfiles{
			Enabled: *enableGCSFuseProfiles,
			ScannerConfig: &profiles.ScannerConfig{
				KubeAPIQPS:                  *kubeAPIQPS,
				KubeAPIBurst:                *kubeAPIBurst,
				ResyncPeriod:                time.Duration(*informerResyncDurationSec) * time.Second,
				KubeConfigPath:              *kubeconfigPath,
				RateLimiter:                 workqueue.NewTypedItemExponentialFailureRateLimiter[string](*retryIntervalStart, *retryIntervalMax),
				LeaderElection:              *leaderElection,
				LeaderElectionNamespace:     *leaderElectionNamespace,
				LeaderElectionLeaseDuration: *leaderElectionLeaseDuration,
				LeaderElectionRenewDeadline: *leaderElectionRenewDeadline,
				LeaderElectionRetryPeriod:   *leaderElectionRetryPeriod,
				DatafluxConfig: &profiles.DatafluxConfig{
					Parallelism:          *datafluxParallelism,
					BatchSize:            *datafluxBatchSize,
					SkipDirectoryObjects: *datafluxSkipDirectoryObjects,
				},
			},
		},
	}

	var mounter mount.Interface
	var mm metrics.Manager
	if *runNode {
		if *nodeID == "" {
			klog.Fatalf("NodeID cannot be empty for node service")
		}

		clientset.ConfigurePodLister(ctx, *nodeID)
		clientset.ConfigureNodeLister(ctx, *nodeID)

		if featureOptions.FeatureGCSFuseProfiles.Enabled {
			// Curently, only the gcsfuse profiles feature actually uses these listers.
			clientset.ConfigurePVLister(ctx)
			clientset.ConfigureSCLister(ctx)
		}

		mounter, err = csimounter.New("", *fuseSocketDir)
		if err != nil {
			klog.Fatalf("Failed to prepare CSI mounter: %v", err)
		}

		if *metricsEndpoint != "" {
			mm = metrics.NewMetricsManager(*metricsEndpoint, *fuseSocketDir, *maximumNumberOfCollectors, clientset)
			mm.InitializeHTTPHandler()
		}
	}

	config := &driver.GCSDriverConfig{
		Name:                           driver.DefaultName,
		Version:                        version,
		NodeID:                         *nodeID,
		RunController:                  *runController,
		RunNode:                        *runNode,
		StorageServiceManager:          ssm,
		TokenManager:                   tm,
		Mounter:                        mounter,
		K8sClients:                     clientset,
		MetricsManager:                 mm,
		DisableAutoconfig:              *disableAutoconfig,
		WINodeLabelCheck:               *wiNodeLabelCheck,
		EnableSidecarBucketAccessCheck: *enableSidecarBucketAccessCheck,
		FeatureOptions:                 featureOptions,
	}

	gcfsDriver, err := driver.NewGCSDriver(config)
	if err != nil {
		klog.Fatalf("Failed to initialize Google Cloud Storage FUSE CSI Driver: %v", err)
	}

	klog.Infof("Running Google Cloud Storage FUSE CSI driver version %v", version)
	gcfsDriver.Run(ctx, cancel, *endpoint)

	os.Exit(0)
}
