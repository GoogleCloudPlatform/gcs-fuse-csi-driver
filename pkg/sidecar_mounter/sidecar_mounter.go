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
	"strings"

	"k8s.io/klog/v2"
)

const GCSFUSE_APP_NAME = "gke-gcs-fuse-csi"

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

	args, err := prepareMountArgs(mc)
	if err != nil {
		klog.Warningf("got error when preparing the mount args: %v. Will discard invalid args and continue to mount.", err)
	}
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

var disallowedFlags = map[string]bool{
	"implicit-dirs":        true,
	"app-name":             true,
	"temp-dir":             true,
	"foreground":           true,
	"log-file":             true,
	"log-format":           true,
	"key-file":             true,
	"token-url":            true,
	"reuse-token-from-url": true,
	"endpoint":             true,
}

func prepareMountArgs(mc *MountConfig) ([]string, error) {
	args := []string{
		"gcsfuse",
		"--implicit-dirs",
		"--app-name",
		GCSFUSE_APP_NAME,
		"--temp-dir",
		mc.TempDir,
		"--foreground",
		"--log-file",
		"/dev/fd/1", // redirect the output to cmd stdout
		"--log-format",
		"text",
	}

	invalidArgs := []string{}
	for _, arg := range mc.Options {
		argPair := strings.SplitN(arg, "=", 2)
		if len(argPair) == 0 {
			continue
		}

		if !disallowedFlags[argPair[0]] {
			args = append(args, "--"+argPair[0])
		} else {
			invalidArgs = append(invalidArgs, arg)
			continue
		}
		if len(argPair) > 1 {
			args = append(args, argPair[1])
		}
	}

	args = append(args, mc.BucketName)
	// gcsfuse supports the `/dev/fd/N` syntax
	// the /dev/fuse is passed as ExtraFiles below, and will always be FD 3
	args = append(args, "/dev/fd/3")

	var err error
	if len(invalidArgs) > 0 {
		err = fmt.Errorf("got invalid args %v for volume %q.", invalidArgs, mc.VolumeName)
	}
	return args, err
}
