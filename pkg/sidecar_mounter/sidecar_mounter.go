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

package sidecarmounter

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"k8s.io/klog/v2"
)

// Mounter will be used in the sidecar container to invoke gcsfuse.
type Mounter struct {
	mounterPath string
	WaitGroup   sync.WaitGroup
}

// New returns a Mounter for the current system.
// It provides an option to specify the path to gcsfuse binary.
func New(mounterPath string) *Mounter {
	return &Mounter{
		mounterPath: mounterPath,
	}
}

func (m *Mounter) Mount(ctx context.Context, mc *MountConfig) error {
	klog.Infof("start to mount bucket %q for volume %q", mc.BucketName, mc.VolumeName)

	if err := os.MkdirAll(mc.BufferDir+TempDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create temp dir %q: %w", mc.BufferDir+TempDir, err)
	}

	errorCh := make(chan interface{})
	var err error

retryLoop:
	for i := 0; i < 3; i++ {
		cmd := m.prepareCmd(ctx, mc)

		m.WaitGroup.Add(1)
		go func() {
			defer m.WaitGroup.Done()
			if err := cmd.Start(); err != nil {
				mc.ErrWriter.WriteMsg(fmt.Sprintf("failed to start gcsfuse with error: %v\n", err))

				return
			}

			klog.Infof("gcsfuse for bucket %q, volume %q started with process id %v", mc.BucketName, mc.VolumeName, cmd.Process.Pid)

			if err := cmd.Wait(); err != nil {
				errMsg := fmt.Sprintf("gcsfuse exited with error: %v\n", err)
				if strings.Contains(errMsg, "signal: terminated") {
					klog.Infof("[%v] gcsfuse was terminated.", mc.VolumeName)
				} else {
					errorCh <- struct{}{}
					mc.ErrWriter.WriteMsg(errMsg)
				}
			} else {
				klog.Infof("[%v] gcsfuse exited normally.", mc.VolumeName)
			}
		}()

		select {
		case <-errorCh:
			// check if the gcsfuse error is retriable
			// if yes, no-ops, and empty the error file
			// if no, return with the error
			errStr := mc.ErrWriter.GetFullMsg()
			if util.IsRetriableErr(errStr) {
				mc.ErrWriter.EmptyErrorFile()
			} else {
				return errors.New(errStr)
			}

		// sleep 2 seconds to
		// 1. wait for gcsfuse errors.
		// 2. avoid different gcsfuse logs mixed together.
		// 3. avoid memory usage peak.
		case <-time.After(2 * time.Second):
			// no errors found in gcsfuse,
			// lanuch monitoring goroutines,
			// and break the retry loop.

			loggingSeverity := mc.ConfigFileFlagMap["logging:severity"]
			if loggingSeverity == "debug" || loggingSeverity == "trace" {
				go logMemoryUsage(ctx, cmd.Process.Pid)
				go logVolumeUsage(ctx, mc.BufferDir, mc.CacheDir)
			}

			go waitAndForceKill(ctx, cmd)

			break retryLoop
		}
	}

	// Since the gcsfuse has taken over the file descriptor,
	// closing the file descriptor to avoid other process forking it.
	syscall.Close(mc.FileDescriptor)

	return err
}

func (m *Mounter) prepareCmd(ctx context.Context, mc *MountConfig) *exec.Cmd {
	args := []string{}
	for k, v := range mc.FlagMap {
		args = append(args, "--"+k)
		if v != "" {
			args = append(args, v)
		}
	}

	args = append(args, mc.BucketName)
	// gcsfuse supports the `/dev/fd/N` syntax
	// the /dev/fuse is passed as ExtraFiles below, and will always be FD 3
	args = append(args, "/dev/fd/3")

	klog.Infof("gcsfuse mounting with args %v...", args)
	//nolint: gosec
	cmd := exec.CommandContext(ctx, m.mounterPath, args...)
	cmd.ExtraFiles = []*os.File{os.NewFile(uintptr(mc.FileDescriptor), "/dev/fuse")}
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, mc.ErrWriter)
	cmd.Cancel = func() error {
		klog.V(4).Infof("sending SIGTERM to gcsfuse process: %v", cmd)

		return cmd.Process.Signal(syscall.SIGTERM)
	}

	return cmd
}

// waitAndForceKill waits for ctx.Done() to be closed and
// force kills the gcsfuse process.
func waitAndForceKill(ctx context.Context, cmd *exec.Cmd) {
	<-ctx.Done()
	time.Sleep(time.Second * 5)
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		klog.Warningf("after 5 seconds, process with id %v has not exited, force kill the process", cmd.Process.Pid)
		if err := cmd.Process.Kill(); err != nil {
			klog.Warningf("failed to force kill process with id %v", cmd.Process.Pid)
		}
	}
}

// logMemoryUsage logs gcsfuse process VmRSS (Resident Set Size) usage every 30 seconds.
func logMemoryUsage(ctx context.Context, pid int) {
	ticker := time.NewTicker(30 * time.Second)
	filepath := fmt.Sprintf("/proc/%d/status", pid)
	file, err := os.Open(filepath)
	if err != nil {
		klog.Errorf("failed to open %v: %v", filepath, err)

		return
	}
	defer file.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := file.Seek(0, io.SeekStart)
			if err != nil {
				klog.Errorf("failed to seek to the file beginning: %v", err)

				return
			}

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "VmRSS:") {
					fields := strings.Fields(line)
					klog.Infof("gcsfuse with PID %v uses VmRSS: %v %v", pid, fields[1], fields[2])

					break
				}
			}
		}
	}
}

// logVolumeUsage logs gcsfuse process buffer and cache volume usage every 30 seconds.
func logVolumeUsage(ctx context.Context, bufferDir, cacheDir string) {
	ticker := time.NewTicker(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// TODO: this method does not work for the buffer dir
			logVolumeTotalSize(bufferDir)
			logVolumeTotalSize(cacheDir)
		}
	}
}

// logVolumeTotalSize logs the total volume size of dirPath.
// Warning: this func uses filepath.Walk func that is less efficient when dealing with very large directory trees.
func logVolumeTotalSize(dirPath string) {
	var totalSize int64

	err := filepath.Walk(dirPath, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}

		return nil
	})

	if err != nil {
		klog.Errorf("failed to calculate volume total size for %q: %v", dirPath, err)
	} else {
		klog.Infof("total volume size of %v: %v bytes", dirPath, totalSize)
	}
}
