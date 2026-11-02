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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"golang.org/x/oauth2"
	"golang.org/x/sys/unix"
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
			readyFile := filepath.Join(mc.TempDir, util.ReadyFileName)
			if err := os.WriteFile(readyFile, nil, 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(tc.cmd, tc.args...)
			if err := cmd.Start(); err != nil {
				t.Fatalf("failed to start cmd: %v", err)
			}
			if tc.terminate {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}

			waitForGcsfuseExit(cmd, mc, nil)
			if _, err := os.Stat(readyFile); !os.IsNotExist(err) {
				t.Errorf("expected the ready file to be removed after gcsfuse exits, but it still exists (stat error: %v)", err)
			}
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

// newPipeFuseFile returns a stand-in for the fuse file that NewMountConfig receives: the write end
// of a pipe, wrapped by newFuseFile, and the read end of that pipe. Reading the pipe reaches io.EOF
// only once every copy of the fuse file, in this process and in any child process, is closed.
func newPipeFuseFile(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	pipe, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	t.Cleanup(func() { _ = pipe.Close() })

	// Why: Dup returns a descriptor without FD_CLOEXEC, like the one received by util.RecvMsg.
	// ForkLock keeps any process from being started before newFuseFile sets FD_CLOEXEC.
	syscall.ForkLock.RLock()
	fd, err := syscall.Dup(int(w.Fd()))
	var fuseFile *os.File
	if err == nil {
		fuseFile = newFuseFile(fd)
	}
	syscall.ForkLock.RUnlock()
	_ = w.Close()
	if err != nil {
		t.Fatalf("failed to dup the pipe write end: %v", err)
	}
	// Closing an os.File that Mount has already closed is a no-op.
	t.Cleanup(func() { _ = fuseFile.Close() })

	return fuseFile, pipe
}

// assertPipeEOF fails the test unless every copy of the pipe's write end gets closed.
func assertPipeEOF(t *testing.T, pipe *os.File) {
	t.Helper()

	if err := pipe.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set the pipe read deadline: %v", err)
	}
	if n, err := pipe.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("got n=%d, err=%v reading the pipe, want io.EOF once every copy of the fuse file is closed", n, err)
	}
}

type hookStorageServiceManager struct {
	storage.ServiceManager
	setupForSidecar func(ctx context.Context) (storage.Service, error)
}

func (h *hookStorageServiceManager) SetupStorageServiceForSidecar(ctx context.Context, _ oauth2.TokenSource, _ string) (storage.Service, error) {
	return h.setupForSidecar(ctx)
}

func TestNewFuseFile(t *testing.T) {
	// Do not call t.Parallel() because this test starts child processes while holding a descriptor without FD_CLOEXEC.

	if f := newFuseFile(-1); f != nil {
		t.Errorf("newFuseFile(-1) = %v, want nil", f)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	syscall.ForkLock.RLock()
	fd, err := syscall.Dup(int(w.Fd()))
	if err != nil {
		syscall.ForkLock.RUnlock()
		t.Fatalf("failed to dup the pipe write end: %v", err)
	}
	flagsBefore, errBefore := unix.FcntlInt(uintptr(fd), syscall.F_GETFD, 0)
	fuseFile := newFuseFile(fd)
	syscall.ForkLock.RUnlock()
	defer fuseFile.Close()

	flagsAfter, errAfter := unix.FcntlInt(uintptr(fd), syscall.F_GETFD, 0)
	if errBefore != nil || errAfter != nil {
		t.Fatalf("F_GETFD failed: %v, %v", errBefore, errAfter)
	}
	if flagsBefore&syscall.FD_CLOEXEC != 0 {
		t.Fatalf("expected the duplicated descriptor to start without FD_CLOEXEC")
	}
	if flagsAfter&syscall.FD_CLOEXEC == 0 {
		t.Errorf("expected newFuseFile to set FD_CLOEXEC on descriptor %d", fd)
	}
	if fuseFile.Fd() != uintptr(fd) || fuseFile.Name() != "/dev/fuse" {
		t.Errorf("got file %q with descriptor %d, want %q with descriptor %d", fuseFile.Name(), fuseFile.Fd(), "/dev/fuse", fd)
	}

	// Other child processes do not inherit the fuse file...
	if err := exec.Command("sh", "-c", fmt.Sprintf("test ! -e /proc/self/fd/%d", fd)).Run(); err != nil {
		t.Errorf("a child process inherited the fuse descriptor %d: %v", fd, err)
	}
	// ...whereas gcsfuse receives it as FD 3 through ExtraFiles.
	gcsfuse := exec.Command("sh", "-c", "test -p /proc/self/fd/3")
	gcsfuse.ExtraFiles = []*os.File{fuseFile}
	if err := gcsfuse.Run(); err != nil {
		t.Errorf("the child process did not receive the fuse file as FD 3: %v", err)
	}
}

func TestMountNilMountConfig(t *testing.T) {
	t.Parallel()

	mounter := New("true")
	if err := mounter.Mount(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil MountConfig")
	}
	mounter.WaitGroup.Wait()
}

func TestMountClosesFuseFileOnError(t *testing.T) {
	// Do not call t.Parallel() because these tests start child processes and observe when every copy
	// of the fuse file is closed.

	testCases := []struct {
		name          string
		cancelCtx     bool
		setup         func(m *Mounter, mc *MountConfig)
		wantErr       string
		wantNoErrFile bool
	}{
		{
			name:    "host network token server error",
			setup:   func(_ *Mounter, mc *MountConfig) { mc.HostNetworkKSAOptIn = true },
			wantErr: "token server identity provider is not set",
		},
		{
			name: "bucket access check error",
			setup: func(m *Mounter, mc *MountConfig) {
				mc.EnableSidecarBucketAccessCheck = true
				mc.SidecarRetryConfig = sidecarRetryConfig{Duration: time.Millisecond, Factor: 1, Cap: time.Millisecond, Steps: 1}
				m.StorageServiceManager = &hookStorageServiceManager{
					setupForSidecar: func(context.Context) (storage.Service, error) {
						return nil, errors.New("simulated storage service failure")
					},
				}
			},
			wantErr: "failed to prepare storage service",
		},
		{
			name: "buffer temp dir error",
			setup: func(_ *Mounter, mc *MountConfig) {
				blockingFile := filepath.Join(mc.TempDir, "blocking-file")
				if err := os.WriteFile(blockingFile, nil, 0o644); err != nil {
					t.Fatalf("failed to write blocking file: %v", err)
				}
				mc.BufferDir = blockingFile
			},
			wantErr: "failed to create temp dir",
		},
		{
			name:  "gcsfuse fails to start",
			setup: func(m *Mounter, mc *MountConfig) { m.mounterPath = filepath.Join(mc.TempDir, "nonexistent-gcsfuse") },
			// Mount returns the error for cmd/sidecar_mounter to write, instead of writing it itself.
			wantErr:       "failed to start gcsfuse",
			wantNoErrFile: true,
		},
		{
			name:          "context canceled before gcsfuse starts",
			cancelCtx:     true,
			wantErr:       "failed to start gcsfuse",
			wantNoErrFile: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			errFilePath := filepath.Join(tempDir, util.ErrorFileName)
			fuseFile, pipe := newPipeFuseFile(t)
			mc := &MountConfig{
				BucketName:      "test-bucket",
				VolumeName:      "test-volume",
				TempDir:         tempDir,
				BufferDir:       filepath.Join(tempDir, "buffer"),
				CacheDir:        filepath.Join(tempDir, "cache"),
				ErrWriter:       NewErrorWriter(errFilePath),
				GcsFuseNumaNode: -1,
				fuseFile:        fuseFile,
			}
			mounter := New("true")
			if tc.setup != nil {
				tc.setup(mounter, mc)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelCtx {
				cancel()
			}

			err := mounter.Mount(ctx, mc)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got error %v, want it to contain %q", err, tc.wantErr)
			}
			mounter.WaitGroup.Wait()

			if mc.fuseFile != nil {
				t.Errorf("expected Mount to take ownership of the fuse file")
			}
			assertPipeEOF(t, pipe)
			if _, err := os.Stat(errFilePath); tc.wantNoErrFile && !os.IsNotExist(err) {
				errMsg, _ := os.ReadFile(errFilePath)
				t.Errorf("expected no error file, got %q (%v)", errMsg, err)
			}
		})
	}
}

func TestMountHandsFuseFileToGcsfuse(t *testing.T) {
	// Do not call t.Parallel() because these tests start child processes and observe when every copy
	// of the fuse file is closed.

	testCases := []struct {
		name       string
		noFuseFile bool
		// gcsfuse is the body of the fake gcsfuse script. STOP_FILE is replaced by a file path.
		gcsfuse string
		// whileRunning, if set, is called after Mount returns.
		whileRunning func(t *testing.T, pipe *os.File, cancel context.CancelFunc, stopFile string)
	}{
		{
			name:    "gcsfuse receives the fuse file as FD 3 and the status pipe as FD 4",
			gcsfuse: "test -p /dev/fd/3 && test -p /dev/fd/4",
		},
		{
			name: "the sidecar's copy of the fuse file is closed when Mount returns",
			gcsfuse: "test -p /dev/fd/3 || exit 1\n" +
				"exec 3>&-\n" +
				"while [ ! -e STOP_FILE ]; do sleep 0.01; done",
			whileRunning: func(t *testing.T, pipe *os.File, _ context.CancelFunc, stopFile string) {
				t.Helper()
				// gcsfuse has closed its copy and is still running, so only the sidecar's copy is left.
				assertPipeEOF(t, pipe)
				if err := os.WriteFile(stopFile, nil, 0o644); err != nil {
					t.Fatalf("failed to write the stop file: %v", err)
				}
			},
		},
		{
			name:    "gcsfuse is stopped when the context is canceled",
			gcsfuse: "exec sleep 30",
			whileRunning: func(_ *testing.T, _ *os.File, cancel context.CancelFunc, _ string) {
				cancel()
			},
		},
		{
			name:       "the status pipe stays FD 4 without a fuse file",
			noFuseFile: true,
			gcsfuse:    "test ! -e /dev/fd/3 && test -p /dev/fd/4",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tempDir := t.TempDir()
			stopFile := filepath.Join(tempDir, "stop")
			gcsfusePath := filepath.Join(tempDir, "gcsfuse.sh")
			script := "#!/bin/sh\n" + strings.ReplaceAll(tc.gcsfuse, "STOP_FILE", stopFile) + "\n"
			if err := os.WriteFile(gcsfusePath, []byte(script), 0o755); err != nil {
				t.Fatalf("failed to write the fake gcsfuse: %v", err)
			}
			errFilePath := filepath.Join(tempDir, util.ErrorFileName)
			mc := &MountConfig{
				BucketName:      "test-bucket",
				VolumeName:      "test-volume",
				TempDir:         tempDir,
				BufferDir:       filepath.Join(tempDir, "buffer"),
				CacheDir:        filepath.Join(tempDir, "cache"),
				ErrWriter:       NewErrorWriter(errFilePath),
				GcsFuseNumaNode: -1,
			}
			var pipe *os.File
			if !tc.noFuseFile {
				mc.fuseFile, pipe = newPipeFuseFile(t)
			}

			mounter := New(gcsfusePath)
			if err := mounter.Mount(ctx, mc); err != nil {
				t.Fatalf("Mount failed: %v", err)
			}
			if tc.whileRunning != nil {
				tc.whileRunning(t, pipe, cancel, stopFile)
			}
			exited := make(chan struct{})
			go func() {
				mounter.WaitGroup.Wait()
				close(exited)
			}()
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Fatal("gcsfuse did not exit")
			}

			if pipe != nil {
				assertPipeEOF(t, pipe)
			}
			if _, err := os.Stat(errFilePath); !os.IsNotExist(err) {
				errMsg, _ := os.ReadFile(errFilePath)
				t.Errorf("expected no error file, got %q (%v)", errMsg, err)
			}
			if _, err := unix.FcntlInt(0, syscall.F_GETFD, 0); err != nil {
				t.Errorf("expected stdin to stay open, got %v", err)
			}
		})
	}
}
