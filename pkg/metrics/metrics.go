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
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"k8s.io/klog/v2"
)

const (
	metricsPath             = "/metrics"
	metricsFileNameTemplate = `/metrics_%d.prom`
	generationFileName      = "/generation.txt"
)

type Manager interface {
	InitializeHTTPHandler()
	RegisterMetricsCollector(targetPath, podNamespace, podName, bucketName string)
	UnregisterMetricsCollector(targetPath string)
}

type manager struct {
	registry        *prometheus.Registry
	metricsEndpoint string
}

func NewMetricsManager(metricsEndpoint string) Manager {
	mm := &manager{
		registry:        prometheus.NewRegistry(),
		metricsEndpoint: metricsEndpoint,
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

	podUID, volumeName, _ := util.ParsePodIDVolumeFromTargetpath(targetPath)
	c := NewTextFileCollector(emptyDirBasePath, podUID, volumeName, map[string]string{
		"pod_name":       podName,
		"namespace_name": podNamespace,
		"volume_name":    volumeName,
		"bucket_name":    bucketName,
		"pod_uid":        podUID,
	})
	if err := mm.registry.Register(c); err != nil && !strings.Contains(err.Error(), prometheus.AlreadyRegisteredError{}.Error()) {
		klog.Errorf("failed to register metrics collector for pod  %v/%v, volume %q, bucket %q: %v", podNamespace, podName, volumeName, bucketName, err)
	}
}

// UnregisterMetricsCollector unregisters the metrics collector.
func (mm *manager) UnregisterMetricsCollector(targetPath string) {
	podUID, volumeName, _ := util.ParsePodIDVolumeFromTargetpath(targetPath)

	// textFileCollector uses a hash of pod UID and volume name as an identifier.
	c := NewTextFileCollector("", podUID, volumeName, nil)
	if ok := mm.registry.Unregister(c); !ok {
		klog.Infof("Unregister metrics collector for targetPath %q is not needed since the collector is not registered", targetPath)
	}
}

type textFileCollector struct {
	path        string
	constLabels map[string]string
	podUID      string
	volumeName  string
}

// NewTextFileCollector returns a new Collector exposing metrics read from the give path.
func NewTextFileCollector(path, podUID, volumeName string, labels map[string]string) prometheus.Collector {
	c := &textFileCollector{
		path:        path,
		constLabels: labels,
		podUID:      podUID,
		volumeName:  volumeName,
	}

	return c
}

// Describe emits the description of metrics.
// Prometheus Registry relies on this func to identify collectors.
func (c *textFileCollector) Describe(ch chan<- *prometheus.Desc) {
	// Collector id is a hash of the values of the ConstLabels and fqName.
	ch <- prometheus.NewDesc("gke_gcsfuse_csi_metric", "GKE GCSFuse CSI metric.", nil, map[string]string{"pod_uid": c.podUID, "volume_name": c.volumeName})
}

// Collect emits metrics.
func (c *textFileCollector) Collect(ch chan<- prometheus.Metric) {
	families, err := ProcessMetricsFile(c.path)
	if err != nil {
		klog.Errorf("failed to process metrics from metrics file: %v", err)

		return
	}

	for _, mf := range families {
		c.emitMetricFamily(mf, ch)
	}
}

// ProcessMetricsFile processes a metrics file that follows Prometheus text format: https://prometheus.io/docs/instrumenting/exposition_formats/,
// returning its MetricFamily.
func ProcessMetricsFile(directoryPath string) (map[string]*dto.MetricFamily, error) {
	// Find latest file generation.
	generationFilePath := directoryPath + generationFileName
	genNumber, err := getGenerationNumber(generationFilePath)
	if err != nil {
		return nil, fmt.Errorf("could not get generation number from %s: %w", generationFilePath, err)
	}

	// Find latest metrics file name and open.
	metricsFilePath := directoryPath + GetMetricsFileName(genNumber)
	metricsFile, err := os.Open(metricsFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open metrics file %q: %w", metricsFilePath, err)
	}
	defer metricsFile.Close()

	var parser expfmt.TextParser
	metricFamilies, err := parser.TextToMetricFamilies(metricsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metrics file %q: %w", metricsFilePath, err)
	}

	return metricFamilies, nil
}

// GetMetricsFilePath creates the expected name for the latest metrics file.
func GetMetricsFileName(genNumber int) string {
	return fmt.Sprintf(metricsFileNameTemplate, genNumber)
}

func GetGenerationFileName() string {
	return generationFileName
}

// getGenerationNumber opens the generation.txt file and parses the payload into
// an integer. This integer represents the current generation of the metrics file.
func getGenerationNumber(filePath string) (int, error) {
	genFile, err := os.Open(filePath)
	if err != nil {
		return -1, fmt.Errorf("failed to open generation file %q: %w", filePath, err)
	}
	defer genFile.Close()

	// Create file reader
	reader := bufio.NewReader(genFile)

	// Read the first line.
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input) // Remove leading/trailing whitespace

	// Convert to integer
	num, err := strconv.Atoi(input)
	if err != nil {
		return -1, fmt.Errorf(`invalid input "%s" must be an integer: %w`, input, err)
	}

	return num, nil
}

// emitMetricFamily iterates MetricFamily, converts metricFamily.Metric to prometheus.Metric, and emits the metric via the given chan.
func (c *textFileCollector) emitMetricFamily(metricFamily *dto.MetricFamily, ch chan<- prometheus.Metric) {
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
