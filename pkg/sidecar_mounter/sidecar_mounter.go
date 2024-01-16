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
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

const (
	GCSFuseAppName = "gke-gcs-fuse-csi"
	TempDir        = "/temp-dir"
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

// MountConfig contains the information gcsfuse needs.
type MountConfig struct {
	FileDescriptor int       `json:"-"`
	VolumeName     string    `json:"volumeName,omitempty"`
	BucketName     string    `json:"bucketName,omitempty"`
	BufferDir      string    `json:"-"`
	ConfigFile     string    `json:"-"`
	Options        []string  `json:"options,omitempty"`
	ErrWriter      io.Writer `json:"-"`
}

func (m *Mounter) Mount(mc *MountConfig) (*exec.Cmd, error) {
	klog.Infof("start to mount bucket %q for volume %q", mc.BucketName, mc.VolumeName)

	if err := os.MkdirAll(mc.BufferDir+TempDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create temp dir %q: %w", mc.BufferDir+TempDir, err)
	}

	flagMap, configFileFlagMap := mc.prepareMountArgs()
	if err := mc.prepareConfigFile(configFileFlagMap); err != nil {
		return nil, fmt.Errorf("failed to create config file %q: %w", mc.ConfigFile, err)
	}

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
	"temp-dir":                             true,
	"config-file":                          true,
	"foreground":                           true,
	"log-file":                             true,
	"log-format":                           true,
	"key-file":                             true,
	"token-url":                            true,
	"reuse-token-from-url":                 true,
	"o":                                    true,
	"logging:file-path":                    true,
	"logging:format":                       true,
	"logging:log-rotate:max-file-size-mb":  true,
	"logging:log-rotate:backup-file-count": true,
	"logging:log-rotate:compress":          true,
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

func (mc *MountConfig) prepareMountArgs() (map[string]string, map[string]string) {
	flagMap := map[string]string{
		"app-name":    GCSFuseAppName,
		"temp-dir":    mc.BufferDir + TempDir,
		"config-file": mc.ConfigFile,
		"foreground":  "",
		"uid":         "0",
		"gid":         "0",
	}

	configFileFlagMap := map[string]string{
		"logging:file-path": "/dev/fd/1", // redirect the output to cmd stdout
		"logging:format":    "text",
	}

	invalidArgs := []string{}

	for _, arg := range mc.Options {
		if strings.Contains(arg, ":") {
			i := strings.LastIndex(arg, ":")
			f, v := arg[:i], arg[i+1:]

			if disallowedFlags[f] {
				invalidArgs = append(invalidArgs, arg)
			} else {
				configFileFlagMap[f] = v
			}

			continue
		}

		argPair := strings.SplitN(arg, "=", 2)
		if len(argPair) == 0 {
			continue
		}

		flag := argPair[0]
		if disallowedFlags[flag] {
			invalidArgs = append(invalidArgs, arg)

			continue
		}

		value := ""
		if len(argPair) > 1 {
			value = argPair[1]
		}

		if boolFlags[flag] && value != "" {
			flag = flag + "=" + value
			if value == "true" || value == "false" {
				value = ""
			} else {
				invalidArgs = append(invalidArgs, flag)

				continue
			}
		}

		if flag == "app-name" {
			value = GCSFuseAppName + "-" + value
		}

		flagMap[flag] = value
	}

	if len(invalidArgs) > 0 {
		klog.Warningf("got invalid arguments for volume %q: %v. Will discard invalid args and continue to mount.",
			invalidArgs, mc.VolumeName)
	}

	return flagMap, configFileFlagMap
}

func (mc *MountConfig) prepareConfigFile(flagMap map[string]string) error {
	configMap := map[string]interface{}{}

	for f, v := range flagMap {
		curLevel := configMap
		tokens := strings.Split(f, ":")
		for i, t := range tokens {
			if i == len(tokens)-1 {
				if _, ok := curLevel[t].(map[string]interface{}); ok {
					return fmt.Errorf("invalid config file flag: %q", f)
				}

				if boolVal, err := strconv.ParseBool(v); err == nil {
					curLevel[t] = boolVal
				} else {
					curLevel[t] = v
				}

				break
			}

			if _, ok := curLevel[t]; !ok {
				curLevel[t] = map[string]interface{}{}
			}

			if nextLevel, ok := curLevel[t].(map[string]interface{}); ok {
				curLevel = nextLevel
			} else {
				return fmt.Errorf("invalid config file flag: %q", f)
			}
		}
	}

	yamlData, err := yaml.Marshal(&configMap)
	if err != nil {
		return err
	}

	klog.Infof("gcsfsue config file content:\n%v", string(yamlData))

	return os.WriteFile(mc.ConfigFile, yamlData, 0o400)
}
