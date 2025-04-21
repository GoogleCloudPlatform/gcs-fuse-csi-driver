/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	SocketName = "metrics.sock"

	metricsPath = "/metrics"
	unixURL     = "http://unix/"
)

type Manager interface {
	InitializeHTTPHandler()
	RegisterMetricsCollector(targetPath, podNamespace, podName, bucketName string)
	UnregisterMetricsCollector(targetPath string)
}

type manager struct {
	registry        *prometheus.Registry
	metricsEndpoint string
	fuseSocketDir   string
	clientset       clientset.Interface

	maximumNumberOfCollectors   int
	volumePublishPathRegistered sets.Set[string]
	mutex                       sync.Mutex
}

func NewMetricsManager(metricsEndpoint, fuseSocketDir string, maximumNumberOfCollectors int, clientset clientset.Interface) Manager {
	mm := &manager{
		registry:                    prometheus.NewRegistry(),
		metricsEndpoint:             metricsEndpoint,
		fuseSocketDir:               fuseSocketDir,
		clientset:                   clientset,
		volumePublishPathRegistered: sets.Set[string]{},
		maximumNumberOfCollectors:   maximumNumberOfCollectors,
		mutex:                       sync.Mutex{},
	}

	return mm
}

// InitializeHTTPHandler sets up a server and creates a handler for metrics.
func (mm *manager) InitializeHTTPHandler() {
	mux := http.NewServeMux()
	mux.HandleFunc(metricsPath, promhttp.HandlerFor(mm.registry, promhttp.HandlerOpts{}).ServeHTTP)

	// Configure the http server and start it.
	metricServer := &http.Server{
		Addr:           mm.metricsEndpoint,
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		klog.Infof("metric server listening at %q", mm.metricsEndpoint)
		if err := metricServer.ListenAndServe(); err != nil {
			klog.Errorf("failed to start metric server at specified endpoint %q and path %q: %v", mm.metricsEndpoint, metricsPath, err)
		}
	}()
}

// RegisterMetricsCollector registers the metrics collector. It is idempotent to register the same collector.
func (mm *manager) RegisterMetricsCollector(targetPath, podNamespace, podName, bucketName string) {
	emptyDirBasePath, err := util.PrepareEmptyDir(targetPath, false)
	if err != nil {
		klog.Errorf("failed to register metrics collector for pod %v/%v, bucket %q: %v", podNamespace, podName, bucketName, err)

		return
	}

	socketBasePath := util.GetSocketBasePath(targetPath, mm.fuseSocketDir)
	if err := os.Symlink(emptyDirBasePath, socketBasePath); err != nil && !os.IsExist(err) {
		klog.Errorf("failed to create symbolic link to path %q: %v", socketBasePath, err)

		return
	}

	podUID, volumeName, _ := util.ParsePodIDVolumeFromTargetpath(targetPath)
	c := NewMetricsCollector(socketBasePath, emptyDirBasePath, podNamespace, podName, podUID, volumeName, map[string]string{
		"pod_name":       podName,
		"namespace_name": podNamespace,
		"volume_name":    volumeName,
		"bucket_name":    bucketName,
		"pod_uid":        podUID,
	}, mm.clientset)

	// Lock the number of registered collectors while we attempt to register a new collector.
	mm.mutex.Lock()
	defer mm.mutex.Unlock()

	if mm.maximumNumberOfCollectors == 0 {
		klog.Infof("could not register metrics collector: podUID: %s, volume: %s. metrics collector limit is set to zero.", podUID, bucketName)

		return
	}

	// Check if we need to register collector. We register a collector when the following are met:
	// 1. There is space on the metrics pipeline for the collector to be registered.
	// 2. The metrics collector has not previously been registered.
	if mm.maximumNumberOfCollectors > 0 {
		// If volume is already registered, do not register again. This flow can get triggered
		//  since CSI driver has republishVolume capability.
		if mm.volumePublishPathRegistered.Has(targetPath) {
			return
		}
		// If collector hasn't been registered and there's no space left, log a warning.
		if mm.volumePublishPathRegistered.Len() >= mm.maximumNumberOfCollectors {
			klog.V(6).Infof("could not register a metrics collector: podUID: %s, volume: %s. there's already %d collectors registered.", podUID, bucketName, mm.volumePublishPathRegistered.Len())

			return
		}
	}

	// Attempt to register new metrics collector and record success.
	err = mm.registry.Register(c)
	if err != nil {
		if !strings.Contains(err.Error(), prometheus.AlreadyRegisteredError{}.Error()) {
			klog.Errorf("failed to register metrics collector for pod  %v/%v, volume %q, bucket %q: %v", podNamespace, podName, volumeName, bucketName, err)
		}
	} else {
		mm.volumePublishPathRegistered.Insert(targetPath)
		klog.Infof("successfully registered a new metrics collector: podUID: %s, volume: %s. there's %d collectors registered.", podUID, bucketName, mm.volumePublishPathRegistered.Len())
	}
}

// UnregisterMetricsCollector unregisters the metrics collector. It is idempotent to unregister the same collector.
func (mm *manager) UnregisterMetricsCollector(targetPath string) {
	podUID, volumeName, _ := util.ParsePodIDVolumeFromTargetpath(targetPath)

	// metricsCollector uses a hash of pod UID and volume name as an identifier.
	c := NewMetricsCollector("", "", "", "", podUID, volumeName, nil, nil)

	// Lock the number of registered collectors while we attempt to unregister a collector.
	mm.mutex.Lock()
	defer mm.mutex.Unlock()

	if ok := mm.registry.Unregister(c); !ok {
		klog.Infof("Unregister metrics collector for targetPath %q is not needed since the collector is not registered", targetPath)
	} else {
		mm.volumePublishPathRegistered.Delete(targetPath)
		klog.Infof("successfully unregistered a metrics collector: podUID: %s, volume: %s. there's %d collectors registered.", podUID, volumeName, mm.volumePublishPathRegistered.Len())
	}
}

type metricsCollector struct {
	emptyDirBasePath string
	constLabels      map[string]string
	namespace        string
	podName          string
	podUID           string
	volumeName       string
	httpClient       *http.Client
	clientset        clientset.Interface
}

// NewMetricsCollector returns a new Collector exposing metrics read from the give path.
func NewMetricsCollector(socketBasePath, emptyDirBasePath, namespace, podName, podUID, volumeName string, labels map[string]string, clientset clientset.Interface) prometheus.Collector {
	c := &metricsCollector{
		emptyDirBasePath: emptyDirBasePath,
		constLabels:      labels,
		namespace:        namespace,
		podName:          podName,
		podUID:           podUID,
		volumeName:       volumeName,
		clientset:        clientset,
	}

	// Creating a new HTTP client that is configured to make HTTP requests over a unix domain socket.
	c.httpClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", filepath.Join(socketBasePath, SocketName))
			},
		},
	}

	return c
}

// Describe emits the description of metrics.
// Prometheus Registry relies on this func to identify collectors.
func (c *metricsCollector) Describe(ch chan<- *prometheus.Desc) {
	// Collector id is a hash of the values of the ConstLabels and fqName.
	ch <- prometheus.NewDesc("gke_gcsfuse_csi_metric", "GKE GCSFuse CSI metric.", nil, map[string]string{"pod_uid": c.podUID, "volume_name": c.volumeName})
}

// Collect scrapes metrics from the sidecar and emits metrics.
func (c *metricsCollector) Collect(ch chan<- prometheus.Metric) {
	pod, err := c.clientset.GetPod(c.namespace, c.podName)
	if err != nil || pod == nil || pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		klog.V(6).Infof("pod %v/%v does not exist, skip metrics scraping", c.namespace, c.podName)

		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, unixURL, nil)
	if err != nil {
		klog.Errorf("failed to create scrape metrics request: %v", err)

		return
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		klog.Errorf("failed to scrape metrics: %v", err)

		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		klog.Errorf("unexpected HTTP status: %v", resp.Status)

		return
	}

	families, err := ProcessMetricsData(resp.Body)
	if err != nil {
		klog.Errorf("failed to process metrics data: %v", err)

		return
	}

	for _, mf := range families {
		c.emitMetricFamily(mf, ch)
	}
}

// ProcessMetricsData processes metrics that follow Prometheus text format: https://prometheus.io/docs/instrumenting/exposition_formats/,
// returning its MetricFamily.
func ProcessMetricsData(metricsReader io.Reader) (map[string]*dto.MetricFamily, error) {
	var parser expfmt.TextParser
	metricFamilies, err := parser.TextToMetricFamilies(metricsReader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metrics: %w", err)
	}

	return metricFamilies, nil
}

// emitMetricFamily iterates MetricFamily, converts metricFamily.Metric to prometheus.Metric, and emits the metric via the given chan.
func (c *metricsCollector) emitMetricFamily(metricFamily *dto.MetricFamily, ch chan<- prometheus.Metric) {
	var valType prometheus.ValueType
	var val float64

	for _, metric := range metricFamily.GetMetric() {
		var LabelNames []string
		var LabelValues []string
		for _, label := range metric.GetLabel() {
			LabelNames = append(LabelNames, label.GetName())
			LabelValues = append(LabelValues, label.GetValue())
		}

		for n, v := range c.constLabels {
			LabelNames = append(LabelNames, n)
			LabelValues = append(LabelValues, v)
		}

		emitNewConstMetric := func() {
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc(
					metricFamily.GetName(),
					metricFamily.GetHelp(),
					LabelNames, nil,
				),
				valType, val, LabelValues...,
			)
		}

		metricType := metricFamily.GetType()
		switch metricType {
		case dto.MetricType_COUNTER:
			valType = prometheus.CounterValue
			val = metric.GetCounter().GetValue()
			emitNewConstMetric()

		case dto.MetricType_GAUGE:
			valType = prometheus.GaugeValue
			val = metric.GetGauge().GetValue()
			emitNewConstMetric()

		case dto.MetricType_UNTYPED:
			valType = prometheus.UntypedValue
			val = metric.GetUntyped().GetValue()
			emitNewConstMetric()

		case dto.MetricType_SUMMARY:
			quantiles := map[float64]float64{}
			for _, q := range metric.GetSummary().GetQuantile() {
				quantiles[q.GetQuantile()] = q.GetValue()
			}
			ch <- prometheus.MustNewConstSummary(
				prometheus.NewDesc(
					metricFamily.GetName(),
					metricFamily.GetHelp(),
					LabelNames, nil,
				),
				metric.GetSummary().GetSampleCount(),
				metric.GetSummary().GetSampleSum(),
				quantiles, LabelValues...,
			)

		case dto.MetricType_HISTOGRAM, dto.MetricType_GAUGE_HISTOGRAM:
			buckets := map[float64]uint64{}
			for _, b := range metric.GetHistogram().GetBucket() {
				buckets[b.GetUpperBound()] = b.GetCumulativeCount()
			}
			ch <- prometheus.MustNewConstHistogram(
				prometheus.NewDesc(
					metricFamily.GetName(),
					metricFamily.GetHelp(),
					LabelNames, nil,
				),
				metric.GetHistogram().GetSampleCount(),
				metric.GetHistogram().GetSampleSum(),
				buckets, LabelValues...,
			)

		default:
			klog.Errorf("unknown metric type: %v", metricType)
		}
	}
}
