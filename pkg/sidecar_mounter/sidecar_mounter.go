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
	"fmt"
	"io"
	"os"
	"os/exec"

	"k8s.io/klog/v2"
)

// Mounter will be used in the sidecar container to invoke gcsfuse.
type Mounter struct {
	mounterPath string
	cmds        []*exec.Cmd
}

// New returns a Mounter for the current system.
// It provides an option to specify the path to gcsfuse binary.
func New(mounterPath string) *Mounter {
	return &Mounter{
		mounterPath: mounterPath,
		cmds:        []*exec.Cmd{},
	}
}

func (m *Mounter) Mount(mc *MountConfig) (*exec.Cmd, error) {
	klog.Infof("start to mount bucket %q for volume %q", mc.BucketName, mc.VolumeName)

	if err := os.MkdirAll(mc.BufferDir+TempDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create temp dir %q: %w", mc.BufferDir+TempDir, err)
	}

	args := []string{"gcsfuse"}

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
	cmd := exec.Cmd{
		Path:       m.mounterPath,
		Args:       args,
		ExtraFiles: []*os.File{os.NewFile(uintptr(mc.FileDescriptor), "/dev/fuse")},
		Stdout:     os.Stdout,
		Stderr:     io.MultiWriter(os.Stderr, mc.ErrWriter),
	}

	m.cmds = append(m.cmds, &cmd)

	return &cmd, nil
}

func (m *Mounter) GetCmds() []*exec.Cmd {
	return m.cmds
}
