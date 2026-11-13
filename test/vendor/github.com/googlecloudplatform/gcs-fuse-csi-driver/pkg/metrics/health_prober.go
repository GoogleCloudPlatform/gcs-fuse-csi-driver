/*
Copyright 2018 The Kubernetes Authors.
Copyright 2026 Google LLC

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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	dto "github.com/prometheus/client_model/go"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

// FailureReason classifies the outcome of a kernel-level FUSE mount health probe.
type FailureReason string

const (
	ReasonNone         FailureReason = "none"
	ReasonTimeout      FailureReason = "deadlock_timeout"
	ReasonDisconnected FailureReason = "enotconn_crashed"
	ReasonFuseIOError  FailureReason = "fuse_io_error"

	defaultHealthProbeInterval = 30 * time.Second
	defaultHealthProbeTimeout  = 5 * time.Second

	mountUnhealthyMetricName = "mount_unhealthy"
	mountUnhealthyMetricHelp = "Indicates whether the GCSFuse mount is unhealthy (1) or healthy (0)."

	exitCodeMetricName = "exit_code"
	exitCodeMetricHelp = "Last recorded exit code and termination reason of the GCSFuse process."
)

// ProbeResult captures the health state of a FUSE mount to be exported to metrics.
type ProbeResult struct {
	Unhealthy bool
	Reason    FailureReason
	LatencyMs int64
}

// ExitDetail captures the process termination state of GCSFuse to be exported to metrics.
type ExitDetail struct {
	ExitCode     int    `json:"exitCode"`
	ExitReason   string `json:"exitReason"`
	OOMKillCount uint64 `json:"oomKillCount"`
}

// statxFuncType matches unix.Statx for dependency injection in unit tests.
type statxFuncType func(dirfd int, path string, flags int, mask int, stat *unix.Statx_t) error

// VolumeHealthProber periodically probes a published FUSE mount using unix.Statx
// with AT_STATX_FORCE_SYNC to bypass the Linux VFS attribute cache.
type VolumeHealthProber struct {
	mountPath     string
	readyFilePath string
	probeInterval time.Duration
	probeTimeout  time.Duration
	statxFunc     statxFuncType

	mu           sync.Mutex
	initialized  bool
	hasResult    bool
	latestResult ProbeResult
	inFlight     atomic.Bool
}

// NewVolumeHealthProber creates a new VolumeHealthProber for the given mountPath and sidecar readyFilePath.
func NewVolumeHealthProber(mountPath, readyFilePath string) *VolumeHealthProber {
	return &VolumeHealthProber{
		mountPath:     mountPath,
		readyFilePath: readyFilePath,
		probeInterval: defaultHealthProbeInterval,
		probeTimeout:  defaultHealthProbeTimeout,
		statxFunc:     unix.Statx,
	}
}

// SetMountPath updates the mountPath of the prober if it was not set at construction time.
func (p *VolumeHealthProber) SetMountPath(mountPath string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mountPath = mountPath
}

// Start runs the periodic mount health probe loop until ctx is canceled.
func (p *VolumeHealthProber) Start(ctx context.Context) {
	interval := p.probeInterval
	if interval <= 0 {
		interval = defaultHealthProbeInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = p.probeIfInitialized()
			}
		}
	}()
}

// isReadyInitialized checks whether the volume has completed startup initialization
// (verified by observing the sidecar ready file). Once observed, initialized remains
// latched so that subsequent GCSFuse crashes (which remove the ready file) continue
// to be probed and reported as enotconn_crashed.
func (p *VolumeHealthProber) isReadyInitialized() bool {
	if p.initialized {
		return true
	}
	if p.readyFilePath == "" || p.mountPath == "" {
		return false
	}
	if _, err := os.Stat(p.readyFilePath); err == nil {
		p.initialized = true
		return true
	}
	return false
}

func (p *VolumeHealthProber) probeIfInitialized() (ProbeResult, bool) {
	p.mu.Lock()
	if !p.isReadyInitialized() {
		p.mu.Unlock()
		return ProbeResult{}, false
	}
	p.mu.Unlock()

	res := p.executeProbe()

	p.mu.Lock()
	p.latestResult = res
	p.hasResult = true
	p.mu.Unlock()

	return res, true
}

// Result returns the latest probe result if the volume has completed startup initialization.
// If the volume is initialized but has not yet been probed by the background ticker, an immediate
// probe is executed.
func (p *VolumeHealthProber) Result() (ProbeResult, bool) {
	if p == nil {
		return ProbeResult{}, false
	}
	p.mu.Lock()
	if !p.isReadyInitialized() {
		p.mu.Unlock()
		return ProbeResult{}, false
	}
	if p.hasResult {
		res := p.latestResult
		p.mu.Unlock()
		return res, true
	}
	p.mu.Unlock()

	return p.probeIfInitialized()
}

// executeProbe executes the kernel-cache-bypassing statx() check against the FUSE mount.
func (p *VolumeHealthProber) executeProbe() ProbeResult {
	timeout := p.probeTimeout
	if timeout <= 0 {
		timeout = defaultHealthProbeTimeout
	}

	// Single-flight guard: if a previous statx() syscall is still blocked in kernel D-state
	// on a deadlocked mount, do not spawn additional blocked OS threads.
	if !p.inFlight.CompareAndSwap(false, true) {
		return ProbeResult{Unhealthy: true, Reason: ReasonTimeout, LatencyMs: timeout.Milliseconds()}
	}

	errCh := make(chan error, 1)
	start := time.Now()
	statxFn := p.statxFunc
	if statxFn == nil {
		statxFn = unix.Statx
	}

	// Execute statx() check on the fuse mount
	go func() {
		defer p.inFlight.Store(false)
		var statxBuf unix.Statx_t
		// AT_STATX_FORCE_SYNC forces the OS to bypass the kernel cache
		// and issue a sync FUSE_GETATTR upcall to GCSFuse.
		flags := unix.AT_STATX_FORCE_SYNC | unix.AT_SYMLINK_NOFOLLOW
		errCh <- statxFn(unix.AT_FDCWD, p.mountPath, flags, unix.STATX_BASIC_STATS, &statxBuf)
	}()

	// Check the result with a timeout.
	select {
	case err := <-errCh:
		// Success Case
		if err == nil {
			return ProbeResult{Unhealthy: false, Reason: ReasonNone, LatencyMs: time.Since(start).Milliseconds()}
		}

		// Crash Detection: Transport endpoint is not connected.
		// The gcsfuse daemon exited/crashed without unmounting.
		if errors.Is(err, unix.ENOTCONN) {
			return ProbeResult{Unhealthy: true, Reason: ReasonDisconnected, LatencyMs: time.Since(start).Milliseconds()}
		}

		return ProbeResult{Unhealthy: true, Reason: ReasonFuseIOError, LatencyMs: time.Since(start).Milliseconds()}

	case <-time.After(timeout):
		// Block State or Mount hung
		return ProbeResult{Unhealthy: true, Reason: ReasonTimeout, LatencyMs: timeout.Milliseconds()}
	}
}

func newMountUnhealthyMetricFamily(res ProbeResult) *dto.MetricFamily {
	name := mountUnhealthyMetricName
	help := mountUnhealthyMetricHelp
	metricType := dto.MetricType_GAUGE
	val := 0.0
	if res.Unhealthy {
		val = 1.0
	}
	reasonKey := "reason"
	reasonVal := string(res.Reason)
	return &dto.MetricFamily{
		Name: &name,
		Help: &help,
		Type: &metricType,
		Metric: []*dto.Metric{
			{
				Label: []*dto.LabelPair{
					{Name: &reasonKey, Value: &reasonVal},
				},
				Gauge: &dto.Gauge{Value: &val},
			},
		},
	}
}

func readExitDetail(socketBasePath string) (ExitDetail, bool) {
	if socketBasePath == "" {
		return ExitDetail{}, false
	}
	data, err := os.ReadFile(filepath.Join(socketBasePath, util.ExitDetailFileName))
	if err != nil {
		if !os.IsNotExist(err) {
			klog.V(4).Infof("failed to read exit detail file in %q: %v", socketBasePath, err)
		}
		return ExitDetail{}, false
	}
	var detail ExitDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		klog.Warningf("failed to unmarshal exit detail file in %q: %v", socketBasePath, err)
		return ExitDetail{}, false
	}
	return detail, true
}

func newExitCodeMetricFamily(detail ExitDetail) *dto.MetricFamily {
	name := exitCodeMetricName
	help := exitCodeMetricHelp
	metricType := dto.MetricType_GAUGE
	val := 1.0
	codeKey := "exit_code"
	codeVal := strconv.Itoa(detail.ExitCode)
	reasonKey := "exit_reason"
	reasonVal := detail.ExitReason
	return &dto.MetricFamily{
		Name: &name,
		Help: &help,
		Type: &metricType,
		Metric: []*dto.Metric{
			{
				Label: []*dto.LabelPair{
					{Name: &codeKey, Value: &codeVal},
					{Name: &reasonKey, Value: &reasonVal},
				},
				Gauge: &dto.Gauge{Value: &val},
			},
		},
	}
}
