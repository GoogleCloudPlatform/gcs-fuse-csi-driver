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

package driver

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
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

// pathForParam returns the sysfs path for a given parameter.
func pathForParam(name ParamName, major, minor uint32) (string, error) {
	switch name {
	case MaxReadAheadKb:
		return fmt.Sprintf("/sys/class/bdi/%d:%d/read_ahead_kb", major, minor), nil

	case MaxBackgroundRequests:
		return fmt.Sprintf("/sys/fs/fuse/connections/%d/max_background", minor), nil

	case CongestionWindowThreshold:
		return fmt.Sprintf("/sys/fs/fuse/connections/%d/congestion_threshold", minor), nil

	default:
		klog.Warningf("Unknown parameter name %q found in kernel parameters config. Skipping...", name)
		return "", fmt.Errorf("unknown parameter name: %q", name)
	}
}

// writeValue attempts to write a value to a sysfs path. It first tries a direct
// filesystem write (effective if running as root) and falls back to 'sudo tee'
// if a permission error occurs.
// Note: Fallback attempt succeeds only if passwordless sudo is available.
func writeValue(path, value string) error {
	data := []byte(value + "\n")

	// Attempt a direct write first it succeeds if.
	// 1. GCSFuse is running as root
	// 2. GCSFuse has required permissions to modify files.
	err := os.WriteFile(path, data, 0644)

	// If direct write fails with a Permission Denied, attempt sudo fallback.
	if err != nil && os.IsPermission(err) {
		klog.Warningf("Direct write to file path %q failed with error: %v, Attempting to write using sudo..", path, err)
		// -n: non-interactive mode.
		cmd := exec.Command("sudo", "-n", "tee", path)
		cmd.Stdin = strings.NewReader(value + "\n")

		// Capture Stderr to see why sudo/tee failed
		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		if sudoErr := cmd.Run(); sudoErr != nil {
			// Combine the system error with the actual message from stderr
			return fmt.Errorf("sudo error: %v, stderr: %q", sudoErr, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
	return err
}

// monitorKernelParamsFile watches a specific configuration file and applies kernel
// parameter changes (via sysfs) whenever the RequestID in the config file changes.
func monitorKernelParamsFile(ctx context.Context, targetPath string, pollInterval time.Duration) {
	klog.Infof("Starting GCSFuse kernel params monitor for target path %q", targetPath)
	var err error
	var kernelParamsFilePath, emptyDirBasePath, lastKernelParamsRequestID string
	var major, minor uint32
	var config *KernelParamsConfig

	// Ensure we log the termination reason (context cancellation or actual error)
	defer klog.Infof("Stopping GCSFuse kernel params monitor for target path %q:  %v", targetPath, err)

	emptyDirBasePath, err = util.PrepareEmptyDir(targetPath, false)
	if err != nil {
		err = fmt.Errorf("failed to get emptyDir path: %w", err)
		return
	}
	kernelParamsFilePath = filepath.Join(emptyDirBasePath, util.GCSFuseKernelParamsFileName)

	major, minor, err = getDeviceMajorMinor(targetPath)
	if err != nil {
		err = fmt.Errorf("Failed to get device major/minor: %w", err)
		return
	}

	for {
		// Check for file existence to avoid unnecessary parsing attempts.
		if _, statErr := os.Stat(kernelParamsFilePath); statErr != nil {
			// If file is missing, wait for the next interval or exit signal.
			select {
			case <-ctx.Done():
				err = ctx.Err()
				return
			case <-time.After(pollInterval):
				continue
			}
		}

		config, err = parseKernelParamsConfig(kernelParamsFilePath)
		if err != nil {
			err = fmt.Errorf("failed to parse kernel params config: %w", err)
			return
		}

		// Only apply parameters if the RequestID has changed to prevent
		// redundant writes to sysfs.
		if config.RequestID != lastKernelParamsRequestID {
			klog.InfoS("Applying kernel parameters", "target path", targetPath, "kernel config", config)
			for _, param := range config.Parameters {
				path, err := pathForParam(param.Name, major, minor)
				if err != nil {
					klog.Warningf("Skipping parameter %q: %v", param.Name, err)
					continue
				}

				klog.Infof("Updating kernel param %q: to %q for target path %q", param.Name, param.Value, targetPath)
				if err := writeValue(path, param.Value); err != nil {
					klog.Warningf("Failed to write kernel param %q to file path %q for target path %q: %v", param.Name, path, targetPath, err)
				} else {
					klog.Infof("Successfully updated kernel param %q to %q for target path %q", param.Name, param.Value, targetPath)
				}
			}
			// Update state so we don't re-apply these settings in the next tick.
			lastKernelParamsRequestID = config.RequestID
		}

		// Throttle the loop to avoid high CPU usage.
		select {
		case <-ctx.Done():
			err = ctx.Err()
			return
		case <-time.After(pollInterval):
		}
	}
}
