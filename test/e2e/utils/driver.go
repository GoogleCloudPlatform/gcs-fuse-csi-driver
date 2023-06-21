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

package utils

import (
	"fmt"
	"os/exec"
)

func installDriver(pkgDir, registry, overlay string) error {
	//nolint:gosec
	cmd := exec.Command("make", "-C", pkgDir, "install", fmt.Sprintf("OVERLAY=%s", overlay), fmt.Sprintf("REGISTRY=%s", registry))
	if err := runCommand("Installing non-managed CSI driver", cmd); err != nil {
		return fmt.Errorf("failed to run install non-managed CSI driver: %w", err)
	}

	return nil
}

func deleteDriver(pkgDir, overlay string) error {
	//nolint:gosec
	cmd := exec.Command("make", "-C", pkgDir, "uninstall", fmt.Sprintf("OVERLAY=%s", overlay))
	if err := runCommand("Uninstalling non-managed CSI driver", cmd); err != nil {
		return fmt.Errorf("failed to run uninstall non-managed CSI driver: %w", err)
	}

	return nil
}
