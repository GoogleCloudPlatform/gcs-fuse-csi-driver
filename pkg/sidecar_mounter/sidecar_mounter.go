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
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

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

	// when the ctx.Done() is closed,
	// the main workload containers have exited,
	// so it is safe to force kill the gcsfuse process.
	go func(cmd *exec.Cmd) {
		<-ctx.Done()
		time.Sleep(time.Second * 5)
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			klog.Warningf("after 5 seconds, process with id %v has not exited, force kill the process", cmd.Process.Pid)
			if err := cmd.Process.Kill(); err != nil {
				klog.Warningf("failed to force kill process with id %v", cmd.Process.Pid)
			}
		}
	}(cmd)

	m.WaitGroup.Add(1)
	go func() {
		defer m.WaitGroup.Done()
		if err := cmd.Start(); err != nil {
			mc.ErrWriter.WriteMsg(fmt.Sprintf("failed to start gcsfuse with error: %v\n", err))

			return
		}

		klog.Infof("gcsfuse for bucket %q, volume %q started with process id %v", mc.BucketName, mc.VolumeName, cmd.Process.Pid)

		loggingSeverity := mc.ConfigFileFlagMap["logging:severity"]
		if loggingSeverity == "debug" || loggingSeverity == "trace" {
			go logMemoryUsage(ctx, cmd.Process.Pid)
			go logVolumeUsage(ctx, mc.BufferDir, mc.CacheDir)
		}

		promPort := mc.FlagMap["prometheus-port"]
		if promPort != "0" {
			klog.Infof("start to collect metrics from port %v for volume %q", promPort, mc.VolumeName)
			go collectMetrics(ctx, promPort, mc.TempDir)
		}

		// Since the gcsfuse has taken over the file descriptor,
		// closing the file descriptor to avoid other process forking it.
		syscall.Close(mc.FileDescriptor)
		if err := cmd.Wait(); err != nil {
			errMsg := fmt.Sprintf("gcsfuse exited with error: %v\n", err)
			if strings.Contains(errMsg, "signal: terminated") {
				klog.Infof("[%v] gcsfuse was terminated.", mc.VolumeName)
			} else {
				mc.ErrWriter.WriteMsg(errMsg)
			}
		} else {
			klog.Infof("[%v] gcsfuse exited normally.", mc.VolumeName)
		}
	}()

	return nil
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

// collectMetrics collects metrics from the gcsfuse instance every 10 seconds.
func collectMetrics(ctx context.Context, port, dirPath string) {
	metricEndpoint := "http://localhost:" + port + "/metrics"
	outputPath := dirPath + "/metrics.prom"
	ticker := time.NewTicker(10 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			scrapeMetrics(newCtx, metricEndpoint, outputPath)
			cancel()
		}
	}
}

// scrapeMetrics connects to the metrics endpoint, scrapes metrics, and save the metrics to the given file path.
func scrapeMetrics(ctx context.Context, metricEndpoint, outputPath string) {
	// Make the HTTP GET request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricEndpoint, nil)
	if err != nil {
		klog.Errorf("failed to create HTTP request to %q: %v", metricEndpoint, err)

		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		klog.Errorf("failed to make HTTP request to %q: %v", metricEndpoint, err)

		return
	}
	defer resp.Body.Close() // Ensure closure of response body

	// Check for a successful HTTP status code
	if resp.StatusCode != http.StatusOK {
		klog.Errorf("unexpected HTTP status: %v", resp.Status)

		return
	}

	// Create the output file
	out, err := os.Create(outputPath)
	if err != nil {
		klog.Errorf("error creating output file: %v", err)

		return
	}
	defer out.Close() // Ensure closure of output file

	// Copy the response body (file content) to our output file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		klog.Errorf("error writing to output file: %v", err)

		return
	}
}
