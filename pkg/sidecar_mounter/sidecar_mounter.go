/*
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
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

// Interface defines the set of methods to allow for gcsfuse mount operations on a system.
type Interface interface {
	// Mount mounts bucket using a file descriptor passed via a unix domain socket.
	Mount(mc *MountConfig) (*exec.Cmd, error)
	// GetCmds returns a list of gcsfuse process cmds.
	GetCmds() []*exec.Cmd
}

// Mounter provides the default implementation of sidecarmounter.Interface
// for the linux platform. This implementation assumes that the
// gcsfuse is installed in the host's root mount namespace.
type Mounter struct {
	mounterPath string
	cmds        []*exec.Cmd
}

// New returns a sidecarmounter.Interface for the current system.
// It provides an option to specify the path to `gcsfuse` binary.
func New(mounterPath string) Interface {
	return &Mounter{
		mounterPath: mounterPath,
		cmds:        []*exec.Cmd{},
	}
}

// MountConfig contains the information gcsfuse needs.
type MountConfig struct {
	FileDescriptor int       `json:"-"`
	VolumeName     string    `json:"volume_name,omitempty"`
	BucketName     string    `json:"bucket_name,omitempty"`
	TempDir        string    `json:"-"`
	Options        []string  `json:"options,omitempty"`
	Stdout         io.Writer `json:"-"`
	Stderr         io.Writer `json:"-"`
}

func (m *Mounter) Mount(mc *MountConfig) (*exec.Cmd, error) {
	klog.Infof("start to mount bucket %q for volume %q", mc.BucketName, mc.VolumeName)

	if err := os.MkdirAll(mc.TempDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create temp dir %q: %v", mc.TempDir, err)
	}

	args := []string{
		"--implicit-dirs",
		"--app-name",
		"gke-gcs-csi",
		"--temp-dir",
		mc.TempDir,
		"--foreground",
		"--log-file",
		"/dev/fd/1", // redirect the output to cmd stdout
		"--log-format",
		"text",
	}
	args = append(args, validateMountArgs(mc.VolumeName, mc.Options)...)
	args = append(args, mc.BucketName)
	// gcsfuse supports the `/dev/fd/N` syntax
	// the /dev/fuse is passed as ExtraFiles below, and will always be FD 3
	args = append(args, "/dev/fd/3")

	klog.Infof("gcsfuse mounting with args %v...", args)
	cmd := exec.Cmd{
		Path:       m.mounterPath,
		Args:       args,
		ExtraFiles: []*os.File{os.NewFile(uintptr(mc.FileDescriptor), "/dev/fuse")},
		Stdout:     mc.Stdout,
		Stderr:     mc.Stderr,
	}

	m.cmds = append(m.cmds, &cmd)
	return &cmd, nil
}

func (m *Mounter) GetCmds() []*exec.Cmd {
	return m.cmds
}

func validateMountArgs(volumeName string, args []string) []string {
	allowedBoolFlags := map[string]bool{
		"debug_gcs":     true,
		"debug_fuse":    true,
		"debug_http":    true,
		"debug_fs":      true,
		"debug_mutex":   true,
		"disable-http2": true,
	}
	allowedIntFlags := map[string]bool{
		"uid":                true,
		"gid":                true,
		"max-conns-per-host": true,
	}

	validatedArgs := []string{}
	invalidArgs := []string{}
	for _, arg := range args {
		argPair := strings.Split(arg, "=")
		switch len(argPair) {
		case 1:
			if ok, prs := allowedBoolFlags[argPair[0]]; prs && ok {
				validatedArgs = append(validatedArgs, "--"+argPair[0])
			} else {
				invalidArgs = append(invalidArgs, arg)
			}
		case 2:
			if ok, prs := allowedIntFlags[argPair[0]]; prs && ok {
				if _, err := strconv.Atoi(argPair[1]); err == nil {
					validatedArgs = append(validatedArgs, "--"+argPair[0])
					validatedArgs = append(validatedArgs, argPair[1])
				} else {
					invalidArgs = append(invalidArgs, arg)
				}
			} else {
				invalidArgs = append(invalidArgs, arg)
			}
		default:
			invalidArgs = append(invalidArgs, arg)
		}
	}

	if len(invalidArgs) > 0 {
		klog.Warningf("got invalid args %v for volume %q, will discard these args and continue to mount.", invalidArgs, volumeName)
	}

	return validatedArgs
}
