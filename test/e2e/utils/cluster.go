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
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

func clusterDownGKE(testParams *TestParameters) error {
	//nolint:gosec
	cmd := exec.Command("gcloud", "container", "clusters", "delete", testParams.GkeClusterName, "--region", testParams.GkeClusterRegion, "--quiet")
	if err := runCommand("Bringing Down E2E Cluster on GKE", cmd); err != nil {
		return fmt.Errorf("failed to bring down kubernetes e2e cluster on gke: %w", err)
	}

	return nil
}

func clusterUpGKE(testParams *TestParameters) error {
	//nolint:gosec
	out, err := exec.Command("gcloud", "container", "clusters", "list", "--region", testParams.GkeClusterRegion, "--verbosity", "none", "--filter", "name="+testParams.GkeClusterName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check for previous test cluster: output: %v, err: %w", out, err)
	}
	if len(out) > 0 {
		klog.Infof("Detected previous cluster %s. Deleting it so a new one can be created...", testParams.GkeClusterName)
		if err := clusterDownGKE(testParams); err != nil {
			return err
		}
	}

	var cmd *exec.Cmd
	createCmd := "create"
	if testParams.UseGKEAutopilot {
		createCmd = "create-auto"
	}

	// TODO(songjiaxun): remove beta after GA.
	cmdParams := []string{
		"beta", "container", "clusters", createCmd, testParams.GkeClusterName,
		"--region", testParams.GkeClusterRegion, "--quiet",
	}

	if isVariableSet(testParams.GkeClusterVersion) {
		cmdParams = append(cmdParams, "--cluster-version", testParams.GkeClusterVersion)
	}

	standardClusterFlags := []string{
		"--num-nodes", strconv.Itoa(testParams.NumNodes), "--image-type", testParams.NodeImageType,
		"--machine-type", testParams.NodeMachineType,
		"--workload-pool", fmt.Sprintf("%s.svc.id.goog", testParams.ProjectID),
		"--no-enable-autoupgrade",
	}

	if testParams.UseGKEManagedDriver {
		standardClusterFlags = append(standardClusterFlags, "--addons", "GcsFuseCsiDriver")
	}

	if isVariableSet(testParams.GkeNodeVersion) {
		standardClusterFlags = append(standardClusterFlags, "--node-version", testParams.GkeNodeVersion)
	}

	// If using standard cluster, add required flags.
	if !testParams.UseGKEAutopilot {
		cmdParams = append(cmdParams, standardClusterFlags...)

		// Update gcloud to latest version.
		cmd = exec.Command("gcloud", "components", "update")
		if err := runCommand("Updating gcloud to the latest version", cmd); err != nil {
			return fmt.Errorf("failed to update gcloud to latest version: %w", err)
		}
	}

	// TODO(songjiaxun): remove this after 1.27 is available in prod environment.
	if strings.Contains(testParams.GkeClusterVersion, "1.27") {
		cmdParams = append(cmdParams, "--release-channel", "rapid")
	}

	cmd = exec.Command("gcloud", cmdParams...)
	if err := runCommand("Starting e2e Cluster on GKE", cmd); err != nil {
		return fmt.Errorf("failed to bring up kubernetes e2e cluster on GKE: %w", err)
	}

	// Call update because --add-maintenance-exclusion is not an available flag for create-auto.
	if testParams.UseGKEAutopilot {
		startExclusionTime := time.Now().UTC()
		//nolint:gosec
		cmd = exec.Command("gcloud", "container", "clusters", "update", testParams.GkeClusterName, "--region", testParams.GkeClusterRegion,
			"--add-maintenance-exclusion-name", "no-upgrades-during-test",
			"--add-maintenance-exclusion-start", startExclusionTime.Format(time.RFC3339),
			"--add-maintenance-exclusion-end", startExclusionTime.Add(2*time.Hour).Format(time.RFC3339),
			"--add-maintenance-exclusion-scope", "no_upgrades")
		if err := runCommand("Updating Autopilot Cluster with maintenance window", cmd); err != nil {
			return fmt.Errorf("failed to update autopilot cluster with maintenance window: %w", err)
		}
	}

	return nil
}
