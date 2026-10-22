/*
Copyright 2018 The Kubernetes Authors.
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

package sidecarmounter

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"k8s.io/klog/v2"
)

// The startup probe passes once every volume has a ready file. The sidecar writes it when gcsfuse
// reports a successful mount on the DAEMONIZE_STATUS_FD pipe (github.com/jacobsa/daemonize).
// daemonize doesn't export its message types, so they are copied below.
const (
	daemonizeStatusFDEnv = "DAEMONIZE_STATUS_FD"
	// The status pipe is the second entry of exec.Cmd.ExtraFiles, so gcsfuse sees it as FD 4.
	statusPipeFD = 4
)

type logMsg struct {
	Msg []byte
}

type outcomeMsg struct {
	Successful bool
	ErrorMsg   string
}

func init() {
	gob.RegisterName("github.com/jacobsa/daemonize.logMsg", logMsg{})
	gob.RegisterName("github.com/jacobsa/daemonize.outcomeMsg", outcomeMsg{})
}

var errNoMountOutcome = errors.New("gcsfuse exited before reporting the mount outcome")

// readMountOutcome reads the gcsfuse status pipe (skipping any progress log
// messages) until gcsfuse reports whether the mount succeeded or failed.
func readMountOutcome(r io.Reader) error {
	decoder := gob.NewDecoder(r)
	for {
		var msg any
		if err := decoder.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return errNoMountOutcome
			}

			return fmt.Errorf("failed to decode the gcsfuse mount status: %w", err)
		}

		if m, ok := msg.(outcomeMsg); ok {
			if m.Successful {
				return nil
			}

			return errors.New(m.ErrorMsg)
		}
	}
}

// watchMountOutcome writes a ready file in the volume directory once gcsfuse reports a successful mount.
func watchMountOutcome(statusR *os.File, mc *MountConfig) {
	defer statusR.Close()

	if err := readMountOutcome(statusR); err != nil {
		// The error file is left alone: gcsfuse prints its failures to stderr, which already goes there,
		// and the CSI driver treats any other text in it as an internal error.
		klog.Errorf("[%v] gcsfuse failed to mount: %v", mc.VolumeName, err)

		return
	}

	klog.Infof("[%v] gcsfuse mounted bucket %q", mc.VolumeName, mc.BucketName)
	readyFile := filepath.Join(mc.TempDir, util.ReadyFileName)
	if err := os.WriteFile(readyFile, nil, 0o644); err != nil {
		klog.Errorf("[%v] failed to write the ready file %q: %v", mc.VolumeName, readyFile, err)
	}
}

// CheckStartupProbe succeeds once every volume served by this sidecar has a ready file, and
// otherwise returns what is holding the mount up so that the kubelet puts it in the Pod events.
func CheckStartupProbe(volumeBasePath string) error {
	// Every volume has its own directory here. The socket in it can't be used to find the volumes,
	// because the sidecar deletes it as soon as it has read the mount config.
	entries, err := os.ReadDir(volumeBasePath)
	if err != nil {
		// Pass if the pod enabled gke-gcsfuse/volumes: "true" without mounting any GCSFuse volumes.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to list volume directories: %w", err)
	}

	var notReady []string
	for _, entry := range entries {
		// Skip files such as flags-for-defaulting, which the CSI driver also writes here.
		if !entry.IsDir() {
			continue
		}

		volumeName := entry.Name()
		volumeDir := filepath.Join(volumeBasePath, volumeName)
		if _, err := os.Stat(filepath.Join(volumeDir, util.ReadyFileName)); err == nil {
			continue
		}

		data, _ := os.ReadFile(filepath.Join(volumeDir, util.ErrorFileName))
		if msg := strings.TrimSpace(string(data)); msg != "" {
			notReady = append(notReady, fmt.Sprintf("volume %q: %s", volumeName, msg))
		} else {
			notReady = append(notReady, fmt.Sprintf("volume %q: mount in progress", volumeName))
		}
	}

	if len(notReady) > 0 {
		return fmt.Errorf("gcsfuse mount not ready: %s", strings.Join(notReady, "; "))
	}

	return nil
}
