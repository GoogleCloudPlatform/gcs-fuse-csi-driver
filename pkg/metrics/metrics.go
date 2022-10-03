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

package metrics

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/component-base/metrics"
	"k8s.io/klog"
)

const (
	// envGKEGCSCSIVersion is an environment variable set in the GCS CSI driver controller manifest
	// with the current version of the GKE component.
	envGKEGCSCSIVersion = "GKE_GCSCSI_VERSION"

	subSystem                   = "gcscsi"
	operationsLatencyMetricName = "operations_seconds"

	labelStatusCode = "grpc_status_code"
	labelMethodName = "method_name"
)

var (
	metricBuckets = []float64{.1, .25, .5, 1, 2.5, 5, 10, 15, 30, 60, 120, 300, 600}

	// This metric is exposed only from the controller driver component when GKE_GCSCSI_VERSION env variable is set.
	gkeComponentVersion = metrics.NewGaugeVec(&metrics.GaugeOpts{
		Name: "component_version",
		Help: "Metric to expose the version of the GCSCSI GKE component.",
	}, []string{"component_version"})

	operationSeconds = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Subsystem: subSystem,
			Name:      operationsLatencyMetricName,
			Buckets:   metricBuckets,
		},
		[]string{labelStatusCode, labelMethodName},
	)
)

type Manager struct {
	registry metrics.KubeRegistry
}

func NewMetricsManager() *Manager {
	mm := &Manager{
		registry: metrics.NewKubeRegistry(),
	}
	mm.registry.MustRegister(operationSeconds)
	return mm
}

func (mm *Manager) GetRegistry() metrics.KubeRegistry {
	return mm.registry
}

func (mm *Manager) registerComponentVersionMetric() {
	mm.registry.MustRegister(gkeComponentVersion)
}

func (mm *Manager) recordComponentVersionMetric() error {
	v := getEnvVar(envGKEGCSCSIVersion)
	if v == "" {
		klog.V(2).Info("Skip emitting component version metric")
		return fmt.Errorf("failed to register GKE component version metric, env variable %v not defined", envGKEGCSCSIVersion)
	}

	klog.Infof("Emit component_version metric with value %v", v)
	gkeComponentVersion.WithLabelValues(v).Set(1.0)
	return nil
}

func (mm *Manager) RecordOperationMetrics(opErr error, methodName string, opDuration time.Duration) {
	operationSeconds.WithLabelValues(getErrorCode(opErr), methodName).Observe(opDuration.Seconds())
}

func getErrorCode(err error) string {
	if err == nil {
		return codes.OK.String()
	}

	st, ok := status.FromError(err)
	if !ok {
		// This is not gRPC error. The operation must have failed before gRPC
		// method was called, otherwise we would get gRPC error.
		return "unknown-non-grpc"
	}

	return st.Code().String()
}

func (mm *Manager) EmitGKEComponentVersion() error {
	mm.registerComponentVersionMetric()
	if err := mm.recordComponentVersionMetric(); err != nil {
		return err
	}

	return nil
}

// Server represents any type that could serve HTTP requests for the metrics
// endpoint.
type Server interface {
	Handle(pattern string, handler http.Handler)
}

// RegisterToServer registers an HTTP handler for this metrics manager to the
// given server at the specified address/path.
func (mm *Manager) registerToServer(s Server, metricsPath string) {
	s.Handle(metricsPath, metrics.HandlerFor(
		mm.GetRegistry(),
		metrics.HandlerOpts{
			ErrorHandling: metrics.ContinueOnError}))
}

// InitializeHTTPHandler sets up a server and creates a handler for metrics.
func (mm *Manager) InitializeHTTPHandler(address, path string) {
	mux := http.NewServeMux()
	mm.registerToServer(mux, path)
	go func() {
		klog.Infof("Metric server listening at %q", address)
		if err := http.ListenAndServe(address, mux); err != nil {
			klog.Fatalf("Failed to start metric server at specified address (%q) and path (%q): %s", address, path, err)
		}
	}()
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
	return getEnvVar(envGKEGCSCSIVersion) != ""
}
