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

	case MaxPagesLimit:
		return "/sys/module/fuse/parameters/max_pages_limit", nil

	case TransparentHugePages:
		return "/sys/kernel/mm/transparent_hugepage/enabled", nil

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

func pollAndApplyKernelParameters(targetPath string) {
	// Poll for the kernel params file existence.
	// We use a timeout to prevent infinite looping if the file is never created.
	const timeout = 90 * time.Second
	const pollInterval = 1 * time.Second
	deadline := time.Now().Add(timeout)
	var fileFound bool
	emptyDirBasePath, err := util.PrepareEmptyDir(targetPath, false)
	if err != nil {
		klog.Warningf("Failed to get empty dir: %v", err)
		return
	}
	kernelParamsFilePath := filepath.Join(emptyDirBasePath, util.GCSFuseKernelParamsFileName)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(kernelParamsFilePath); err == nil {
			fileFound = true
			break
		}
		time.Sleep(pollInterval)
	}

	if !fileFound {
		klog.Warningf("Timed out waiting for kernel params file %q after %v", kernelParamsFilePath, timeout)
		return
	}

	config, err := parseKernelParamsConfig(kernelParamsFilePath)
	if err != nil {
		klog.Warningf("Failed to parse kernel params config: %v", err)
		return
	}

	major, minor, err := getDeviceMajorMinor(targetPath)
	if err != nil {
		klog.Warningf("Failed to get device major/minor for %q: %v", targetPath, err)
		return
	}

	for _, param := range config.Parameters {
		path, err := pathForParam(param.Name, major, minor)
		if err != nil {
			klog.Warningf("Skipping parameter %q: %v", param.Name, err)
			continue
		}

		klog.Infof("Updating kernel param %q: to %q", param.Name, param.Value)
		if err := writeValue(path, param.Value); err != nil {
			klog.Warningf("Failed to write kernel param %q to %q: %v", param.Name, path, err)
		}
		klog.Infof("Successfully updated kernel param %q: to %q", param.Name, param.Value)
	}
}
