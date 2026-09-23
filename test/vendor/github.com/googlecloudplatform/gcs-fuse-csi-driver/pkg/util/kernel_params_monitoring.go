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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/safchain/ethtool"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

const defaultNIC = "eth0"

// ethtoolClient abstracts github.com/safchain/ethtool for deterministic unit testing.
type ethtoolClient interface {
	Features(intf string) (map[string]bool, error)
	Change(intf string, config map[string]bool) error
	Close()
}

var (
	// fuseMaxMaxPagesMu serializes concurrent updates to the host's FUSE max_pages_limit.
	fuseMaxMaxPagesMu sync.Mutex
	// ProcSysFsFuseMaxPagesLimitPath is the host FUSE max_pages_limit path (overridable for unit testing).
	ProcSysFsFuseMaxPagesLimitPath = "/host-proc-sys-fs-fuse/max_pages_limit"

	// lroMu serializes default NIC LRO checks and updates across concurrent volume monitors.
	lroMu sync.Mutex

	// newEthtoolClient creates a new ethtool ioctl client (overridable for unit testing).
	newEthtoolClient = func() (ethtoolClient, error) {
		return ethtool.NewEthtool()
	}
	// runEthtoolCommandFunc executes the CLI fallback `ethtool -K <nic> lro on` (overridable for unit testing).
	runEthtoolCommandFunc = runEthtoolCommand
	// enableLROFunc enables LRO on the specified NIC (overridable for unit testing).
	enableLROFunc = enableLROOnNIC
)

// FuseMaxMaxPagesUpdateSupported returns true if the host supports FUSE max_pages_limit tuning.
func FuseMaxMaxPagesUpdateSupported() bool {
	_, err := os.Lstat(ProcSysFsFuseMaxPagesLimitPath)
	return err == nil
}

// ReadFuseMaxPagesLimit reads the host's current FUSE max_pages_limit.
func ReadFuseMaxPagesLimit() (int64, error) {
	bytes, err := os.ReadFile(ProcSysFsFuseMaxPagesLimitPath)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(bytes)), 10, 64)
}

// SetFuseMaxPagesLimit writes the target limit to the host's FUSE max_pages_limit file.
func SetFuseMaxPagesLimit(target int64) error {
	return os.WriteFile(ProcSysFsFuseMaxPagesLimitPath, []byte(strconv.FormatInt(target, 10)+"\n"), 0o644)
}

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

// isLROEnabledValue returns true when the validated parameter value signals enabling LRO.
func isLROEnabledValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "on", "1":
		return true
	default:
		return false
	}
}

func runEthtoolCommand(nic string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ethtool", "-K", nic, "lro", "on").CombinedOutput()
	if err == nil {
		return nil
	}
	if sudoOut, sudoErr := exec.CommandContext(ctx, "sudo", "-n", "ethtool", "-K", nic, "lro", "on").CombinedOutput(); sudoErr == nil {
		return nil
	} else {
		return fmt.Errorf("ethtool -K %s lro on failed: %w (output: %s, sudo output: %s)", nic, sudoErr, strings.TrimSpace(string(out)), strings.TrimSpace(string(sudoOut)))
	}
}

// enableLROOnNIC idempotently enables Large Receive Offload (rx-lro / large-receive-offload)
// on the specified NIC using ethtool ioctls (Features read-before-write Change),
// falling back to `ethtool -K <nic> lro on` if ioctl creation or execution fails.
func enableLROOnNIC(nic string) error {
	lroMu.Lock()
	defer lroMu.Unlock()

	nic = strings.TrimSpace(nic)
	if nic == "" {
		return fmt.Errorf("NIC name cannot be empty")
	}

	eth, err := newEthtoolClient()
	if err != nil {
		cmdErr := runEthtoolCommandFunc(nic)
		if cmdErr == nil {
			return nil
		}
		return fmt.Errorf("failed to create ethtool client for NIC %q: %w (fallback error: %v)", nic, err, cmdErr)
	}
	defer eth.Close()

	features, err := eth.Features(nic)
	if err != nil {
		cmdErr := runEthtoolCommandFunc(nic)
		if cmdErr == nil {
			return nil
		}
		return fmt.Errorf("failed to get ethtool features for NIC %q: %w (fallback error: %v)", nic, err, cmdErr)
	}

	featureKey := "rx-lro"
	var alreadyEnabled bool
	if val, ok := features["rx-lro"]; ok {
		featureKey = "rx-lro"
		alreadyEnabled = val
	} else if val, ok := features["large-receive-offload"]; ok {
		featureKey = "large-receive-offload"
		alreadyEnabled = val
	}

	// Idempotent no-op when LRO is already active on the NIC.
	if alreadyEnabled {
		return nil
	}

	if err := eth.Change(nic, map[string]bool{featureKey: true}); err != nil {
		cmdErr := runEthtoolCommandFunc(nic)
		if cmdErr == nil {
			return nil
		}
		return fmt.Errorf("failed to enable %s on NIC %q: %w (fallback error: %v)", featureKey, nic, err, cmdErr)
	}
	return nil
}

func enableLROOnDefaultNIC() error {
	if err := enableLROFunc(defaultNIC); err != nil {
		return fmt.Errorf("failed to enable large-receive-offload on NIC %q: %w", defaultNIC, err)
	}
	return nil
}

// validateParamValue converts the string value to an integer and checks it against safe bounds,
// or validates boolean/toggle values for LargeReceiveOffload.
func validateParamValue(name ParamName, value string) error {
	trimmed := strings.TrimSpace(value)
	if name == LargeReceiveOffload {
		switch strings.ToLower(trimmed) {
		case "true", "false", "on", "off", "1", "0":
			return nil
		default:
			return fmt.Errorf("value %q is not a valid boolean/toggle for %s", value, name)
		}
	}

	valInt, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return fmt.Errorf("value %q is not a valid integer", value)
	}

	// Enforce safe minimum and maximum boundaries for each parameter to prevent node instability.
	switch name {
	case MaxReadAheadKb:
		if valInt < 0 || valInt > 1048576 { // 1 GB
			return fmt.Errorf("value %d is outside safe bounds for %s", valInt, name)
		}
	case MaxBackgroundRequests:
		if valInt < 1 || valInt > 1000 {
			return fmt.Errorf("value %d is outside safe bounds for %s", valInt, name)
		}
	case CongestionWindowThreshold:
		if valInt < 0 || valInt > 1000 {
			return fmt.Errorf("value %d is outside safe bounds for %s", valInt, name)
		}
	default:
		// Fail-closed for unknown parameters
		return fmt.Errorf("validation rules missing for parameter %s", name)
	}

	return nil
}

// checkAndApplyKernelParams checks for the existence of the kernel parameters file,
// parses the configuration, and applies the parameters to the system if they differ
// from the current values.
func checkAndApplyKernelParams(kernelParamsFilePath string, pathForParam map[ParamName]string, logPrefix string) error {
	// Check for file existence to avoid unnecessary parsing attempts. Use Lstat to prevent following symlinks.
	if _, statErr := os.Lstat(kernelParamsFilePath); statErr != nil {
		// If file is missing, wait for the next interval.
		return nil
	}

	config, err := parseKernelParamsConfig(kernelParamsFilePath)
	if err != nil {
		return fmt.Errorf("failed to parse kernel params config: %w", err)
	}

	for _, param := range config.Parameters {
		param.Value = strings.TrimSpace(param.Value)
		if param.Name == LargeReceiveOffload {
			if err := validateParamValue(param.Name, param.Value); err != nil {
				klog.Warningf("%v Invalid value for parameter %q (requestID %q): %v. Skipping...", logPrefix, param.Name, config.RequestID, err)
				continue
			}
			if isLROEnabledValue(param.Value) {
				if err := enableLROOnDefaultNIC(); err != nil {
					klog.Warningf("%v Failed to apply parameter %q (requestID %q): %v. Skipping...", logPrefix, param.Name, config.RequestID, err)
					continue
				}
				klog.Infof("%v Successfully ensured kernel param %q is enabled on default NIC for requestID %q", logPrefix, param.Name, config.RequestID)
			}
			continue
		}

		path, ok := pathForParam[param.Name]
		if !ok {
			klog.Warningf("%v Unknown parameter name %q found in kernel parameters config for requestID %q. Skipping...", logPrefix, param.Name, config.RequestID)
			continue
		}

		if err := validateParamValue(param.Name, param.Value); err != nil {
			klog.Warningf("%v Invalid value for parameter %q (requestID %q): %v. Skipping...", logPrefix, param.Name, config.RequestID, err)
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
func MonitorKernelParamsFile(ctx context.Context, mountPoint, emptyDirBasePath, logPrefix string, interval time.Duration) {
	kernelParamsFilePath := filepath.Join(emptyDirBasePath, GCSFuseKernelParamsFileName)
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

	major, minor, err := getDeviceMajorMinor(mountPoint)
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

// MountUsingElevatedFuseMaxPagesLimit temporarily increases the host's FUSE max_pages_limit if the current limit is lower,
// executes the provided function, and then restores the original limit.
func MountUsingElevatedFuseMaxPagesLimit(targetLimit int64, logPrefix string, fn func() error) error {
	fuseMaxMaxPagesMu.Lock()
	defer fuseMaxMaxPagesMu.Unlock()

	origLimit, err := ReadFuseMaxPagesLimit()
	if err != nil {
		klog.Warningf("%v Failed to read host FUSE max_pages_limit: %v. Proceeding with default kernel limit.", logPrefix, err)
		return fn()
	}

	if origLimit >= targetLimit {
		return fn()
	}

	klog.Infof("%v Temporarily increasing host FUSE max_pages_limit from %d to %d for mount", logPrefix, origLimit, targetLimit)
	if err := SetFuseMaxPagesLimit(targetLimit); err != nil {
		klog.Warningf("%v Failed to set host FUSE max_pages_limit to %d: %v. Proceeding with default kernel limit.", logPrefix, targetLimit, err)
		return fn()
	}

	// TODO(mohit): This approach for restoration suffers from TOCTOU problem and needs to be re-visited before default ON enablement
	// of kernel reader feature in GCSFuse. This restore can overwrite any changes between time when the limit was checked vs the time
	// when limit was restored.
	defer func() {
		klog.Infof("%v Restoring host FUSE max_pages_limit back to %d", logPrefix, origLimit)
		if err := SetFuseMaxPagesLimit(origLimit); err != nil {
			klog.Errorf("%v Failed to restore host FUSE max_pages_limit to %d: %v", logPrefix, origLimit, err)
		}
	}()

	return fn()
}
