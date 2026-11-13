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

package sidecarmounter

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/metrics"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"k8s.io/klog/v2"
)

// ExitDetail captures what is to be exported to metrics when a GCSFuse process exits.
type ExitDetail = metrics.ExitDetail

const (
	ExitReasonNormal          = "NORMAL_EXIT"
	ExitReasonExecWaitError   = "EXEC_WAIT_ERROR"
	ExitReasonUnknown         = "UNKNOWN"
	ExitReasonOOMKilled       = "OOM_KILLED"
	ExitReasonSigkillExternal = "SIGKILL_EXTERNAL"
	ExitReasonSigsegvCrash    = "SIGSEGV_CRASH"
	ExitReasonSigtermGraceful = "SIGTERM_GRACEFUL"
	ExitReasonSignalExit      = "SIGNAL_EXIT"
	ExitReasonGoPanicOrRace   = "GO_PANIC_OR_RACE"
	ExitReasonTSANDataRace    = "TSAN_DATA_RACE"
	ExitReasonExitError       = "EXIT_ERROR"
)

// cgroupOOMEventPaths lists the cgroup v2 and v1 files inspected for OOM kill events.
var cgroupOOMEventPaths = []string{
	"/sys/fs/cgroup/memory.events",
	"/sys/fs/cgroup/memory/memory.oom_control",
}

// readCgroupOOMKillCount reads the container's cgroup memory event file and returns
// the cumulative oom_kill counter. Returns 0 if the counter cannot be read.
func readCgroupOOMKillCount() uint64 {
	for _, path := range cgroupOOMEventPaths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		count, ok := parseOOMKillCount(f)
		_ = f.Close()
		if ok {
			return count
		}
	}
	return 0
}

func parseOOMKillCount(f *os.File) (uint64, bool) {
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "oom_kill" {
			val, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil {
				return val, true
			}
		}
	}
	return 0, false
}

// ExtractExitDetail inspects the error returned by cmd.Wait() for the GCSFuse child process
// and returns the standardized ExitCode, ExitReason, and cgroup OOMKillCount.
func ExtractExitDetail(waitErr error) ExitDetail {
	rec := ExitDetail{
		ExitCode:     0,
		ExitReason:   ExitReasonNormal,
		OOMKillCount: readCgroupOOMKillCount(),
	}

	if waitErr == nil {
		return rec
	}

	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return ExitDetail{ExitCode: -1, ExitReason: ExitReasonExecWaitError, OOMKillCount: rec.OOMKillCount}
	}

	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return ExitDetail{ExitCode: exitErr.ExitCode(), ExitReason: ExitReasonUnknown, OOMKillCount: rec.OOMKillCount}
	}

	switch {
	case ws.Signaled():
		sig := ws.Signal()
		rec.ExitCode = 128 + int(sig) // Standardize kernel signals to exit code

		switch sig {
		case syscall.SIGKILL: // 137
			if rec.OOMKillCount > 0 {
				rec.ExitReason = ExitReasonOOMKilled // Confirmed customer failure
			} else {
				rec.ExitReason = ExitReasonSigkillExternal
			}
		case syscall.SIGSEGV: // 139
			rec.ExitReason = ExitReasonSigsegvCrash // Confirmed GCSFuse fatal bug
		case syscall.SIGTERM: // 143
			rec.ExitReason = ExitReasonSigtermGraceful
		default:
			rec.ExitReason = ExitReasonSignalExit
		}

	case ws.Exited():
		rec.ExitCode = ws.ExitStatus()

		switch rec.ExitCode {
		case 0:
			rec.ExitReason = ExitReasonNormal
		case 2:
			rec.ExitReason = ExitReasonGoPanicOrRace
		case 66:
			rec.ExitReason = ExitReasonTSANDataRace
		default:
			rec.ExitReason = ExitReasonExitError
		}
	}

	return rec
}

func writeExitDetailFile(mc *MountConfig, detail ExitDetail) {
	if mc == nil || mc.TempDir == "" {
		return
	}
	data, err := json.Marshal(detail)
	if err != nil {
		klog.Errorf("[%v] failed to marshal exit detail: %v", mc.VolumeName, err)
		return
	}
	targetPath := filepath.Join(mc.TempDir, util.ExitDetailFileName)
	tmpPath := targetPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		klog.Errorf("[%v] failed to write temporary exit detail file %q: %v", mc.VolumeName, tmpPath, err)
		return
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		klog.Errorf("[%v] failed to rename exit detail file %q to %q: %v", mc.VolumeName, tmpPath, targetPath, err)
	}
}

func removeExitDetailFile(mc *MountConfig) {
	if mc == nil || mc.TempDir == "" {
		return
	}
	targetPath := filepath.Join(mc.TempDir, util.ExitDetailFileName)
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		klog.Warningf("[%v] failed to remove stale exit detail file %q: %v", mc.VolumeName, targetPath, err)
	}
}
