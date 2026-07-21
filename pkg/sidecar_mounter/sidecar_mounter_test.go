/*
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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMain(m *testing.M) {
	gracefulKillGracePeriod = 10 * time.Millisecond
	os.Exit(m.Run())
}

func TestCreateBufferTempDir(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	blockingFile := filepath.Join(tempDir, "blocking-file")
	if err := os.WriteFile(blockingFile, []byte("block"), 0644); err != nil {
		t.Fatalf("failed to write blocking file: %v", err)
	}

	testCases := []struct {
		name        string
		mc          *MountConfig
		expectedErr bool
	}{
		{
			name: "should create valid buffer temp dir",
			mc: &MountConfig{
				BufferDir: tempDir,
			},
			expectedErr: false,
		},
		{
			name: "should fail when buffer dir path is blocked by a file",
			mc: &MountConfig{
				BufferDir: blockingFile,
			},
			expectedErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := createBufferTempDir(tc.mc)
			if (err != nil) != tc.expectedErr {
				t.Errorf("Got error %v, but expected error %v", err, tc.expectedErr)
			}
			if !tc.expectedErr {
				expectedDir := filepath.Join(tc.mc.BufferDir, TempDir)
				info, err := os.Stat(expectedDir)
				if err != nil || !info.IsDir() {
					t.Errorf("expected directory %q to exist, got err: %v", expectedDir, err)
				}
			}
		})
	}
}

func TestWaitForGcsfuseExit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		cmd       string
		args      []string
		terminate bool
	}{
		{
			name:      "should log normal exit",
			cmd:       "true",
			args:      []string{},
			terminate: false,
		},
		{
			name:      "should write error to ErrWriter when command exits with error",
			cmd:       "false",
			args:      []string{},
			terminate: false,
		},
		{
			name:      "should log terminated exit when SIGTERM is sent",
			cmd:       "sleep",
			args:      []string{"10"},
			terminate: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mc := &MountConfig{
				VolumeName: "test-vol",
				TempDir:    t.TempDir(),
			}
			mc.EnsureErrWriter()

			cmd := exec.Command(tc.cmd, tc.args...)
			if err := cmd.Start(); err != nil {
				t.Fatalf("failed to start cmd: %v", err)
			}
			if tc.terminate {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}

			waitForGcsfuseExit(cmd, mc)
		})
	}
}

func TestForceKillCmd(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		cmd              string
		args             []string
		startOnly        bool
		expectedExit     bool
		skipInShortTests bool
	}{
		{
			name:             "should handle already exited process without error",
			cmd:              "true",
			args:             []string{},
			startOnly:        false,
			expectedExit:     true,
			skipInShortTests: false,
		},
		{
			name:             "should force kill running process after timeout",
			cmd:              "sleep",
			args:             []string{"30"},
			startOnly:        true,
			expectedExit:     true,
			skipInShortTests: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipInShortTests && testing.Short() {
				t.Skip("skipping long sleep test in short mode")
			}
			cmd := exec.Command(tc.cmd, tc.args...)
			if tc.startOnly {
				if err := cmd.Start(); err != nil {
					t.Fatalf("failed to start cmd: %v", err)
				}
			} else {
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to run cmd: %v", err)
				}
			}

			cmdDone := make(chan struct{})
			if !tc.startOnly {
				close(cmdDone)
			}
			forceKillCmd(cmd, cmdDone)
			if tc.startOnly {
				_ = cmd.Wait()
			}
			if tc.expectedExit && cmd.ProcessState == nil {
				t.Errorf("expected process state to be non-nil after forceKillCmd and Wait")
			}
		})
	}
}

func TestStartVolumeMonitoring(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name              string
		configFileFlagMap map[string]string
		flagMap           map[string]string
	}{
		{
			name: "should start monitoring goroutines when debug logging is enabled",
			configFileFlagMap: map[string]string{
				"logging:severity": "debug",
			},
			flagMap: map[string]string{
				"prometheus-port": "0",
			},
		},
		{
			name: "should start monitoring goroutines when info logging is enabled",
			configFileFlagMap: map[string]string{
				"logging:severity": "info",
			},
			flagMap: map[string]string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			mc := &MountConfig{
				VolumeName:        "test-volume",
				BufferDir:         t.TempDir(),
				CacheDir:          t.TempDir(),
				TempDir:           t.TempDir(),
				ConfigFileFlagMap: tc.configFileFlagMap,
				FlagMap:           tc.flagMap,
			}

			startVolumeMonitoring(ctx, mc, os.Getpid())
		})
	}
}

func TestPrepareMountCommand(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		mounterPath      string
		mc               *MountConfig
		mountPoint       string
		expectedFlags    []string
		expectedLastArgs []string
	}{
		{
			name:        "should prepare command with bucket and mount point positional args",
			mounterPath: "/path/to/gcsfuse",
			mc: &MountConfig{
				BucketName: "my-bucket",
				FlagMap: map[string]string{
					"foreground": "",
					"uid":        "1000",
				},
				GcsFuseNumaNode: -1,
			},
			mountPoint:       "/mnt/target",
			expectedFlags:    []string{"--foreground", "--uid=1000"},
			expectedLastArgs: []string{"my-bucket", "/mnt/target"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mounter := New(tc.mounterPath)
			cmd := mounter.prepareMountCommand(context.Background(), tc.mc, tc.mountPoint)

			if cmd.Path != tc.mounterPath {
				t.Errorf("Got cmd path %q, expected %q", cmd.Path, tc.mounterPath)
			}
			argStr := strings.Join(cmd.Args, " ")
			for _, flag := range tc.expectedFlags {
				if !strings.Contains(argStr, flag) {
					t.Errorf("Got args %v, expected to contain %q", cmd.Args, flag)
				}
			}
			n := len(cmd.Args)
			if n < 2 {
				t.Fatalf("Got %d args, expected at least 2", n)
			}
			lastTwo := cmd.Args[n-2:]
			for i, exp := range tc.expectedLastArgs {
				if lastTwo[i] != exp {
					t.Errorf("Got positional arg %q, expected %q", lastTwo[i], exp)
				}
			}
			if cmd.Cancel == nil {
				t.Errorf("expected cmd.Cancel to be set")
			}
		})
	}
}

func TestMountToNode(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	blockingFile := filepath.Join(tempDir, "blocking")
	if err := os.WriteFile(blockingFile, []byte("data"), 0644); err != nil {
		t.Fatalf("failed to write blocking file: %v", err)
	}
	unmountedPath := filepath.Join(t.TempDir(), "not-mounted")

	dummySleepScript := filepath.Join(tempDir, "dummy-sleep.sh")
	if err := os.WriteFile(dummySleepScript, []byte("#!/bin/sh\nexec sleep 10\n"), 0755); err != nil {
		t.Fatalf("failed to create dummy sleep script: %v", err)
	}

	testCases := []struct {
		name         string
		mounterPath  string
		mc           *MountConfig
		timeout      time.Duration
		expectedCode codes.Code
		acceptCodes  []codes.Code
		expectError  bool
	}{
		{
			name:         "should return Internal error when MountConfig is nil",
			mounterPath:  "true",
			mc:           nil,
			expectedCode: codes.Internal,
			expectError:  true,
		},
		{
			name:        "should return Internal error when SharedMountPoint is empty",
			mounterPath: "true",
			mc: &MountConfig{
				BucketName: "my-bucket",
			},
			expectedCode: codes.Internal,
			expectError:  true,
		},
		{
			name:        "should return error when createBufferTempDir fails",
			mounterPath: "true",
			mc: &MountConfig{
				BucketName:       "my-bucket",
				SharedMountPoint: "/mnt/shared",
				BufferDir:        blockingFile,
			},
			expectError: true,
		},
		{
			name:        "should return Internal error when binary start fails",
			mounterPath: "/nonexistent/binary/path",
			mc: &MountConfig{
				BucketName:       "my-bucket",
				SharedMountPoint: "/mnt/shared",
				BufferDir:        t.TempDir(),
			},
			expectedCode: codes.Internal,
			expectError:  true,
		},
		{
			name:        "should succeed when target path is already mounted",
			mounterPath: dummySleepScript,
			mc: &MountConfig{
				BucketName:       "my-bucket",
				SharedMountPoint: "/",
				BufferDir:        t.TempDir(),
				TempDir:          t.TempDir(),
			},
			timeout:     2 * time.Second,
			expectError: false,
		},
		{
			name:        "should return DeadlineExceeded error when waiting for mount times out",
			mounterPath: dummySleepScript,
			mc: &MountConfig{
				BucketName:       "my-bucket",
				SharedMountPoint: unmountedPath,
				BufferDir:        t.TempDir(),
				TempDir:          t.TempDir(),
				FlagMap:          map[string]string{},
			},
			timeout:      200 * time.Millisecond,
			expectedCode: codes.DeadlineExceeded,
			expectError:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mounter := New(tc.mounterPath)

			mountServerCtx, mountServerCancel := context.WithCancel(context.Background())
			t.Cleanup(func() {
				mountServerCancel()
				mounter.WaitGroup.Wait()
			})

			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}

			err := mounter.MountToNode(ctx, mountServerCtx, tc.mc)

			if (err != nil) != tc.expectError {
				t.Fatalf("Got error %v, but expected error=%v", err, tc.expectError)
			}
			if tc.expectError && tc.expectedCode != codes.OK {
				st, ok := status.FromError(err)
				if !ok || st.Code() != tc.expectedCode {
					t.Errorf("Got error code %v, expected %v", st.Code(), tc.expectedCode)
				}
			}
			if tc.expectError && len(tc.acceptCodes) > 0 {
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("Got error %v, expected gRPC status error", err)
				}
				matched := false
				for _, code := range tc.acceptCodes {
					if st.Code() == code {
						matched = true
						break
					}
				}
				if !matched {
					t.Errorf("Got error code %v, expected one of %v", st.Code(), tc.acceptCodes)
				}
			}
		})
	}
}
