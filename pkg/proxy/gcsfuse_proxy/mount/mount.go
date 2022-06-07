/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mount

import (
	"fmt"
	"os/exec"
	"strings"

	"k8s.io/klog"
	mount "k8s.io/mount-utils"
	"sigs.k8s.io/gcp-cloud-storage-csi-driver/pkg/util"
)

const (
	// Default mount command mount(8).
	defaultMountCommand = "mount"
	// Error thrown by exec cmd.Run() when process spawned by cmd.Start() completes before cmd.Wait() is called (see - k/k issue #103753)
	errNoChildProcesses = "wait: no child processes"
)

// Mounter provides the default implementation of mount.Interface
// for the linux platform.  This implementation assumes that the
// kubelet is running in the host's root mount namespace.
type Mounter struct {
	defaultMounter mount.Interface
	mounterPath    string
}

// New returns a mount.Interface for the current system.
// It provides options to override the default mounter behavior.
// mounterPath allows using an alternative to `/bin/mount` for mounting.
func New(mounterPath string) mount.Interface {
	return &Mounter{
		defaultMounter: mount.New(""),
		mounterPath:    mounterPath,
	}
}

// Mount mounts source to target as fstype with given options.
// options MUST not contain sensitive material (like passwords).
func (mounter *Mounter) Mount(source string, target string, fstype string, options []string) error {
	return mounter.MountSensitive(source, target, fstype, options, nil)
}

// MountSensitive is the same as Mount() but this method allows
// sensitiveOptions to be passed in a separate parameter from the normal
// mount options and ensures the sensitiveOptions are never logged. This
// method should be used by callers that pass sensitive material (like
// passwords) as mount options.
func (mounter *Mounter) MountSensitive(source string, target string, fstype string, options []string, sensitiveOptions []string) error {
	if !detectCgexec() {
		klog.Error("Mount failed: cgexec is not enabled")
		return fmt.Errorf("mount failed: cgexec is not enabled")
	}
	mountArgs, mountArgsLogStr := mount.MakeMountArgsSensitiveWithMountFlags(source, target, fstype, options, sensitiveOptions, nil /* mountFlags */)
	mountCmd, mountArgs, mountArgsLogStr := AddCgexecSensitive("cgexec", target, defaultMountCommand, mountArgs, mountArgsLogStr)

	// Logging with sensitive mount options removed.
	klog.V(4).Infof("Mounting cmd (%s) with arguments (%s)", mountCmd, mountArgsLogStr)
	command := exec.Command(mountCmd, mountArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		if err.Error() == errNoChildProcesses {
			if command.ProcessState.Success() {
				// We don't consider errNoChildProcesses an error if the process itself succeeded (see - k/k issue #103753).
				return nil
			}
			// Rewrite err with the actual exit error of the process.
			err = &exec.ExitError{ProcessState: command.ProcessState}
		}
		klog.Errorf("Mount failed: %v\nMounting command: %s\nMounting arguments: %s\nOutput: %s\n", err, mountCmd, mountArgsLogStr, string(output))
		return fmt.Errorf("mount failed: %v\nMounting command: %s\nMounting arguments: %s\nOutput: %s",
			err, mountCmd, mountArgsLogStr, string(output))
	}
	return err
}

// detectCgexec returns true if OS enables cgexec
func detectCgexec() bool {
	if _, err := exec.LookPath("cgexec"); err != nil {
		klog.V(2).Infof("Detected OS without cgexec")
		return false
	}
	klog.V(2).Infof("Detected OS with cgexec")
	return true
}

// AddCgexecSensitive adds "cgexec -g cpu,memory:<cgroup>" to given command line
// It also accepts takes a sanitized string containing mount arguments, mountArgsLogStr,
// and returns the string appended to the cgexec command for logging.
func AddCgexecSensitive(cgexecPath, mountName, command string, args []string, mountArgsLogStr string) (string, []string, string) {
	podID, _, _ := util.ParsePodIDVolume(mountName)
	groupArg := fmt.Sprintf("cpu,memory:kubepods/burstable/pod%v", podID)
	cgexecRunArgs := []string{"-g", groupArg, "--", command}
	return cgexecPath, append(cgexecRunArgs, args...), strings.Join(cgexecRunArgs, " ") + " " + mountArgsLogStr
}

// MountSensitiveWithoutSystemd is the same as MountSensitive() but this method disable using systemd mount.
func (mounter *Mounter) MountSensitiveWithoutSystemd(source string, target string, fstype string, options []string, sensitiveOptions []string) error {
	return mounter.defaultMounter.MountSensitiveWithoutSystemd(source, target, fstype, options, sensitiveOptions)
}

// MountSensitiveWithoutSystemdWithMountFlags is the same as MountSensitiveWithoutSystemd() with additional mount flags
func (mounter *Mounter) MountSensitiveWithoutSystemdWithMountFlags(source string, target string, fstype string, options []string, sensitiveOptions []string, mountFlags []string) error {
	return mounter.defaultMounter.MountSensitiveWithoutSystemdWithMountFlags(source, target, fstype, options, sensitiveOptions, mountFlags)
}

// Unmount unmounts given target.
func (mounter *Mounter) Unmount(target string) error {
	return mounter.defaultMounter.Unmount(target)
}

// List returns a list of all mounted filesystems.  This can be large.
// On some platforms, reading mounts directly from the OS is not guaranteed
// consistent (i.e. it could change between chunked reads). This is guaranteed
// to be consistent.
func (mounter *Mounter) List() ([]mount.MountPoint, error) {
	return mounter.defaultMounter.List()
}

// IsLikelyNotMountPoint uses heuristics to determine if a directory
// is not a mountpoint.
// It should return ErrNotExist when the directory does not exist.
// IsLikelyNotMountPoint does NOT properly detect all mountpoint types
// most notably linux bind mounts and symbolic link. For callers that do not
// care about such situations, this is a faster alternative to calling List()
// and scanning that output.
func (mounter *Mounter) IsLikelyNotMountPoint(file string) (bool, error) {
	return mounter.defaultMounter.IsLikelyNotMountPoint(file)
}

// GetMountRefs finds all mount references to pathname, returning a slice of
// paths. Pathname can be a mountpoint path or a normal	directory
// (for bind mount). On Linux, pathname is excluded from the slice.
// For example, if /dev/sdc was mounted at /path/a and /path/b,
// GetMountRefs("/path/a") would return ["/path/b"]
// GetMountRefs("/path/b") would return ["/path/a"]
// On Windows there is no way to query all mount points; as long as pathname is
// a valid mount, it will be returned.
func (mounter *Mounter) GetMountRefs(pathname string) ([]string, error) {
	return mounter.defaultMounter.GetMountRefs(pathname)
}
