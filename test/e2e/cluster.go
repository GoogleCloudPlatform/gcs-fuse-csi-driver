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
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"

	apimachineryversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/klog/v2"
)

func gkeLocationArgs(gceRegion string) (locationArg, locationVal string, err error) {
	switch {
	case len(gceRegion) > 0:
		locationArg = "--region"
		locationVal = gceRegion
	default:
		return "", "", fmt.Errorf("region unspecified")
	}

	return
}

func clusterDownGKE(gceRegion string) error {
	locationArg, locationVal, err := gkeLocationArgs(gceRegion)
	if err != nil {
		return err
	}

	cmd := exec.Command("gcloud", "container", "clusters", "delete", *gkeTestClusterName,
		locationArg, locationVal, "--quiet")
	err = runCommand("Bringing Down E2E Cluster on GKE", cmd)
	if err != nil {
		return fmt.Errorf("failed to bring down kubernetes e2e cluster on gke: %v", err.Error())
	}

	return nil
}

func clusterUpGKE(projectID string, gceRegion string, numNodes int, imageType string, useManagedDriver, useGKEAutopilot bool) error {
	locationArg, locationVal, err := gkeLocationArgs(gceRegion)
	if err != nil {
		return err
	}

	out, err := exec.Command("gcloud", "container", "clusters", "list",
		locationArg, locationVal, "--verbosity", "none", "--filter",
		fmt.Sprintf("name=%s", *gkeTestClusterName)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check for previous test cluster: %v %s", err.Error(), out)
	}
	if len(out) > 0 {
		klog.Infof("Detected previous cluster %s. Deleting so a new one can be created...", *gkeTestClusterName)
		err = clusterDownGKE(gceRegion)
		if err != nil {
			return err
		}
	}

	var cmd *exec.Cmd
	createCmd := "create"
	if useGKEAutopilot {
		createCmd = "create-auto"
	}
	cmdParams := []string{
		"beta", "container", "clusters", createCmd, *gkeTestClusterName,
		locationArg, locationVal,
		"--quiet", "--machine-type", "n1-standard-2",
	}

	if isVariableSet(gkeClusterVer) {
		cmdParams = append(cmdParams, "--cluster-version", *gkeClusterVer)
	}

	standardClusterFlags := []string{
		"--num-nodes", strconv.Itoa(numNodes), "--image-type", imageType,
		"--workload-pool", fmt.Sprintf("%s.svc.id.goog", projectID),
	}
	if isVariableSet(gkeNodeVersion) {
		standardClusterFlags = append(standardClusterFlags, "--node-version", *gkeNodeVersion)
	}
	if useManagedDriver {
		standardClusterFlags = append(standardClusterFlags, "--addons", "GcsFuseCsiDriver")
	}

	// If using standard cluster, add required flags.
	if !useGKEAutopilot {
		cmdParams = append(cmdParams, standardClusterFlags...)

		// Update gcloud to latest version.
		cmd = exec.Command("gcloud", "components", "update")
		err = runCommand("Updating gcloud to the latest version", cmd)
		if err != nil {
			return fmt.Errorf("failed to update gcloud to latest version: %v", err.Error())
		}
	}

	// TODO(amacaskill): change from beta to GA.
	cmd = exec.Command("gcloud", cmdParams...)
	err = runCommand("Starting E2E Cluster on GKE", cmd)
	if err != nil {
		return fmt.Errorf("failed to bring up kubernetes e2e cluster on gke: %v", err.Error())
	}

	return nil
}

func getKubeClusterVersion() (string, error) {
	out, err := exec.Command("kubectl", "version", "-o=json").Output()
	if err != nil {
		return "", fmt.Errorf("failed to obtain cluster version, error: %v; output was %s", err.Error(), out)
	}
	type version struct {
		ClientVersion *apimachineryversion.Info `json:"clientVersion,omitempty" yaml:"clientVersion,omitempty"`
		ServerVersion *apimachineryversion.Info `json:"serverVersion,omitempty" yaml:"serverVersion,omitempty"`
	}

	var v version
	err = json.Unmarshal(out, &v)
	if err != nil {
		return "", fmt.Errorf("Failed to parse kubectl version output, error: %v", err.Error())
	}

	return v.ServerVersion.GitVersion, nil
}

func mustGetKubeClusterVersion() string {
	ver, err := getKubeClusterVersion()
	if err != nil {
		klog.Fatalf("Error: %v", err.Error())
	}

	return ver
}
