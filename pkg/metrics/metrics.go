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
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

	metricsPath           = "/metrics"
	unixURL               = "http://unix/"
	contentTypeHeader     = "Content-Type"
	contentEncodingHeader = "Content-Encoding"
	acceptEncodingHeader  = "Accept-Encoding"

	controllerTypeJobSet          = "jobset"
	controllerTypeLeaderWorkerSet = "leaderworkerset"
	controllerTypeDeployment      = "deployment"
	controllerTypeReplicaSet      = "replicaset"
	controllerTypeStatefulSet     = "statefulset"
	controllerTypeDaemonSet       = "daemonset"

	podTemplateHashLabel         = "pod-template-hash"
	rolloutsPodTemplateHashLabel = "rollouts-pod-template-hash"
	jobSetNameLabel              = "jobset.sigs.k8s.io/jobset-name"
	leaderWorkerSetNameLabel     = "leaderworkerset.sigs.k8s.io/name"

	// scrapeTimeoutHeader is set by Prometheus scrapers to the configured scrape_timeout in seconds.
	scrapeTimeoutHeader = "X-Prometheus-Scrape-Timeout-Seconds"

	// metricsServerWriteTimeout is the WriteTimeout of the node driver metrics HTTP server.
	metricsServerWriteTimeout = 10 * time.Second

	// defaultStreamMetricsBudget is the default overall time budget for scraping all mounts when
	// streaming metrics and the scraper does not advertise its timeout via scrapeTimeoutHeader.
	// Why: Mounts are scraped sequentially, so the total latency grows with the number of mounts. The
	// GKE metrics collector scrapes this endpoint every 60s with a 45s timeout (3/4 of the interval) and
	// does not send scrapeTimeoutHeader, so the budget is kept below 45s. Prometheus scrapers send
	// scrapeTimeoutHeader, which lowers the budget to fit their scrape_timeout. The per-request write
	// deadline is extended to match, overriding metricsServerWriteTimeout.
	defaultStreamMetricsBudget = 30 * time.Second

	// streamMetricsWriteSlack is the extra time, beyond the streaming budget, allowed for flushing
	// the encoder and gzip writer after the last mount has been scraped.
	streamMetricsWriteSlack = 2 * time.Second

	// mountScrapeTimeout is the maximum time allowed for scraping the metrics of a single mount.
	mountScrapeTimeout = 5 * time.Second
)

var gzipPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(nil)
	},
}

type Manager interface {
	InitializeHTTPHandler()
	RegisterMetricsCollector(mountPath, podNamespace, podName, bucketName, nodeName, emptyDirBasePath, podUID, volumeName, k8sControllerType, k8sControllerName string)
	UnregisterMetricsCollector(mountPath, nodeName, podUID, volumeName string)
}

type manager struct {
	registry                         *prometheus.Registry
	metricsEndpoint                  string
	fuseSocketDir                    string
	clientset                        clientset.Interface
	streamMetrics                    bool
	enableGcsFuseVolumeMetricsSchema bool
	// sequentialMetrics (--sequential-metrics-export) enables sequentialMetricsHandler, which scrapes and
	// encodes mounts sequentially instead of using prometheus.Registry.Gather().
	sequentialMetrics bool
	// streamMetricsBudget is the overall time budget for scraping all mounts in sequentialMetricsHandler.
	streamMetricsBudget time.Duration
	// streamMountScrapeTimeout is the maximum time allowed for scraping a single mount in
	// sequentialMetricsHandler. Defaults to mountScrapeTimeout.
	streamMountScrapeTimeout time.Duration
	// streamStartIndex returns the index of the mount at which a sequentialMetricsHandler request starts
	// scraping, given the number of mounts. Defaults to rand.IntN; overridden in tests.
	streamStartIndex func(n int) int
	// streamScrapeSlot allows only one sequentialMetricsHandler request to scrape mounts at a time.
	// Why: Each in-flight request holds up to one mount's parsed metrics, so concurrent requests (e.g.
	// from several scrapers) would multiply the peak memory that streaming is meant to bound.
	streamScrapeSlot chan struct{}

	maximumNumberOfCollectors int
	volumeMountPathRegistered sets.Set[string]
	collectors                []*metricsCollector
	mutex                     sync.Mutex
}

func NewMetricsManager(metricsEndpoint, fuseSocketDir string, maximumNumberOfCollectors int, clientset clientset.Interface, streamMetrics, sequentialMetrics, enableGcsFuseVolumeMetricsSchema bool) Manager {
	mm := &manager{
		registry:                         prometheus.NewRegistry(),
		metricsEndpoint:                  metricsEndpoint,
		fuseSocketDir:                    fuseSocketDir,
		clientset:                        clientset,
		streamMetrics:                    streamMetrics,
		enableGcsFuseVolumeMetricsSchema: enableGcsFuseVolumeMetricsSchema,
		sequentialMetrics:                sequentialMetrics,
		streamMetricsBudget:              defaultStreamMetricsBudget,
		streamMountScrapeTimeout:         mountScrapeTimeout,
		streamStartIndex:                 rand.IntN,
		streamScrapeSlot:                 make(chan struct{}, 1),
		volumeMountPathRegistered:        sets.Set[string]{},
		maximumNumberOfCollectors:        maximumNumberOfCollectors,
		mutex:                            sync.Mutex{},
	}

	return mm
}

// snapshotCollectors returns the registered collectors in registration order.
func (mm *manager) snapshotCollectors() []*metricsCollector {
	mm.mutex.Lock()
	defer mm.mutex.Unlock()

	return slices.Clone(mm.collectors)
}

func (mm *manager) metricsHandler() http.Handler {
	// Why: When sequentialMetrics (--sequential-metrics-export) is enabled, promhttp.HandlerFor(mm.registry, ...)
	// is not used, because prometheus.Registry.Gather() runs all registered mount collectors concurrently and
	// holds every mount's *dto.MetricFamily objects in memory before writing the HTTP response, so its peak
	// memory grows with the number of mounts. Instead, sequentialMetricsHandler scrapes and encodes mounts one
	// at a time, and only one such request runs at a time, bounding peak memory to roughly one mount's parsed
	// metrics.
	if mm.sequentialMetrics {
		return http.HandlerFunc(mm.sequentialMetricsHandler)
	}
	return promhttp.HandlerFor(mm.registry, promhttp.HandlerOpts{})
}

// streamMetricsBudgetFor returns the overall time budget for scraping all mounts for the given request.
// The configured budget is further capped by the scraper's own timeout, if advertised.
func (mm *manager) streamMetricsBudgetFor(header http.Header) time.Duration {
	budget := mm.streamMetricsBudget
	if budget <= 0 {
		budget = defaultStreamMetricsBudget
	}
	if v := header.Get(scrapeTimeoutHeader); v != "" {
		if seconds, err := strconv.ParseFloat(v, 64); err == nil && seconds > 0 {
			// Leave 10% of the scraper's timeout for encoding and network transfer.
			if scraperBudget := time.Duration(seconds * 0.9 * float64(time.Second)); scraperBudget < budget {
				budget = scraperBudget
			}
		}
	}
	return budget
}

func (mm *manager) sequentialMetricsHandler(w http.ResponseWriter, r *http.Request) {
	// Why: Mounts are scraped one after another, so the total latency is the sum of all mounts' latencies.
	// Bound the whole scrape by an overall budget that fits inside the scraper's timeout, and extend this
	// request's write deadline to the budget plus streamMetricsWriteSlack, overriding the server-wide
	// WriteTimeout (metricsServerWriteTimeout), which is shorter than the default budget. Once the budget
	// is used up, the remaining mounts are skipped so that a partial but well-formed response is
	// delivered instead of a truncated body or a dropped connection. The server
	// WriteTimeout does not cancel r.Context(), so the budget is enforced through a derived context.
	budget := mm.streamMetricsBudgetFor(r.Header)
	deadline := time.Now().Add(budget)
	if err := http.NewResponseController(w).SetWriteDeadline(deadline.Add(streamMetricsWriteSlack)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		klog.V(4).Infof("failed to set write deadline for streaming metrics response: %v", err)
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()

	// Wait for any other streaming request to finish. The wait counts against this request's budget.
	if mm.streamScrapeSlot != nil {
		select {
		case mm.streamScrapeSlot <- struct{}{}:
			defer func() { <-mm.streamScrapeSlot }()
		case <-ctx.Done():
			if r.Context().Err() == nil {
				klog.Warningf("streaming metrics budget %v exhausted while waiting for another metrics request to finish", budget)
				http.Error(w, "timed out waiting for another metrics request to finish", http.StatusServiceUnavailable)
			}
			return
		}
	}

	contentType := expfmt.Negotiate(r.Header)
	w.Header().Set(contentTypeHeader, string(contentType))

	var out io.Writer = w
	if acceptsGzip(r.Header) {
		w.Header().Set(contentEncodingHeader, "gzip")
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			_ = gz.Close()
			gz.Reset(io.Discard)
			gzipPool.Put(gz)
		}()
		out = gz
	}

	sw := &skipFirstLineWriter{w: out, skipped: true}
	enc := expfmt.NewEncoder(sw, contentType)
	defer func() {
		if closer, ok := enc.(expfmt.Closer); ok {
			_ = closer.Close()
		}
	}()

	isTextFormat := contentType.FormatType() == expfmt.TypeTextPlain
	seenFamilies := make(map[string]dto.MetricType)

	collectors := mm.snapshotCollectors()
	n := len(collectors)
	if n == 0 {
		return
	}
	// Why: Start at a random mount on every request. When the budget is exhausted (e.g. by many mounts or
	// hung sidecars), the remaining mounts are skipped. With a fixed starting mount, the same tail mounts
	// would be skipped on every scrape and their metrics would never be exported. A random start gives
	// every mount the same chance of being scraped, without any state shared across requests, and a hung
	// sidecar cannot repeatedly use up the budget ahead of the same mounts.
	startIndex := mm.streamStartIndex
	if startIndex == nil {
		startIndex = rand.IntN
	}
	start := startIndex(n)
	perMountTimeout := mm.perMountScrapeTimeout()
	for i := range n {
		c := collectors[(start+i)%n]
		writeErr := false
		// The per-mount scrape timeout is derived from ctx, so it is also capped at the remaining budget.
		completed := c.scrapeMetricFamilies(ctx, perMountTimeout, true, func(mf *dto.MetricFamily) bool {
			if !c.transformMetricFamily(mf) {
				return true
			}
			if err := encodeMetricFamily(enc, sw, mf, isTextFormat, seenFamilies); err != nil {
				klog.Errorf("failed to encode metric family %q: %v", mf.GetName(), err)
				writeErr = true
				return false
			}
			return true
		})
		if writeErr || r.Context().Err() != nil {
			return
		}
		if ctx.Err() != nil {
			// The budget ran out while (or right after) scraping this mount. If its scrape did not
			// complete, its metrics were dropped, so report it as skipped as well.
			skipped := n - i - 1
			if !completed {
				skipped++
			}
			if skipped > 0 {
				klog.Warningf("streaming metrics budget %v exhausted, skipping metrics of %d out of %d mounts", budget, skipped, n)
			}
			return
		}
	}
}

// perMountScrapeTimeout returns the maximum time allowed for scraping a single mount when streaming.
// Each mount gets the per-mount timeout (by default the same mountScrapeTimeout as the non-streaming
// path), capped at the remaining budget via the request context.
// Why: Splitting the remaining budget evenly across the remaining mounts would give early mounts only a
// tiny slice on nodes with many mounts, so healthy but slightly slower sidecars would be canceled and
// dropped even though fast mounts leave most of the budget unused. Hung sidecars are instead bounded by
// the per-mount timeout and the overall budget, and the random starting mount in sequentialMetricsHandler
// ensures that the mounts skipped after the budget is exhausted vary across requests.
func (mm *manager) perMountScrapeTimeout() time.Duration {
	if mm.streamMountScrapeTimeout <= 0 {
		return mountScrapeTimeout
	}
	return mm.streamMountScrapeTimeout
}

// acceptsGzip reports whether the Accept-Encoding header accepts gzip. Like older promhttp versions, it
// ignores q-values, so "gzip;q=0" is treated as accepting gzip; real scrapers do not send it.
func acceptsGzip(header http.Header) bool {
	for _, part := range strings.Split(header.Get(acceptEncodingHeader), ",") {
		part = strings.TrimSpace(part)
		if part == "gzip" || strings.HasPrefix(part, "gzip;") {
			return true
		}
	}
	return false
}

// skipFirstLineWriter wraps an io.Writer and discards bytes up to and including the first newline.
type skipFirstLineWriter struct {
	w       io.Writer
	skipped bool
}

func (s *skipFirstLineWriter) Write(p []byte) (int, error) {
	if s.skipped {
		return s.w.Write(p)
	}
	idx := bytes.IndexByte(p, '\n')
	if idx == -1 {
		return len(p), nil
	}
	s.skipped = true
	rest := p[idx+1:]
	if len(rest) == 0 {
		return len(p), nil
	}
	n, err := s.w.Write(rest)
	if err != nil {
		return idx + 1 + n, err
	}
	return len(p), nil
}

func encodeMetricFamily(
	enc expfmt.Encoder,
	sw *skipFirstLineWriter,
	mf *dto.MetricFamily,
	isTextFormat bool,
	seenFamilies map[string]dto.MetricType,
) error {
	name := mf.GetName()
	metricType := mf.GetType()
	if prevType, seen := seenFamilies[name]; seen {
		if prevType != metricType {
			klog.Errorf("metric family %q has conflicting type %v (already emitted as %v), skipping", name, metricType, prevType)
			return nil
		}
		if isTextFormat {
			// Why: Standard Prometheus text parsers (expfmt.TextParser) reject repeated
			// "# HELP" or "# TYPE" lines for the same metric family name within a single
			// scrape payload, whereas sample lines for an already-declared metric family
			// are merged into that family. Clear Help and skip the single "# TYPE" header
			// line so subsequent mounts emit only sample lines.
			// Note: Because mounts are streamed one at a time, the text output repeats a sample
			// group per mount for each metric family, with other families in between, i.e. the
			// samples of a family are not contiguous. expfmt.TextParser and Prometheus' text
			// parser accept this, but stricter consumers may not. The protobuf delimited format,
			// where repeated MetricFamily messages are well-defined, is preferred for such consumers.
			mf.Help = nil
			sw.skipped = false
			defer func() { sw.skipped = true }()
		}
	} else {
		seenFamilies[name] = metricType
	}
	return enc.Encode(mf)
}

// InitializeHTTPHandler sets up a server and creates a handler for metrics.
func (mm *manager) InitializeHTTPHandler() {
	mux := http.NewServeMux()
	mux.HandleFunc(metricsPath, mm.metricsHandler().ServeHTTP)

	// Configure the http server and start it.
	metricServer := &http.Server{
		Addr:           mm.metricsEndpoint,
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   metricsServerWriteTimeout,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		klog.Infof("metric server listening at %q", mm.metricsEndpoint)
		if err := metricServer.ListenAndServe(); err != nil {
			klog.Errorf("failed to start metric server at specified endpoint %q and path %q: %v", mm.metricsEndpoint, metricsPath, err)
		}
	}()
}

// MetricSchemaPolicy encapsulates schema-specific metric name normalization and constant label construction.
// This allows seamless, instantaneous rollback between the k8s_gcs_fuse_volume Monitored Resource schema
// and legacy k8s_pod schema without workload restarts across all 4 {CSI Driver Flag x Sidecar Binary} combinations.
type MetricSchemaPolicy struct {
	NormalizeMetricName func(name string) string
	BuildConstLabels    func(podName, podNamespace, volumeName, bucketName, controllerType, controllerName string) map[string]string
}

func isGrpcMetric(name string) bool {
	return strings.HasPrefix(strings.TrimPrefix(name, "volume_"), "grpc_")
}

func newMetricSchemaPolicy(enableGcsFuseVolumeMetricsSchema bool) MetricSchemaPolicy {
	if enableGcsFuseVolumeMetricsSchema {
		return MetricSchemaPolicy{
			NormalizeMetricName: func(name string) string {
				name = strings.TrimPrefix(name, "volume_")
				if isGrpcMetric(name) {
					return name
				}
				if name == "fs_ops_latency" {
					name = "fs_ops_latencies"
				}
				return "volume_" + name
			},
			BuildConstLabels: func(podName, podNamespace, volumeName, bucketName, controllerType, controllerName string) map[string]string {
				return map[string]string{
					"pod_name":            podName,
					"namespace_name":      podNamespace,
					"volume_name":         volumeName,
					"bucket_name":         bucketName,
					"k8s_controller_type": controllerType,
					"k8s_controller_name": controllerName,
				}
			},
		}
	}
	return MetricSchemaPolicy{
		NormalizeMetricName: func(name string) string {
			name = strings.TrimPrefix(name, "volume_")
			if name == "fs_ops_latencies" {
				return "fs_ops_latency"
			}
			return name
		},
		BuildConstLabels: func(podName, podNamespace, volumeName, bucketName, _, _ string) map[string]string {
			return map[string]string{
				"pod_name":       podName,
				"namespace_name": podNamespace,
				"volume_name":    volumeName,
				"bucket_name":    bucketName,
				// Why: In the legacy k8s_pod Monitored Resource schema, emitting a non-empty runtime pod_uid
				// causes high-cardinality Monarch label explosions across Pod churn. However, omitting pod_uid
				// or any of {pod_name, namespace_name, volume_name, bucket_name} breaks override_labels in the
				// GKE Metrics Collector (SCHEMA_K8S_POD), resulting in 100% silent telemetry drops.
				"pod_uid": "",
			}
		},
	}
}

// RegisterMetricsCollector registers the metrics collector. It is idempotent to register the same collector.
func (mm *manager) RegisterMetricsCollector(mountPath, podNamespace, podName, bucketName, nodeName, emptyDirBasePath, podUID, volumeName, k8sControllerType, k8sControllerName string) {
	socketBasePath := util.GetSocketBasePath(podUID, volumeName, mm.fuseSocketDir)
	if err := os.Symlink(emptyDirBasePath, socketBasePath); err != nil && !os.IsExist(err) {
		klog.Errorf("failed to create symbolic link to path %q: %v", socketBasePath, err)
		return
	}

	policy := newMetricSchemaPolicy(mm.enableGcsFuseVolumeMetricsSchema)
	constLabels := policy.BuildConstLabels(podName, podNamespace, volumeName, bucketName, k8sControllerType, k8sControllerName)

	c := NewMetricsCollector(socketBasePath, emptyDirBasePath, podNamespace, podName, podUID, volumeName, constLabels, mm.clientset, mm.streamMetrics, mm.enableGcsFuseVolumeMetricsSchema)

	// Lock the number of registered collectors while we attempt to register a new collector.
	mm.mutex.Lock()
	defer mm.mutex.Unlock()

	if mm.maximumNumberOfCollectors == 0 {
		klog.Infof("could not register metrics collector for mount path %q. The metrics collector limit for node %q is set to zero.", mountPath, nodeName)
		return
	}

	// Check if we need to register collector. We register a collector when the following are met:
	// 1. There is space on the metrics pipeline for the collector to be registered.
	// 2. The metrics collector has not previously been registered.
	if mm.maximumNumberOfCollectors > 0 {
		// If volume is already registered, do not register again. This flow can get triggered
		//  since CSI driver has republishVolume capability.
		if mm.volumeMountPathRegistered.Has(mountPath) {
			return
		}
		// If collector hasn't been registered and there's no space left, log a warning.
		if mm.volumeMountPathRegistered.Len() >= mm.maximumNumberOfCollectors {
			klog.V(4).Infof("could not register a metrics collector for mount path %q. There's already %d collectors registered on node %q.", mountPath, mm.volumeMountPathRegistered.Len(), nodeName)
			return
		}
	}

	// Attempt to register new metrics collector and record success.
	err := mm.registry.Register(c)
	if err != nil {
		if !strings.Contains(err.Error(), prometheus.AlreadyRegisteredError{}.Error()) {
			klog.Errorf("failed to register metrics collector for mount path %q, bucket %q: %v", mountPath, bucketName, err)
		}
	} else {
		mm.volumeMountPathRegistered.Insert(mountPath)
		if mc, ok := c.(*metricsCollector); ok {
			mm.collectors = append(mm.collectors, mc)
		}
		klog.Infof("successfully registered a new metrics collector for mount path %q. There's %d collectors registered on node %q.", mountPath, mm.volumeMountPathRegistered.Len(), nodeName)
	}
}

// UnregisterMetricsCollector unregisters the metrics collector. It is idempotent to unregister the same collector.
func (mm *manager) UnregisterMetricsCollector(mountPath, nodeName, podUID, volumeName string) {
	// Why: UnregisterMetricsCollector constructs a dummy collector with constLabels = nil.
	// Prometheus Registry.Unregister matches collectors solely by hashing the descriptor emitted by Describe().
	// Therefore, Describe() MUST remain pinned to {pod_uid, volume_name} independent of c.constLabels.
	c := NewMetricsCollector("", "", "", "", podUID, volumeName, nil, nil, mm.streamMetrics, mm.enableGcsFuseVolumeMetricsSchema)

	// Lock the number of registered collectors while we attempt to unregister a collector.
	mm.mutex.Lock()
	defer mm.mutex.Unlock()

	if ok := mm.registry.Unregister(c); !ok {
		klog.Infof("Unregister metrics collector for mount path %q is not needed since the collector is not registered.", mountPath)
	} else {
		mm.volumeMountPathRegistered.Delete(mountPath)
		mm.collectors = slices.DeleteFunc(mm.collectors, func(mc *metricsCollector) bool {
			if mc.podUID == podUID && mc.volumeName == volumeName {
				if mc.httpClient != nil {
					mc.httpClient.CloseIdleConnections()
				}
				return true
			}
			return false
		})
		klog.Infof("successfully unregistered a metrics collector for mount path %q. There's %d collectors registered on node %q.", mountPath, mm.volumeMountPathRegistered.Len(), nodeName)
	}
}

type metricsCollector struct {
	emptyDirBasePath                 string
	constLabels                      map[string]string
	namespace                        string
	podName                          string
	podUID                           string
	volumeName                       string
	httpClient                       *http.Client
	clientset                        clientset.Interface
	streamMetrics                    bool
	enableGcsFuseVolumeMetricsSchema bool
}

// NewMetricsCollector returns a new Collector exposing metrics read from the give path.
func NewMetricsCollector(socketBasePath, emptyDirBasePath, namespace, podName, podUID, volumeName string, labels map[string]string, clientset clientset.Interface, streamMetrics bool, enableGcsFuseVolumeMetricsSchema bool) prometheus.Collector {
	c := &metricsCollector{
		emptyDirBasePath:                 emptyDirBasePath,
		constLabels:                      labels,
		namespace:                        namespace,
		podName:                          podName,
		podUID:                           podUID,
		volumeName:                       volumeName,
		clientset:                        clientset,
		streamMetrics:                    streamMetrics,
		enableGcsFuseVolumeMetricsSchema: enableGcsFuseVolumeMetricsSchema,
	}

	// Creating a new HTTP client that is configured to make HTTP requests over a unix domain socket.
	c.httpClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", filepath.Join(socketBasePath, SocketName))
			},
		},
	}

	return c
}

// Describe emits the description of metrics.
// Prometheus Registry relies on this func to identify collectors.
func (c *metricsCollector) Describe(ch chan<- *prometheus.Desc) {
	// Why: Collector identity in prometheus.Registry.Register/Unregister is computed by hashing
	// the fqName and ConstLabels emitted by Describe(). This MUST remain pinned to {pod_uid: c.podUID, volume_name: c.volumeName}
	// and MUST NOT use c.constLabels (which omits pod_uid in k8s_gcs_fuse_volume mode), otherwise UnregisterMetricsCollector
	// (which passes nil constLabels) will fail to match the descriptor hash and leak collectors on volume unmount.
	ch <- prometheus.NewDesc("gke_gcsfuse_csi_metric", "GKE GCSFuse CSI metric.", nil, map[string]string{"pod_uid": c.podUID, "volume_name": c.volumeName})
}

// Collect scrapes metrics from the sidecar and emits metrics.
func (c *metricsCollector) Collect(ch chan<- prometheus.Metric) {
	_ = c.scrapeMetricFamilies(context.Background(), mountScrapeTimeout, false, func(mf *dto.MetricFamily) bool {
		c.emitMetricFamily(mf, ch)
		return true
	})
}

// scrapeMetricFamilies scrapes the sidecar metrics endpoint, bounded by timeout and parentCtx, and calls
// handleFamily for each parsed MetricFamily until it returns false. It returns true only if the response
// was received and fully parsed, and every family was passed to handleFamily without early termination.
// If parseWholeBody is true, the whole response is parsed before any family is handled, families are
// handled in name order, and nothing is handled if the response cannot be parsed. Otherwise, the
// response is parsed as a stream if streamMetrics (--stream-metrics-export) is enabled.
func (c *metricsCollector) scrapeMetricFamilies(parentCtx context.Context, timeout time.Duration, parseWholeBody bool, handleFamily func(*dto.MetricFamily) bool) bool {
	if parentCtx.Err() != nil || c.clientset == nil {
		return false
	}

	pod, err := c.clientset.GetPod(c.namespace, c.podName)
	if err != nil || pod == nil || pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		klog.V(6).Infof("pod %v/%v does not exist, skip metrics scraping", c.namespace, c.podName)

		return false
	}

	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, unixURL, nil)
	if err != nil {
		klog.Errorf("failed to create scrape metrics request: %v", err)

		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		klog.Errorf("failed to scrape metrics: %v", err)

		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		klog.Errorf("unexpected HTTP status: %v", resp.Status)

		return false
	}

	if !parseWholeBody && c.streamMetrics {
		for mf, err := range ProcessMetricsDataAsStream(resp.Body) {
			if err != nil {
				klog.Errorf("failed to decode metrics data: %v", err)

				return false
			}
			if !handleFamily(mf) {
				return false
			}
		}
		return true
	}

	// Why: Do not use ProcessMetricsDataAsStream here. The underlying expfmt text decoder yields the
	// partially parsed families before reporting a parse error, e.g. when the per-mount scrape timeout
	// cuts the body off mid-family, which would emit truncated families (such as histograms with
	// missing buckets). Parse the whole body first and drop the mount on any error. Peak memory is the
	// same, since the text decoder also builds the full per-mount map before yielding anything.
	families, err := ProcessMetricsData(resp.Body)
	if err != nil {
		if parseWholeBody {
			klog.Errorf("failed to process metrics data, dropping metrics of pod %v/%v volume %q: %v", c.namespace, c.podName, c.volumeName, err)
		} else {
			klog.Errorf("failed to process metrics data: %v", err)
		}

		return false
	}

	for _, name := range slices.Sorted(maps.Keys(families)) {
		if !handleFamily(families[name]) {
			return false
		}
	}

	return true
}

// ProcessMetricsDataAsStream processes metrics that follow Prometheus text format,
// returning an iterator of MetricFamily.
func ProcessMetricsDataAsStream(metricsReader io.Reader) iter.Seq2[*dto.MetricFamily, error] {
	return func(yield func(*dto.MetricFamily, error) bool) {
		decoder := expfmt.NewDecoder(metricsReader, expfmt.FmtText)
		for {
			var mf dto.MetricFamily
			err := decoder.Decode(&mf)
			if err == io.EOF {
				break
			}
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(&mf, nil) {
				return
			}
		}
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

func (c *metricsCollector) resolveMetricFamilySchema(name string) (string, map[string]string) {
	// Normalize metric names bidirectionally based on enableGcsFuseVolumeMetricsSchema to support
	// live DaemonSet rollbacks (requiresRepublish) without restarting workload mounter sidecar containers.
	// gRPC metrics remain on the legacy k8s_pod schema (unprefixed, with pod_uid="").
	effectiveSchemaEnabled := c.enableGcsFuseVolumeMetricsSchema && !isGrpcMetric(name)
	policy := newMetricSchemaPolicy(effectiveSchemaEnabled)
	normalizedName := policy.NormalizeMetricName(name)
	constLabels := c.constLabels
	if !effectiveSchemaEnabled && c.enableGcsFuseVolumeMetricsSchema && c.constLabels != nil {
		constLabels = policy.BuildConstLabels(
			c.constLabels["pod_name"],
			c.constLabels["namespace_name"],
			c.constLabels["volume_name"],
			c.constLabels["bucket_name"],
			"",
			"",
		)
	}
	return normalizedName, constLabels
}

func hasValidMetricValue(metricType dto.MetricType, metric *dto.Metric) bool {
	switch metricType {
	case dto.MetricType_COUNTER:
		return metric.GetCounter() != nil
	case dto.MetricType_GAUGE:
		return metric.GetGauge() != nil
	case dto.MetricType_UNTYPED:
		return metric.GetUntyped() != nil
	case dto.MetricType_SUMMARY:
		return metric.GetSummary() != nil
	case dto.MetricType_HISTOGRAM, dto.MetricType_GAUGE_HISTOGRAM:
		return metric.GetHistogram() != nil
	default:
		return false
	}
}

// transformMetricFamily normalizes the MetricFamily name and injects constLabels directly in-place
// without allocating intermediate prometheus.Metric or prometheus.Desc wrappers.
func (c *metricsCollector) transformMetricFamily(metricFamily *dto.MetricFamily) bool {
	if metricFamily == nil || metricFamily.Type == nil || metricFamily.GetName() == "" || len(metricFamily.GetMetric()) == 0 {
		return false
	}

	metricType := metricFamily.GetType()
	switch metricType {
	case dto.MetricType_COUNTER, dto.MetricType_GAUGE, dto.MetricType_UNTYPED, dto.MetricType_SUMMARY, dto.MetricType_HISTOGRAM:
	case dto.MetricType_GAUGE_HISTOGRAM:
		histType := dto.MetricType_HISTOGRAM
		metricFamily.Type = &histType
		metricType = histType
	default:
		klog.Errorf("unknown metric type: %v", metricType)
		return false
	}

	name, constLabels := c.resolveMetricFamilySchema(metricFamily.GetName())
	metricFamily.Name = &name

	constLabelPairs := make([]*dto.LabelPair, 0, len(constLabels))
	for _, k := range slices.Sorted(maps.Keys(constLabels)) {
		v := constLabels[k]
		constLabelPairs = append(constLabelPairs, &dto.LabelPair{Name: &k, Value: &v})
	}

	metricFamily.Metric = slices.DeleteFunc(metricFamily.GetMetric(), func(metric *dto.Metric) bool {
		if metric == nil || !hasValidMetricValue(metricType, metric) {
			return true
		}
		metric.Label = slices.DeleteFunc(metric.GetLabel(), func(label *dto.LabelPair) bool {
			if label == nil {
				return true
			}
			_, exists := constLabels[label.GetName()]
			return exists
		})
		metric.Label = append(metric.Label, constLabelPairs...)
		// Drop sidecar-provided timestamps to match the non-streaming path (emitMetricFamily), which
		// never exports timestamps.
		metric.TimestampMs = nil
		return false
	})
	return len(metricFamily.Metric) > 0
}

// emitMetricFamily iterates MetricFamily, converts metricFamily.Metric to prometheus.Metric, and emits the metric via the given chan.
func (c *metricsCollector) emitMetricFamily(metricFamily *dto.MetricFamily, ch chan<- prometheus.Metric) {
	help := metricFamily.GetHelp()
	metricType := metricFamily.GetType()
	name, constLabels := c.resolveMetricFamilySchema(metricFamily.GetName())

	var cachedDesc *prometheus.Desc

	var cachedLabelNames []string

	for _, metric := range metricFamily.GetMetric() {
		rawLabels := metric.GetLabel()
		labels := make([]*dto.LabelPair, 0, len(rawLabels))
		for _, label := range rawLabels {
			if _, exists := constLabels[label.GetName()]; !exists {
				labels = append(labels, label)
			}
		}

		var desc *prometheus.Desc
		// Reuse cached prometheus.Desc assuming identical label ordering.
		if cachedDesc != nil && len(cachedLabelNames) == len(labels) {
			desc = cachedDesc
			for i, label := range labels {
				if cachedLabelNames[i] != label.GetName() {
					// Cache new prometheus.Desc to reduce garbage collection overhead and expensive string formatting.
					desc = nil
					break
				}
			}
		}

		if desc == nil {
			labelNames := make([]string, len(labels))
			for i, label := range labels {
				labelNames[i] = label.GetName()
			}
			desc = prometheus.NewDesc(name, help, labelNames, prometheus.Labels(constLabels))
			cachedDesc = desc
			cachedLabelNames = labelNames
		}

		labelValues := make([]string, len(labels))
		for i, label := range labels {
			labelValues[i] = label.GetValue()
		}

		switch metricType {
		case dto.MetricType_COUNTER:
			ch <- prometheus.MustNewConstMetric(
				desc,
				prometheus.CounterValue, metric.GetCounter().GetValue(), labelValues...,
			)

		case dto.MetricType_GAUGE:
			ch <- prometheus.MustNewConstMetric(
				desc,
				prometheus.GaugeValue, metric.GetGauge().GetValue(), labelValues...,
			)

		case dto.MetricType_UNTYPED:
			ch <- prometheus.MustNewConstMetric(
				desc,
				prometheus.UntypedValue, metric.GetUntyped().GetValue(), labelValues...,
			)

		case dto.MetricType_SUMMARY:
			quantiles := make(map[float64]float64, len(metric.GetSummary().GetQuantile()))
			for _, q := range metric.GetSummary().GetQuantile() {
				quantiles[q.GetQuantile()] = q.GetValue()
			}
			ch <- prometheus.MustNewConstSummary(
				desc,
				metric.GetSummary().GetSampleCount(),
				metric.GetSummary().GetSampleSum(),
				quantiles, labelValues...,
			)

		case dto.MetricType_HISTOGRAM, dto.MetricType_GAUGE_HISTOGRAM:
			buckets := make(map[float64]uint64, len(metric.GetHistogram().GetBucket()))
			for _, b := range metric.GetHistogram().GetBucket() {
				buckets[b.GetUpperBound()] = b.GetCumulativeCount()
			}
			ch <- prometheus.MustNewConstHistogram(
				desc,
				metric.GetHistogram().GetSampleCount(),
				metric.GetHistogram().GetSampleSum(),
				buckets, labelValues...,
			)

		default:
			klog.Errorf("unknown metric type: %v", metricType)
		}
	}
}

type PrometheusMetricManager interface {
	InitializeHTTPHandler()
	RecordSyncPVMetric(err error)
	RecordSyncPodMetric(err error)
}

type prometheusMetricManager struct {
	mux             *http.ServeMux
	metricsEndpoint string
	syncPVCounter   *prometheus.CounterVec
	syncPodCounter  *prometheus.CounterVec
}

// NewPrometheusMetricManager initializes and returns a prometheusMetricManager.
// This manager is intended to encapsulate Prometheus metric definitions
// and registrations used by this application. New metrics can be added here.
// Note that this manager is orthogonal to the metrics collector above.
// Use the metrics collector instead if your component is running on user nodes.
func NewPrometheusMetricManager(metricsEndpoint string, mux *http.ServeMux) *prometheusMetricManager {
	pmm := prometheusMetricManager{
		metricsEndpoint: metricsEndpoint,
		mux:             mux,
	}

	pmm.syncPVCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sync_pv_count",
			Help: "Number of syncPV operations",
		},
		[]string{"status_code"},
	)
	prometheus.MustRegister(pmm.syncPVCounter)

	pmm.syncPodCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sync_pod_count",
			Help: "Number of syncPod operations",
		},
		[]string{"status_code"},
	)
	prometheus.MustRegister(pmm.syncPodCounter)

	return &pmm
}

// InitializeHTTPHandler sets up a server and creates a handler for metrics.
func (pmm *prometheusMetricManager) InitializeHTTPHandler() {
	if pmm == nil || pmm.mux == nil {
		return
	}
	pmm.mux.Handle(metricsPath, promhttp.Handler())
	go func() {
		klog.Infof("ServeMux listening on HTTP endpoint %v and metrics path: %v", pmm.metricsEndpoint, metricsPath)
		err := http.ListenAndServe(pmm.metricsEndpoint, pmm.mux)
		if err != nil {
			klog.Fatalf("Failed to start HTTP server at specified endpoint and metrics path, err %v", err)
		}
	}()
}

func (pmm *prometheusMetricManager) RecordSyncPVMetric(err error) {
	if pmm == nil {
		return
	}
	pmm.syncPVCounter.WithLabelValues(GetErrorCode(err)).Inc()
}

func (pmm *prometheusMetricManager) RecordSyncPodMetric(err error) {
	if pmm == nil {
		return
	}
	pmm.syncPodCounter.WithLabelValues(GetErrorCode(err)).Inc()
}

func GetErrorCode(err error) string {
	// A nil error means success.
	if err == nil {
		return codes.OK.String()
	}

	internalErr, _ := status.FromError(err)
	code := internalErr.Code().String()
	return code
}

func resolveControllerKindAndName(kind, name string, labels map[string]string) (string, string) {
	if name == "" {
		return "", ""
	}
	switch strings.ToLower(kind) {
	case controllerTypeReplicaSet:
		if hash := labels[podTemplateHashLabel]; hash != "" && len(name) > len(hash)+1 && strings.HasSuffix(name, "-"+hash) {
			return controllerTypeDeployment, strings.TrimSuffix(name, "-"+hash)
		}
		if labels[rolloutsPodTemplateHashLabel] != "" {
			return "", ""
		}
		return controllerTypeReplicaSet, name
	case controllerTypeStatefulSet:
		return controllerTypeStatefulSet, name
	case controllerTypeDaemonSet:
		return controllerTypeDaemonSet, name
	default:
		// Unsupported or ambiguous controller types (such as Job, which may be owned by a
		// CronJob without a standard pod label) are left blank.
		return "", ""
	}
}

// ExtractK8sController resolves a pod's top-level workload controller type and name
// strictly from node-local Pod metadata (labels and controller OwnerReferences) without
// making cluster-scoped API calls. Supported controller types are jobset, leaderworkerset,
// deployment, replicaset, statefulset, and daemonset. Returns ("", "") if the pod is
// unmanaged or its top-level controller cannot be unambiguously determined from the Pod
// object (e.g., Job/CronJob or Argo Rollout ReplicaSet).
func ExtractK8sController(pod *corev1.Pod) (string, string) {
	if pod == nil {
		return "", ""
	}
	if js := pod.Labels[jobSetNameLabel]; js != "" {
		return controllerTypeJobSet, js
	}
	if lws := pod.Labels[leaderWorkerSetNameLabel]; lws != "" {
		return controllerTypeLeaderWorkerSet, lws
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return resolveControllerKindAndName(ref.Kind, ref.Name, pod.Labels)
		}
	}
	return "", ""
}
