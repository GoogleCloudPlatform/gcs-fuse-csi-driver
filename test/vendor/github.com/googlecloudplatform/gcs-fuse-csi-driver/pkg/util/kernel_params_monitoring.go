/*
Copyright 2026 The Kubernetes Authors.
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

package util

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

// getDeviceMajorMinor returns the major and minor device numbers
// for the filesystem mounted at the given targetPath.
func getDeviceMajorMinor(targetPath string) (major uint32, minor uint32, err error) {

	fileInfo, err := os.Stat(targetPath)
	if err != nil {
		err = fmt.Errorf("os.Stat: %w", err)
		return
	}

	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		err = fmt.Errorf("fileInfo.Sys() is not of type *syscall.Stat_t")
		return
	}

	devID := stat.Dev
	major = unix.Major(uint64(devID))
	minor = unix.Minor(uint64(devID))
	return
}

// checkAndApplyKernelParams checks for the existence of the kernel parameters file,
// parses the configuration, and applies the parameters to the system if they differ
// from the current values.
func checkAndApplyKernelParams(kernelParamsFilePath string, pathForParam map[ParamName]string, logPrefix string) error {
	// Check for file existence to avoid unnecessary parsing attempts.
	if _, statErr := os.Stat(kernelParamsFilePath); statErr != nil {
		// If file is missing, wait for the next interval.
		return nil
	}

	config, err := parseKernelParamsConfig(kernelParamsFilePath)
	if err != nil {
		return fmt.Errorf("failed to parse kernel params config: %w", err)
	}

	for _, param := range config.Parameters {
		path, ok := pathForParam[param.Name]
		if !ok {
			klog.Warningf("%v Unknown parameter name %q found in kernel parameters config for requestID %q. Skipping...", logPrefix, param.Name, config.RequestID)
			continue
		}
		currValBytes, err := os.ReadFile(path)
		if err != nil {
			klog.Warningf("%v Failed to read kernel parameter %q from file path %q: %v", logPrefix, param.Name, path, err)
			continue
		}
		currVal := strings.TrimSpace(string(currValBytes))
		if currVal != param.Value {
			klog.Infof("%v Updating kernel param %q: from current value %q to new value %q for requestID %q", logPrefix, param.Name, currVal, param.Value, config.RequestID)
			if err := os.WriteFile(path, []byte(param.Value+"\n"), 0o644); err != nil {
				klog.Warningf("%v Failed to write kernel param %q to file path %q for requestID: %q, err: %v", logPrefix, param.Name, path, config.RequestID, err)
			} else {
				klog.Infof("%v Successfully updated kernel param %q to %q for requestID %q", logPrefix, param.Name, param.Value, config.RequestID)
			}
		}
	}
	return nil
}

// MonitorKernelParamsFile monitors the kernel params file and continously enforces
// kernel parameter changes at regular interval as requested by GCSFuse.
func MonitorKernelParamsFile(ctx context.Context, targetPath string, interval time.Duration) {
	podID, volumeName, err := ParsePodIDVolumeFromTargetpath(targetPath)
	if err != nil {
		klog.Warningf("Aborting kernel params monitor. Could not parse Pod ID and Volume Name from target path %q: %v", targetPath, err)
		return
	}
	logPrefix := fmt.Sprintf("Kernel Params Monitor[Pod %v, Volume %v]", podID, volumeName)

	klog.Infof("%v Starting GCSFuse kernel params monitor", logPrefix)

	var terminationError error
	// We log the termination reason for normal terminal due to unmount or due to terminal error.
	// On termination the monitoring go routine is restarted only after GCSFuse is re-mounted or driver is started.
	// TODO(mohit): Ensure terminal failures from the monitoring gorotuine are counted in NodePublishVolume SLO.
	defer func() {
		if terminationError != nil && terminationError != context.Canceled {
			klog.Warningf("%v Stopping GCSFuse kernel params monitor, err: %v", logPrefix, terminationError)
		} else {
			klog.Infof("%v Stopping GCSFuse kernel params monitor", logPrefix)
		}
	}()

	emptyDirBasePath, err := PrepareEmptyDir(targetPath, false)
	if err != nil {
		terminationError = fmt.Errorf("failed to get emptyDir path: %w", err)
		return
	}
	kernelParamsFilePath := filepath.Join(emptyDirBasePath, GCSFuseKernelParamsFileName)

	major, minor, err := getDeviceMajorMinor(targetPath)
	if err != nil {
		terminationError = fmt.Errorf("failed to get device major/minor: %w", err)
		return
	}
	// Setup one time mapping for kernel parameter name to sysfs path for easier lookup.
	pathForParam := map[ParamName]string{
		MaxReadAheadKb:            fmt.Sprintf("/sys/class/bdi/%d:%d/read_ahead_kb", major, minor),
		MaxBackgroundRequests:     fmt.Sprintf("/sys/fs/fuse/connections/%d/max_background", minor),
		CongestionWindowThreshold: fmt.Sprintf("/sys/fs/fuse/connections/%d/congestion_threshold", minor),
	}

	// Perform an initial check before entering the ticker loop to ensure
	// kernel parameters are applied immediately. This is critical for
	// workloads that are I/O heavy at startup.
	if err := checkAndApplyKernelParams(kernelParamsFilePath, pathForParam, logPrefix); err != nil {
		terminationError = err
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			terminationError = ctx.Err()
			return
		case <-ticker.C:
			// Continue monitoring and applying changes periodically.
			if err := checkAndApplyKernelParams(kernelParamsFilePath, pathForParam, logPrefix); err != nil {
				terminationError = err
				return
			}
		}
	}
}
