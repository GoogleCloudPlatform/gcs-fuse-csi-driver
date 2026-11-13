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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"golang.org/x/sys/unix"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestVolumeHealthProberExecuteProbe(t *testing.T) {
	t.Parallel()

	t.Run("healthy mount passes flags bypassing kernel cache", func(t *testing.T) {
		t.Parallel()
		mountPath := "/mnt/test-healthy"
		var gotDirfd, gotFlags, gotMask int
		var gotPath string

		prober := NewVolumeHealthProber(mountPath, "")
		prober.statxFunc = func(dirfd int, path string, flags int, mask int, _ *unix.Statx_t) error {
			gotDirfd = dirfd
			gotPath = path
			gotFlags = flags
			gotMask = mask
			return nil
		}

		res := prober.executeProbe()
		if res.Unhealthy {
			t.Errorf("expected Unhealthy=false, got true (reason=%q)", res.Reason)
		}
		if res.Reason != ReasonNone {
			t.Errorf("expected Reason=%q, got %q", ReasonNone, res.Reason)
		}
		if gotDirfd != unix.AT_FDCWD {
			t.Errorf("expected dirfd=AT_FDCWD (%d), got %d", unix.AT_FDCWD, gotDirfd)
		}
		if gotPath != mountPath {
			t.Errorf("expected path=%q, got %q", mountPath, gotPath)
		}
		wantFlags := unix.AT_STATX_FORCE_SYNC | unix.AT_SYMLINK_NOFOLLOW
		if gotFlags != wantFlags {
			t.Errorf("expected flags=%#x, got %#x", wantFlags, gotFlags)
		}
		if gotMask != unix.STATX_BASIC_STATS {
			t.Errorf("expected mask=STATX_BASIC_STATS (%#x), got %#x", unix.STATX_BASIC_STATS, gotMask)
		}
	})

	t.Run("crashed daemon returning ENOTCONN", func(t *testing.T) {
		t.Parallel()
		prober := NewVolumeHealthProber("/mnt/test-enotconn", "")
		prober.statxFunc = func(_ int, _ string, _ int, _ int, _ *unix.Statx_t) error {
			return unix.ENOTCONN
		}

		res := prober.executeProbe()
		if !res.Unhealthy {
			t.Errorf("expected Unhealthy=true on ENOTCONN, got false")
		}
		if res.Reason != ReasonDisconnected {
			t.Errorf("expected Reason=%q, got %q", ReasonDisconnected, res.Reason)
		}
	})

	t.Run("generic FUSE I/O error", func(t *testing.T) {
		t.Parallel()
		prober := NewVolumeHealthProber("/mnt/test-eio", "")
		prober.statxFunc = func(_ int, _ string, _ int, _ int, _ *unix.Statx_t) error {
			return unix.EIO
		}

		res := prober.executeProbe()
		if !res.Unhealthy {
			t.Errorf("expected Unhealthy=true on EIO, got false")
		}
		if res.Reason != ReasonFuseIOError {
			t.Errorf("expected Reason=%q, got %q", ReasonFuseIOError, res.Reason)
		}
	})

	t.Run("deadlock timeout and single-flight guard prevents thread leak", func(t *testing.T) {
		t.Parallel()
		unblockCh := make(chan struct{})
		var calls atomic.Int32

		prober := NewVolumeHealthProber("/mnt/test-deadlock", "")
		prober.probeTimeout = 30 * time.Millisecond
		prober.statxFunc = func(_ int, _ string, _ int, _ int, _ *unix.Statx_t) error {
			calls.Add(1)
			<-unblockCh
			return nil
		}

		// First probe times out while statxFunc remains blocked.
		res1 := prober.executeProbe()
		if !res1.Unhealthy || res1.Reason != ReasonTimeout {
			t.Fatalf("expected first probe to time out with Reason=%q, got %+v", ReasonTimeout, res1)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("expected 1 statx call, got %d", got)
		}

		// Second probe while the first is still blocked must immediately return ReasonTimeout
		// without spawning a second blocked statx goroutine.
		res2 := prober.executeProbe()
		if !res2.Unhealthy || res2.Reason != ReasonTimeout {
			t.Fatalf("expected second probe to return Reason=%q, got %+v", ReasonTimeout, res2)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("expected single-flight guard to prevent second statx call while first is blocked, got %d calls", got)
		}

		// Unblock the first statx goroutine and verify subsequent probes execute normally.
		close(unblockCh)
		deadline := time.Now().Add(time.Second)
		for prober.inFlight.Load() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		res3 := prober.executeProbe()
		if res3.Unhealthy || res3.Reason != ReasonNone {
			t.Fatalf("expected third probe after unblocking to succeed, got %+v", res3)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("expected 2 total statx calls after unblocking, got %d", got)
		}
	})
}

func TestVolumeHealthProberInitializationLatching(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	readyFile := filepath.Join(tempDir, util.ReadyFileName)
	mountPath := filepath.Join(tempDir, "mnt")

	var currentErr error
	var calls atomic.Int32
	prober := NewVolumeHealthProber(mountPath, readyFile)
	prober.statxFunc = func(_ int, _ string, _ int, _ int, _ *unix.Statx_t) error {
		calls.Add(1)
		return currentErr
	}

	// 1. Before ready file exists, Result() returns (_, false) and does not call statx.
	if _, ok := prober.Result(); ok {
		t.Fatalf("expected Result() to return false before ready file exists")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("expected 0 statx calls before ready file exists, got %d", got)
	}

	// 2. Create ready file; Result() should now probe and report healthy.
	if err := os.WriteFile(readyFile, nil, 0o644); err != nil {
		t.Fatalf("failed to write ready file: %v", err)
	}
	res, ok := prober.Result()
	if !ok || res.Unhealthy || res.Reason != ReasonNone {
		t.Fatalf("expected healthy probe result once ready file exists, got res=%+v ok=%v", res, ok)
	}

	// 3. Simulate gcsfuse crash: remove ready file and make statx return ENOTCONN.
	// Because initialization was latched, subsequent probes must still run and detect ENOTCONN.
	if err := os.Remove(readyFile); err != nil {
		t.Fatalf("failed to remove ready file: %v", err)
	}
	currentErr = unix.ENOTCONN
	if _, ok := prober.probeIfInitialized(); !ok {
		t.Fatalf("expected probeIfInitialized() to remain active after ready file removal")
	}
	res, ok = prober.Result()
	if !ok || !res.Unhealthy || res.Reason != ReasonDisconnected {
		t.Fatalf("expected ENOTCONN crash result after ready file removal, got res=%+v ok=%v", res, ok)
	}
}

func TestVolumeHealthProberPeriodicLoop(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	readyFile := filepath.Join(tempDir, util.ReadyFileName)
	if err := os.WriteFile(readyFile, nil, 0o644); err != nil {
		t.Fatalf("failed to write ready file: %v", err)
	}

	var calls atomic.Int32
	prober := NewVolumeHealthProber(tempDir, readyFile)
	prober.probeInterval = 15 * time.Millisecond
	prober.statxFunc = func(_ int, _ string, _ int, _ int, _ *unix.Statx_t) error {
		calls.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	prober.Start(ctx)

	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	if got := calls.Load(); got < 2 {
		t.Fatalf("expected periodic prober to execute at least 2 probes, got %d", got)
	}
}

func TestEndToEndMountHealthAndExitCodeMetrics(t *testing.T) {
	t.Parallel()

	podUID := "pod-uid-telemetry"
	volumeName := "vol-telemetry"
	podNamespace := "ml-training"
	podName := "trainer-0"
	bucketName := "jane-street-bucket"

	testCases := []struct {
		name               string
		enableVolumeSchema bool
		sequentialMetrics  bool
		probeErr           error
		exitDetail         *ExitDetail
		wantMetrics        []string
	}{
		{
			name:               "volume schema enabled + crashed sidecar emits volume_mount_unhealthy and volume_exit_code even without sidecar socket",
			enableVolumeSchema: true,
			sequentialMetrics:  true,
			probeErr:           unix.ENOTCONN,
			exitDetail: &ExitDetail{
				ExitCode:     137,
				ExitReason:   "OOM_KILLED",
				OOMKillCount: 2,
			},
			wantMetrics: []string{
				`volume_mount_unhealthy{reason="enotconn_crashed",bucket_name="jane-street-bucket",k8s_controller_name="trainer-job",k8s_controller_type="jobset",namespace_name="ml-training",pod_name="trainer-0",volume_name="vol-telemetry"} 1`,
				`volume_exit_code{exit_code="137",exit_reason="OOM_KILLED",bucket_name="jane-street-bucket",k8s_controller_name="trainer-job",k8s_controller_type="jobset",namespace_name="ml-training",pod_name="trainer-0",volume_name="vol-telemetry"} 1`,
			},
		},
		{
			name:               "legacy pod schema + deadlocked mount emits mount_unhealthy with pod_uid via Gather()",
			enableVolumeSchema: false,
			sequentialMetrics:  false,
			probeErr:           nil,
			exitDetail: &ExitDetail{
				ExitCode:   139,
				ExitReason: "SIGSEGV_CRASH",
			},
			wantMetrics: []string{
				`mount_unhealthy{bucket_name="jane-street-bucket",namespace_name="ml-training",pod_name="trainer-0",pod_uid="",reason="none",volume_name="vol-telemetry"} 0`,
				`exit_code{bucket_name="jane-street-bucket",exit_code="139",exit_reason="SIGSEGV_CRASH",namespace_name="ml-training",pod_name="trainer-0",pod_uid="",volume_name="vol-telemetry"} 1`,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fuseSocketDir := newSocketTempDir(t)
			emptyDirBasePath := filepath.Join(t.TempDir(), "emptyDir")
			if err := os.MkdirAll(emptyDirBasePath, 0o755); err != nil {
				t.Fatalf("failed to create emptyDirBasePath: %v", err)
			}
			mountPath := filepath.Join(t.TempDir(), "mount")
			if err := os.MkdirAll(mountPath, 0o755); err != nil {
				t.Fatalf("failed to create mountPath: %v", err)
			}

			// Mark volume as having completed startup initialization.
			if err := os.WriteFile(filepath.Join(emptyDirBasePath, util.ReadyFileName), nil, 0o644); err != nil {
				t.Fatalf("failed to write ready file: %v", err)
			}

			if tc.exitDetail != nil {
				data, err := json.Marshal(tc.exitDetail)
				if err != nil {
					t.Fatalf("failed to marshal exit detail: %v", err)
				}
				if err := os.WriteFile(filepath.Join(emptyDirBasePath, util.ExitDetailFileName), data, 0o644); err != nil {
					t.Fatalf("failed to write exit detail file: %v", err)
				}
			}

			fakeClient := clientset.NewFakeClientset()
			fakeClient.CreatePod(clientset.FakePodConfig{
				Name:      podName,
				Namespace: podNamespace,
				UID:       types.UID(podUID),
				PodStatus: &corev1.PodStatus{Phase: corev1.PodRunning},
			})

			mm := NewMetricsManager(":0", fuseSocketDir, 10, fakeClient, true, tc.sequentialMetrics, tc.enableVolumeSchema).(*manager)
			mm.RegisterMetricsCollector(mountPath, podNamespace, podName, bucketName, "node-1", emptyDirBasePath, podUID, volumeName, controllerTypeJobSet, "trainer-job")
			defer mm.UnregisterMetricsCollector(mountPath, "node-1", podUID, volumeName)

			if len(mm.collectors) != 1 {
				t.Fatalf("expected 1 registered collector, got %d", len(mm.collectors))
			}
			probeErr := tc.probeErr
			mm.collectors[0].healthProber.statxFunc = func(_ int, _ string, _ int, _ int, _ *unix.Statx_t) error {
				return probeErr
			}

			// Note: No metrics.sock server is started, simulating a crashed gcsfuse daemon whose
			// metrics endpoint is unreachable.
			req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
			rec := httptest.NewRecorder()
			mm.metricsHandler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, wantLine := range tc.wantMetrics {
				if !strings.Contains(body, wantLine) {
					t.Errorf("expected metrics output to contain:\n  %s\ngot:\n%s", wantLine, body)
				}
			}
		})
	}
}
