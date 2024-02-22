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

package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	sidecarmounter "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/sidecar_mounter"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"k8s.io/klog/v2"
)

var (
	gcsfusePath    = flag.String("gcsfuse-path", "/gcsfuse", "gcsfuse path")
	volumeBasePath = flag.String("volume-base-path", webhook.SidecarContainerTmpVolumeMountPath+"/.volumes", "volume base path")
	_              = flag.Int("grace-period", 0, "grace period for gcsfuse termination. This flag has been deprecated, has no effect and will be removed in the future.")
	// This is set at compile time.
	version = "unknown"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	klog.Infof("Running Google Cloud Storage FUSE CSI driver sidecar mounter version %v", version)
	socketPathPattern := *volumeBasePath + "/*/socket"
	socketPaths, err := filepath.Glob(socketPathPattern)
	if err != nil {
		klog.Fatalf("failed to look up socket paths: %v", err)
	}

	mounter := sidecarmounter.New(*gcsfusePath)
	var wg sync.WaitGroup

	for _, sp := range socketPaths {
		// sleep 1.5 seconds before launch the next gcsfuse to avoid
		// 1. different gcsfuse logs mixed together.
		// 2. memory usage peak.
		time.Sleep(1500 * time.Millisecond)
		errWriter := sidecarmounter.NewErrorWriter(filepath.Join(filepath.Dir(sp), "error"))
		mc, err := sidecarmounter.NewMountConfig(sp)
		if err != nil {
			errMsg := fmt.Sprintf("failed prepare mount config: socket path %q: %v\n", sp, err)
			klog.Errorf(errMsg)
			if _, e := errWriter.Write([]byte(errMsg)); e != nil {
				klog.Errorf("failed to write the error message %q: %v", errMsg, e)
			}

			continue
		}
		mc.ErrWriter = errWriter

		wg.Add(1)
		go func(mc *sidecarmounter.MountConfig) {
			defer wg.Done()
			cmd, err := mounter.Mount(mc)
			if err != nil {
				errMsg := fmt.Sprintf("failed to mount bucket %q for volume %q: %v\n", mc.BucketName, mc.VolumeName, err)
				klog.Errorf(errMsg)
				if _, e := errWriter.Write([]byte(errMsg)); e != nil {
					klog.Errorf("failed to write the error message %q: %v", errMsg, e)
				}

				return
			}

			if err = cmd.Start(); err != nil {
				errMsg := fmt.Sprintf("failed to start gcsfuse with error: %v\n", err)
				klog.Errorf(errMsg)
				if _, e := errWriter.Write([]byte(errMsg)); e != nil {
					klog.Errorf("failed to write the error message %q: %v", errMsg, e)
				}

				return
			}

			// Since the gcsfuse has taken over the file descriptor,
			// closing the file descriptor to avoid other process forking it.
			syscall.Close(mc.FileDescriptor)
			if err = cmd.Wait(); err != nil {
				errMsg := fmt.Sprintf("gcsfuse exited with error: %v\n", err)
				if strings.Contains(errMsg, "signal: terminated") {
					klog.Infof("[%v] gcsfuse was terminated.", mc.VolumeName)
				} else {
					klog.Errorf(errMsg)
				}

				if _, e := errWriter.Write([]byte(errMsg)); e != nil {
					klog.Errorf("failed to write the error message %q: %v", errMsg, e)
				}
			} else {
				klog.Infof("[%v] gcsfuse exited normally.", mc.VolumeName)
			}
		}(mc)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM)
	klog.Info("waiting for SIGTERM signal...")

	// Function that monitors the exit file used in regular sidecar containers.
	monitorExitFile := func() {
		ticker := time.NewTicker(5 * time.Second)
		for {
			<-ticker.C
			// If exit file is detected, send a syscall.SIGTERM signal to the signal channel.
			if _, err := os.Stat(*volumeBasePath + "/exit"); err == nil {
				klog.Info("all the other containers terminated in the Pod, exiting the sidecar container.")

				for _, cmd := range mounter.GetCmds() {
					klog.V(4).Infof("sending SIGTERM to gcsfuse process: %v", cmd)
					err := cmd.Process.Signal(syscall.SIGTERM)
					if err != nil {
						klog.Errorf("failed to terminate process %v with error: %v", cmd, err)
					}
				}

				c <- syscall.SIGTERM

				return
			}
		}
	}

	envVar := os.Getenv("NATIVE_SIDECAR")
	isNativeSidecar, err := strconv.ParseBool(envVar)
	if envVar != "" && err != nil {
		klog.Warningf(`env variable "%s" could not be converted to boolean`, envVar)
	}
	// When the pod contains a regular container, we monitor for the exit file.
	if !isNativeSidecar {
		go monitorExitFile()
	}

	<-c // blocking the process
	klog.Info("received SIGTERM signal, waiting for all the gcsfuse processes exit...")
	wg.Wait()

	klog.Info("exiting sidecar mounter...")
}
