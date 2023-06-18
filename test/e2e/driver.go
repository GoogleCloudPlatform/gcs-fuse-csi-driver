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
	"fmt"
	"os"
	"os/exec"

	"k8s.io/klog/v2"
)

func installDriver(pkgDir string) error {
	cmd := exec.Command("make", "-C", pkgDir, "install-driver")
	err := runCommand("Installing non-managed driver", cmd)
	if err != nil {
		return fmt.Errorf("failed to run make command: err: %v", err.Error())
	}

	return nil
}

// TODO(amacaskill): Implement this function. This is used when useManagedDriver is false, but doDriverBuild is true.
//
//nolint:revive
func deleteDriver(testParams *testParameters, deployOverlayName string) error {
	return nil
}

func pushImage(pkgDir, registry, stagingVersion string) error {
	klog.Infof("Pushing image to REGISTRY=%q STAGINGVERSION=%q", registry, stagingVersion)

	err := os.Setenv("REGISTRY", registry)
	if err != nil {
		return err
	}
	// TODO(amacaskill): Revert this once prow docker version allows arm build.
	err = os.Setenv("BUILD_ARM_IMAGE", "false")
	if err != nil {
		return err
	}

	cmd := exec.Command("make", "-C", pkgDir, "build-gcs-fuse")
	err = runCommand("Pushing image", cmd)
	if err != nil {
		return fmt.Errorf("failed to run make command: err: %v", err.Error())
	}

	return nil
}

// TODO(amacaskill): Implement this function. This is used when useManagedDriver is false, but doDriverBuild is true.
//
//nolint:revive
func deleteImage(stagingImage, stagingVersion string) error {
	return nil
}
