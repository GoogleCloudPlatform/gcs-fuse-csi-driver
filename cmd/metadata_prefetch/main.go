/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"k8s.io/klog/v2"
)

const (
	mountPathsLocation = "/volumes/"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// Create cancellable context to pass into exec.
	ctx, cancel := context.WithCancel(context.Background())

	// Handle SIGTERM signal.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)

	go func() {
		<-sigs
		klog.Info("Caught SIGTERM signal: Terminating...")
		cancel()

		os.Exit(0) // Exit gracefully
	}()

	// Start the "ls" command in the background.
	// All our volumes are mounted under the /volumes/ directory.
	cmd := exec.CommandContext(ctx, "ls", "-R", mountPathsLocation)
	cmd.Stdout = nil // Connects file descriptor to the null device (os.DevNull).

	// TODO(hime): We should research stratergies to parallelize ls execution and speed up cache population.
	err := cmd.Start()
	if err == nil {
		mountPaths, err := getDirectoryNames(mountPathsLocation)
		if err == nil {
			klog.Infof("Running ls on mountPath(s): %s", strings.Join(mountPaths, ", "))
		} else {
			klog.Warningf("failed to get mountPaths: %v", err)
		}

		err = cmd.Wait()
		if err != nil {
			klog.Errorf("Error while executing ls command: %v", err)
		} else {
			klog.Info("Metadata prefetch complete")
		}
	} else {
		klog.Errorf("Error starting ls command: %v.", err)
	}

	klog.Info("Going to sleep...")

	// Keep the process running.
	select {}
}

// getDirectoryNames returns a list of strings representing the names of
// the directories within the provided path.
func getDirectoryNames(dirPath string) ([]string, error) {
	directories := []string{}
	items, err := os.ReadDir(dirPath)
	if err != nil {
		return directories, err
	}

	for _, item := range items {
		if item.IsDir() {
			directories = append(directories, item.Name())
		}
	}

	return directories, nil
}
