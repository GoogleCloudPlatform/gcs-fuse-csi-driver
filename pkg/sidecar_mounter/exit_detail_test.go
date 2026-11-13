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
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
)

func TestExtractExitDetail(t *testing.T) {
	origPaths := cgroupOOMEventPaths
	t.Cleanup(func() {
		cgroupOOMEventPaths = origPaths
	})

	tempDir := t.TempDir()
	cgroupZeroFile := filepath.Join(tempDir, "memory.events.zero")
	if err := os.WriteFile(cgroupZeroFile, []byte("low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n"), 0o644); err != nil {
		t.Fatalf("failed to write cgroup zero file: %v", err)
	}
	cgroupOOMFile := filepath.Join(tempDir, "memory.events.oom")
	if err := os.WriteFile(cgroupOOMFile, []byte("low 0\nhigh 0\nmax 5\noom 2\noom_kill 2\noom_group_kill 0\n"), 0o644); err != nil {
		t.Fatalf("failed to write cgroup oom file: %v", err)
	}
	cgroupV1OOMFile := filepath.Join(tempDir, "memory.oom_control")
	if err := os.WriteFile(cgroupV1OOMFile, []byte("oom_kill_disable 0\nunder_oom 0\noom_kill 4\n"), 0o644); err != nil {
		t.Fatalf("failed to write cgroup v1 oom file: %v", err)
	}

	testCases := []struct {
		name           string
		cgroupPaths    []string
		makeErr        func(t *testing.T) error
		wantExitCode   int
		wantExitReason string
		wantOOMKillCnt uint64
	}{
		{
			name:           "normal exit (nil error)",
			cgroupPaths:    []string{cgroupZeroFile},
			makeErr:        func(_ *testing.T) error { return nil },
			wantExitCode:   0,
			wantExitReason: ExitReasonNormal,
			wantOOMKillCnt: 0,
		},
		{
			name:           "non-ExitError returns EXEC_WAIT_ERROR",
			cgroupPaths:    []string{cgroupZeroFile},
			makeErr:        func(_ *testing.T) error { return errors.New("wait failure") },
			wantExitCode:   -1,
			wantExitReason: ExitReasonExecWaitError,
			wantOOMKillCnt: 0,
		},
		{
			name:        "SIGKILL with cgroup v2 oom_kill > 0 returns OOM_KILLED (137)",
			cgroupPaths: []string{cgroupOOMFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "kill -KILL $$").Run()
			},
			wantExitCode:   137,
			wantExitReason: ExitReasonOOMKilled,
			wantOOMKillCnt: 2,
		},
		{
			name:        "SIGKILL with cgroup v1 fallback oom_kill > 0 returns OOM_KILLED (137)",
			cgroupPaths: []string{filepath.Join(tempDir, "nonexistent"), cgroupV1OOMFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "kill -KILL $$").Run()
			},
			wantExitCode:   137,
			wantExitReason: ExitReasonOOMKilled,
			wantOOMKillCnt: 4,
		},
		{
			name:        "SIGKILL with oom_kill == 0 returns SIGKILL_EXTERNAL (137)",
			cgroupPaths: []string{cgroupZeroFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "kill -KILL $$").Run()
			},
			wantExitCode:   137,
			wantExitReason: ExitReasonSigkillExternal,
			wantOOMKillCnt: 0,
		},
		{
			name:        "SIGSEGV returns SIGSEGV_CRASH (139)",
			cgroupPaths: []string{cgroupZeroFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "kill -SEGV $$").Run()
			},
			wantExitCode:   139,
			wantExitReason: ExitReasonSigsegvCrash,
			wantOOMKillCnt: 0,
		},
		{
			name:        "SIGTERM returns SIGTERM_GRACEFUL (143)",
			cgroupPaths: []string{cgroupZeroFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "kill -TERM $$").Run()
			},
			wantExitCode:   143,
			wantExitReason: ExitReasonSigtermGraceful,
			wantOOMKillCnt: 0,
		},
		{
			name:        "unhandled signal SIGABRT returns SIGNAL_EXIT (134)",
			cgroupPaths: []string{cgroupZeroFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "kill -ABRT $$").Run()
			},
			wantExitCode:   134,
			wantExitReason: ExitReasonSignalExit,
			wantOOMKillCnt: 0,
		},
		{
			name:        "exit code 2 returns GO_PANIC_OR_RACE",
			cgroupPaths: []string{cgroupZeroFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "exit 2").Run()
			},
			wantExitCode:   2,
			wantExitReason: ExitReasonGoPanicOrRace,
			wantOOMKillCnt: 0,
		},
		{
			name:        "exit code 66 returns TSAN_DATA_RACE",
			cgroupPaths: []string{cgroupZeroFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "exit 66").Run()
			},
			wantExitCode:   66,
			wantExitReason: ExitReasonTSANDataRace,
			wantOOMKillCnt: 0,
		},
		{
			name:        "other non-zero exit code returns EXIT_ERROR",
			cgroupPaths: []string{cgroupZeroFile},
			makeErr: func(_ *testing.T) error {
				return exec.Command("sh", "-c", "exit 1").Run()
			},
			wantExitCode:   1,
			wantExitReason: ExitReasonExitError,
			wantOOMKillCnt: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cgroupOOMEventPaths = tc.cgroupPaths
			err := tc.makeErr(t)
			detail := ExtractExitDetail(err)
			if detail.ExitCode != tc.wantExitCode {
				t.Errorf("ExitCode = %d, want %d", detail.ExitCode, tc.wantExitCode)
			}
			if detail.ExitReason != tc.wantExitReason {
				t.Errorf("ExitReason = %q, want %q", detail.ExitReason, tc.wantExitReason)
			}
			if detail.OOMKillCount != tc.wantOOMKillCnt {
				t.Errorf("OOMKillCount = %d, want %d", detail.OOMKillCount, tc.wantOOMKillCnt)
			}
		})
	}
}

func TestWaitForGcsfuseExitWritesExitDetailAndRemovesReadyFile(t *testing.T) {
	origPaths := cgroupOOMEventPaths
	t.Cleanup(func() {
		cgroupOOMEventPaths = origPaths
	})

	tempDir := t.TempDir()
	cgroupFile := filepath.Join(tempDir, "memory.events")
	if err := os.WriteFile(cgroupFile, []byte("oom_kill 1\n"), 0o644); err != nil {
		t.Fatalf("failed to write cgroup file: %v", err)
	}
	cgroupOOMEventPaths = []string{cgroupFile}

	volDir := filepath.Join(tempDir, "vol")
	if err := os.MkdirAll(volDir, 0o755); err != nil {
		t.Fatalf("failed to create volume dir: %v", err)
	}
	readyFile := filepath.Join(volDir, util.ReadyFileName)
	if err := os.WriteFile(readyFile, nil, 0o644); err != nil {
		t.Fatalf("failed to write ready file: %v", err)
	}

	mc := &MountConfig{
		VolumeName: "test-vol",
		TempDir:    volDir,
	}
	mc.EnsureErrWriter()

	cmd := exec.Command("sh", "-c", "kill -KILL $$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start command: %v", err)
	}

	waitForGcsfuseExit(cmd, mc, nil)

	if _, err := os.Stat(readyFile); !os.IsNotExist(err) {
		t.Errorf("expected ready file to be removed after gcsfuse exit, stat err=%v", err)
	}

	exitFilePath := filepath.Join(volDir, util.ExitDetailFileName)
	data, err := os.ReadFile(exitFilePath)
	if err != nil {
		t.Fatalf("expected exit detail file to be written at %q: %v", exitFilePath, err)
	}

	var got ExitDetail
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to unmarshal exit detail JSON: %v", err)
	}
	if got.ExitCode != 137 || got.ExitReason != ExitReasonOOMKilled || got.OOMKillCount != 1 {
		t.Errorf("unexpected ExitDetail written: %+v", got)
	}

	removeExitDetailFile(mc)
	if _, err := os.Stat(exitFilePath); !os.IsNotExist(err) {
		t.Errorf("expected removeExitDetailFile to delete %q, stat err=%v", exitFilePath, err)
	}
}
