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
	"strings"

	"k8s.io/klog/v2"
)

const GCSFuseAppName = "gke-gcs-fuse-csi"

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

// MountConfig contains the information gcsfuse needs.
type MountConfig struct {
	FileDescriptor  int       `json:"-"`
	VolumeName      string    `json:"volumeName,omitempty"`
	BucketName      string    `json:"bucketName,omitempty"`
	TempDir         string    `json:"-"`
	Options         []string  `json:"options,omitempty"`
	ErrWriter       io.Writer `json:"-"`
	StorageEndpoint string    `json:"storageEndpoint,omitempty"`
	TSEndpoint   string    `json:"tokenEndpoint,omitempty"`
}

func (m *Mounter) Mount(mc *MountConfig) (*exec.Cmd, error) {
	klog.Infof("start to mount bucket %q for volume %q", mc.BucketName, mc.VolumeName)

	if err := os.MkdirAll(mc.TempDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create temp dir %q: %w", mc.TempDir, err)
	}

	flagMap := mc.PrepareMountArgs()
	args := []string{"gcsfuse"}

	for k, v := range flagMap {
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

var disallowedFlags = map[string]bool{
	"app-name":             true,
	"temp-dir":             true,
	"foreground":           true,
	"log-file":             true,
	"log-format":           true,
	"key-file":             true,
	"token-url":            true,
	"reuse-token-from-url": true,
	"o":                    true,
	"endpoint":             true,
}

var boolFlags = map[string]bool{
	"implicit-dirs":                 true,
	"experimental-local-file-cache": true,
	"enable-nonexistent-type-cache": true,
	"debug_fuse_errors":             true,
	"debug_fuse":                    true,
	"debug_fs":                      true,
	"debug_gcs":                     true,
	"debug_http":                    true,
	"debug_invariants":              true,
	"debug_mutex":                   true,
}

func (mc *MountConfig) PrepareMountArgs() map[string]string {
	flagMap := map[string]string{
		"app-name":   GCSFuseAppName,
		"temp-dir":   mc.TempDir,
		"foreground": "",
		"log-file":   "/dev/fd/1", // redirect the output to cmd stdout
		"log-format": "text",
		"uid":        "0",
		"gid":        "0",
	}
	if mc.StorageEndpoint != "" {
		flagMap["endpoint"] = mc.StorageEndpoint
	}

	if mc.TSEndpoint != "" {
		flagMap["token-url"] = mc.TSEndpoint
	}

	invalidArgs := []string{}

	for _, arg := range mc.Options {
		argPair := strings.SplitN(arg, "=", 2)

		if len(argPair) == 0 {
			continue
		}

		if disallowedFlags[argPair[0]] {
			invalidArgs = append(invalidArgs, arg)

			continue
		}

		if boolFlags[argPair[0]] && len(argPair) > 1 {
			argPair[0] = argPair[0] + "=" + argPair[1]
			argPair = argPair[:1]
		}

		flagMap[argPair[0]] = ""
		if len(argPair) > 1 {
			flagMap[argPair[0]] = argPair[1]
		}
	}

	if len(invalidArgs) > 0 {
		klog.Warningf("got invalid arguments for volume %q: %v. Will discard invalid args and continue to mount.",
			invalidArgs, mc.VolumeName)
	}

	return flagMap
}
