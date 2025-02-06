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
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	sidecarmounter "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/sidecar_mounter"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"k8s.io/klog/v2"
)

var (
	hostNetwork    = flag.Bool("host-network", false, "pod hostNetwork configuration")
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
	ctx, cancel := context.WithCancel(context.Background())

	for _, sp := range socketPaths {
		// sleep 1.5 seconds before launch the next gcsfuse to avoid
		// 1. different gcsfuse logs mixed together.
		// 2. memory usage peak.
		time.Sleep(1500 * time.Millisecond)
		mc := sidecarmounter.NewMountConfig(sp, *hostNetwork)
		if mc != nil {
			if err := mounter.Mount(ctx, mc); err != nil {
				mc.ErrWriter.WriteMsg(fmt.Sprintf("failed to mount bucket %q for volume %q: %v\n", mc.BucketName, mc.VolumeName, err))
			}
		}
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

				// After the Kubernetes native sidecar container feature is adopted,
				// we should propagate the SIGTERM signal outside of this goroutine.
				cancel()

				if err := os.Remove(*volumeBasePath + "/exit"); err != nil {
					klog.Error("failed to remove the exit file from emptyDir.")
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

	if isNativeSidecar {
		cancel()
	}

	mounter.WaitGroup.Wait()

	klog.Info("exiting sidecar mounter...")
}
