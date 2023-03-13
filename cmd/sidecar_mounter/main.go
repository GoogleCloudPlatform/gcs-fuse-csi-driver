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

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	sidecarmounter "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/sidecar_mounter"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"k8s.io/klog/v2"
)

var (
	gcsfusePath    = flag.String("gcsfuse-path", "/gcsfuse", "gcsfuse path")
	volumeBasePath = flag.String("volume-base-path", "/gcsfuse-tmp/.volumes", "volume base path")
	gracePeriod    = flag.Int("grace-period", 15, "grace period for gcsfuse termination")
	// This is set at compile time
	version = "unknown"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	klog.Infof("Running Google Cloud Storage FUSE CSI driver sidecar mounter version %v", version)
	socketPathPattern := *volumeBasePath + "/*/socket"
	socketPathes, err := filepath.Glob(socketPathPattern)
	if err != nil {
		klog.Fatalf("failed to look up socket pathes: %v", err)
	}

	mounter := sidecarmounter.New(*gcsfusePath)
	var wg sync.WaitGroup

	for _, sp := range socketPathes {
		// sleep 1.5 seconds before launch the next gcsfuse to avoid
		// 1. different gcsfuse logs mixed together.
		// 2. memory usage peak.
		time.Sleep(1500 * time.Millisecond)
		errWriter := sidecarmounter.NewErrorWriter(filepath.Join(filepath.Dir(sp), "error"))
		mc, err := prepareMountConfig(sp)
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
				klog.Errorf(errMsg)
				if _, e := errWriter.Write([]byte(errMsg)); e != nil {
					klog.Errorf("failed to write the error message %q: %v", errMsg, e)
				}
			} else {
				klog.Infof("[%v] gcsfuse exited normally.", mc.VolumeName)
			}
		}(mc)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	klog.Info("waiting for termination signals...")

	// Monitor the exit file.
	// If the exit file is detected, send a syscall.SIGTERM signal to the singal channel.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		for {
			select {
			case <-ticker.C:
				if _, err := os.Stat("/tmp/.volumes/exit"); err == nil {
					klog.Info("all the other containers exited in the Job Pod, exiting the sidecar container.")
					c <- syscall.SIGTERM
					return
				}
			}
		}
	}()

	sig := <-c // blocking the process
	klog.Infof("received signal: %v, sleep %v seconds before terminating gcsfuse processes.", sig, *gracePeriod)
	time.Sleep(time.Duration(*gracePeriod) * time.Second)

	for _, cmd := range mounter.GetCmds() {
		klog.V(4).Infof("killing gcsfue process: %v", cmd)
		err := cmd.Process.Kill()
		if err != nil {
			klog.Errorf("failed to kill process %v with error: %v", cmd, err)
		}
	}

	wg.Wait()
	klog.Info("existing sidecar mounter...")
}

// Fetch the following information from a given socket path:
// 1. Pod volume name
// 2. The file descriptor
// 3. GCS bucket name
// 4. Mount options passing to gcsfuse (passed by the csi mounter)
func prepareMountConfig(sp string) (*sidecarmounter.MountConfig, error) {
	// socket path pattern: /tmp/.volumes/<volume-name>/socket
	dir := filepath.Dir(sp)
	volumeName := filepath.Base(dir)
	mc := sidecarmounter.MountConfig{
		VolumeName: volumeName,
		TempDir:    filepath.Join(dir, "temp-dir"),
	}

	klog.Infof("connecting to socket %q", sp)
	c, err := net.Dial("unix", sp)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to the socket %q: %v", sp, err)
	}

	fd, msg, err := util.RecvMsg(c)
	if err != nil {
		return nil, fmt.Errorf("failed to receive mount options from the socket %q: %v", sp, err)
	}
	// as we got all the information from the socket, closing the connection and deleting the socket
	c.Close()
	if err = syscall.Unlink(sp); err != nil {
		klog.Errorf("failed to close socket %q: %v", sp, err)
	}

	mc.FileDescriptor = fd

	if err := json.Unmarshal(msg, &mc); err != nil {
		return nil, fmt.Errorf("failed to unmarchal the mount config: %v", err)
	}

	if mc.BucketName == "" {
		return nil, fmt.Errorf("failed to fetch bucket name from CSI driver")
	}

	return &mc, nil
}
