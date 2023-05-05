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

package metrics

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"k8s.io/component-base/metrics"
	"k8s.io/klog/v2"
)

const (
	// envGKEGCSFuseCSIVersion is an environment variable set in the Cloud Storage FUSE CSI driver webhook manifest
	// with the current version of the GKE component.
	envGKEGCSFuseCSIVersion = "GKE_GCSFUSECSI_VERSION"
)

// This metric is exposed only from the webhook component when GKE_GCSFUSECSI_VERSION env variable is set.
var gkeComponentVersion = metrics.NewGaugeVec(&metrics.GaugeOpts{
	Name: "component_version",
	Help: "Metric to expose the version of the gcsfusecsi GKE component.",
}, []string{"component_version"})

type Manager struct {
	registry metrics.KubeRegistry
}

func NewMetricsManager() *Manager {
	mm := &Manager{
		registry: metrics.NewKubeRegistry(),
	}

	return mm
}

// InitializeHTTPHandler sets up a server and creates a handler for metrics.
func (mm *Manager) InitializeHTTPHandler(address, path string) {
	mux := http.NewServeMux()
	mux.Handle(path, metrics.HandlerFor(
		mm.registry,
		metrics.HandlerOpts{
			ErrorHandling: metrics.ContinueOnError,
		}))
	server := &http.Server{
		Addr:              address,
		ReadHeaderTimeout: 3 * time.Second,
		Handler:           mux,
	}

	go func() {
		klog.Infof("Metric server listening at %q", address)
		if err := server.ListenAndServe(); err != nil {
			klog.Fatalf("Failed to start metric server at specified address (%q) and path (%q): %s", address, path, err)
		}
	}()
}

func (mm *Manager) EmitGKEComponentVersion() error {
	mm.registry.MustRegister(gkeComponentVersion)

	return mm.recordComponentVersionMetric()
}

func (mm *Manager) recordComponentVersionMetric() error {
	v := getEnvVar(envGKEGCSFuseCSIVersion)
	if v == "" {
		klog.V(2).Info("Skip emitting component version metric")

		return fmt.Errorf("failed to register GKE component version metric, env variable %v not defined", envGKEGCSFuseCSIVersion)
	}

	klog.Infof("Emit component_version metric with value %v", v)
	gkeComponentVersion.WithLabelValues(v).Set(1.0)

	return nil
}

func getEnvVar(envVarName string) string {
	v, ok := os.LookupEnv(envVarName)
	if !ok {
		klog.Warningf("%q env not set", envVarName)

		return ""
	}

	return v
}

func IsGKEComponentVersionAvailable() bool {
	return getEnvVar(envGKEGCSFuseCSIVersion) != ""
}
