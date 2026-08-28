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
	"unicode"

	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/klog/v2"
)

var (
	nativeSidecarMinimumVersion = version.MustParseGeneric("1.29.0")
	// TODO(@siyanshen): to enable hostnetwork tests for managed drivers, update min version when GCW feature flag is on.
	SaTokenVolInjectionMinimumVersion           = version.MustParseGeneric("1.100.0")
	skipBucketCheckMinimumVersion               = version.MustParseGeneric("1.29.0")
	kernelReadAheadMinimumVersion               = version.MustParseGeneric("1.32.0")
	metricsSupportedMinimumVersion              = version.MustParseGeneric("1.33.0")
	metadataPrefetchMinimumVersion              = version.MustParseGeneric("1.32.0")
	longMountOptionsMinimumVersion              = version.MustParseGeneric("1.32.0")
	supportsMachineTypeAutoConfigMinimumVersion = version.MustParseGeneric("1.33.0")
	sidecarBucketAccessCheckMinimumVersion      = version.MustParseGeneric("1.34.1")
	gcsfuseProfilesMinimumVersion               = version.MustParseGeneric("1.35.1")
	cloudProfilerMinimumVersion                 = version.MustParseGeneric("1.36.1")
	errorFileCleanUpMinimumVersion              = version.MustParseGeneric("1.36.0")
)

// gcloudCommand constructs an exec.Cmd for a gcloud command,
// incorporating custom command paths and default arguments from TestParameters.
func gcloudCommand(testParams *TestParameters, args ...string) *exec.Cmd {
	gcloudBin := testParams.GkeGcloudCommand
	if gcloudBin == "" {
		gcloudBin = "gcloud" // Default to "gcloud" if not provided
	}

	var fullArgs []string
	if testParams.GkeGcloudArgs != "" {
		fullArgs = append(fullArgs, strings.Fields(testParams.GkeGcloudArgs)...)
	}
	fullArgs = append(fullArgs, args...)

	//nolint:gosec
	return exec.Command(gcloudBin, fullArgs...)
}

func clusterDownGKE(testParams *TestParameters) error {
	//nolint:gosec
	cmd := gcloudCommand(testParams, "container", "clusters", "delete", testParams.GkeClusterName, "--region", testParams.GkeClusterRegion, "--project", testParams.ProjectID, "--quiet")
	if err := runCommand("Bringing Down E2E Cluster on GKE", cmd); err != nil {
		return fmt.Errorf("failed to bring down kubernetes e2e cluster on gke: %w", err)
	}

	return nil
}

// queryRegionalStandardZones retrieves standard compute zones for a region from 'gcloud compute regions describe',
// which naturally excludes AI-only zones, so they can be passed to Capacity Advisor via --zones.
func queryRegionalStandardZones(testParams *TestParameters, region string) string {
	regionArgs := []string{
		"compute", "regions", "describe", region,
		"--format=value(zones.basename())",
	}
	if testParams.ProjectID != "" {
		regionArgs = append(regionArgs, "--project="+testParams.ProjectID)
	}

	regionOut, err := gcloudCommand(testParams, regionArgs...).Output()
	if err != nil {
		klog.Warningf("Failed to query standard regional compute zones for %s: %v", region, err)
		return ""
	}

	// gcloud formats list projections with semicolons, commas, or whitespace; normalize into a comma-separated list for --zones.
	zones := strings.FieldsFunc(string(regionOut), func(r rune) bool {
		return r == ';' || r == ',' || unicode.IsSpace(r)
	})
	return strings.Join(zones, ",")
}

// queryCapacityAdvice calls queryRegionalStandardZones to get standard GKE zones in the given region,
// and queries Capacity Advisor to return the recommended zone and obtainability score.
func queryCapacityAdvice(testParams *TestParameters, region string) (string, float64, error) {
	// Note: Capacity Advisor requires --provisioning-model (supports only SPOT or FLEX_START).
	// We use SPOT as a proxy for regional resource availability.
	// See: https://cloud.google.com/sdk/gcloud/reference/beta/compute/advice/capacity
	cmdArgs := []string{
		"beta", "compute", "advice", "capacity",
		"--region=" + region,
		"--provisioning-model=SPOT",
		"--size=" + strconv.Itoa(testParams.NumNodes),
		"--instance-selection-machine-types=" + testParams.NodeMachineType,
		"--target-distribution-shape=any-single-zone",
		"--format=value(recommendations[0].scores.obtainability,recommendations[0].shards[0].zone.basename())",
	}

	// Restrict capacity search to standard regional compute zones to avoid non-GKE AI zones.
	if zonesFilter := queryRegionalStandardZones(testParams, region); zonesFilter != "" {
		cmdArgs = append(cmdArgs, "--zones="+zonesFilter)
	}
	if testParams.ProjectID != "" {
		cmdArgs = append(cmdArgs, "--project="+testParams.ProjectID)
	}

	out, err := gcloudCommand(testParams, cmdArgs...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", 0, fmt.Errorf("failed to query capacity advice: %w, stderr: %s", err, string(exitErr.Stderr))
		}
		return "", 0, fmt.Errorf("failed to query capacity advice: %w", err)
	}
	klog.Infof("Capacity Advisor output for region %s: %s", region, string(out))
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return "", 0, fmt.Errorf("no zone or score returned in capacity advice: %q", string(out))
	}
	score, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", 0, fmt.Errorf("failed to parse obtainability score %q: %w", fields[0], err)
	}
	zone := fields[1]
	return zone, score, nil
}

func clusterUpGKE(testParams *TestParameters) error {
	var cmd *exec.Cmd

	// Update gcloud to latest version in Prow.
	if testParams.InProw {
		cmd = gcloudCommand(testParams, "components", "update", "--quiet")
		if err := runCommand("Updating gcloud to the latest version", cmd); err != nil {
			return fmt.Errorf("failed to update gcloud to latest version: %w", err)
		}
	} else {
		// This is skipped only to ensure command doesn't fail for 'apt' package installed gcloud in local runs.
		klog.Infof("Skipping gcloud components update for local run.")
	}

	// Fall back across candidate regions to mitigate stockouts; Zonal Buckets
	// are pinned to testParams.GkeClusterRegion (which defaults to us-central1).
	// For standard clusters, testParams.GkeClusterRegion is prioritized first,
	// followed by candidate fallback regions with typically lower contention.
	candidateRegions := []string{testParams.GkeClusterRegion}
	if !testParams.EnableZB {
		for _, r := range []string{"us-east4", "us-east1", "us-west1", "us-west4"} {
			if r != testParams.GkeClusterRegion {
				candidateRegions = append(candidateRegions, r)
			}
		}
	}

	var nodeLocations string

	// Skip Capacity Advisor for Autopilot because Autopilot automatically manages where nodes are created.
	if !testParams.UseGKEAutopilot && testParams.UseCapacityAdvisor {
		const minObtainabilityThreshold = 0.8
		for _, region := range candidateRegions {
			zone, score, err := queryCapacityAdvice(testParams, region)
			if err != nil {
				klog.Warningf("Capacity Advisor query failed for region %q: %v", region, err)
				continue
			}
			klog.Infof("Capacity Advisor for region %q: zone=%q, obtainability=%.2f", region, zone, score)

			// If score meets threshold, pick this region immediately.
			if score >= minObtainabilityThreshold {
				testParams.GkeClusterRegion = region
				nodeLocations = zone
				break
			}
			klog.Warningf("Region %q score %.2f below threshold %.2f, checking next candidate region...", region, score, minObtainabilityThreshold)
		}

		if nodeLocations == "" {
			klog.Warningf("Capacity Advisor queries failed to find a region meeting threshold %.2f, falling back to default regional node allocation in %s", minObtainabilityThreshold, testParams.GkeClusterRegion)
			testParams.NumNodes = max(1, testParams.NumNodes/3)
		}
	}

	klog.Infof("Attempting to create cluster %q in region %q (zone: %q)...", testParams.GkeClusterName, testParams.GkeClusterRegion, nodeLocations)

	//nolint:gosec
	out, listErr := gcloudCommand(testParams, "container", "clusters", "list", "--region", testParams.GkeClusterRegion, "--project", testParams.ProjectID, "--verbosity", "none", "--filter", "name="+testParams.GkeClusterName).CombinedOutput()
	if listErr != nil {
		return fmt.Errorf("failed to check for previous test cluster: output: %v, err: %w", out, listErr)
	}
	if len(out) > 0 {
		klog.Infof("Detected previous cluster %s. Deleting it so a new one can be created...", testParams.GkeClusterName)
		if err := clusterDownGKE(testParams); err != nil {
			return err
		}
	}

	createCmd := "create"
	if testParams.UseGKEAutopilot {
		createCmd = "create-auto"
	}

	cmdParams := []string{
		"container", "clusters", createCmd, testParams.GkeClusterName,
		"--region", testParams.GkeClusterRegion, "--quiet",
		"--release-channel", testParams.GkeReleaseChannel,
		"--project", testParams.ProjectID,
	}

	if isVariableSet(testParams.GkeClusterVersion) {
		cmdParams = append(cmdParams, "--cluster-version", testParams.GkeClusterVersion)
	}

	standardClusterFlags := []string{
		"--num-nodes", strconv.Itoa(testParams.NumNodes), "--image-type", testParams.NodeImageType,
		"--machine-type", testParams.NodeMachineType,
		"--workload-pool", testParams.ProjectID + ".svc.id.goog",
	}

	if testParams.UseGKEManagedDriver {
		standardClusterFlags = append(standardClusterFlags, "--addons", "GcsFuseCsiDriver")
	}

	if isVariableSet(testParams.GkeNodeVersion) {
		standardClusterFlags = append(standardClusterFlags, "--node-version", testParams.GkeNodeVersion)
	}

	if nodeLocations != "" {
		standardClusterFlags = append(standardClusterFlags, "--node-locations", nodeLocations)
	}

	// If using standard cluster, add required flags.
	if !testParams.UseGKEAutopilot {
		cmdParams = append(cmdParams, standardClusterFlags...)
	}

	cmd = gcloudCommand(testParams, cmdParams...)
	if err := runCommand("Starting e2e Cluster on GKE", cmd); err != nil {
		return fmt.Errorf("failed to bring up kubernetes e2e cluster on GKE: %w", err)
	}

	// Call update because --add-maintenance-exclusion is not an available flag during cluster creation.
	startExclusionTime := time.Now().UTC()

	exclusionDuration, parseErr := time.ParseDuration(testParams.GinkgoTimeout)
	if parseErr != nil {
		klog.Warningf("failed to parse ginkgo timeout %q, using default 4h for maintenance exclusion: %v", testParams.GinkgoTimeout, parseErr)
		exclusionDuration = 4 * time.Hour
	}

	//nolint:gosec
	cmd = gcloudCommand(testParams, "container", "clusters", "update", testParams.GkeClusterName, "--region", testParams.GkeClusterRegion, "--project", testParams.ProjectID,
		"--add-maintenance-exclusion-name", "no-upgrades-during-test",
		"--add-maintenance-exclusion-start", startExclusionTime.Format(time.RFC3339),
		"--add-maintenance-exclusion-end", startExclusionTime.Add(exclusionDuration).Format(time.RFC3339),
		"--add-maintenance-exclusion-scope", "no_upgrades")
	if err := runCommand("Updating Cluster with maintenance window", cmd); err != nil {
		return fmt.Errorf("failed to update cluster with maintenance window: %w", err)
	}

	return nil
}

func ClusterAtLeastMinVersion(clusterVersion, nodeVersion string, minVersion *version.Version) (bool, error) {
	supportsFeature := false
	if clusterVersion != "" {
		parsedClusterVersion, err := version.ParseGeneric(clusterVersion)
		if err != nil {
			return false, err
		}
		if parsedClusterVersion.AtLeast(minVersion) {
			supportsFeature = true

			if nodeVersion != "" {
				parsedNodeVersion, err := version.ParseGeneric(nodeVersion)
				if err != nil {
					return false, err
				}
				if !parsedNodeVersion.AtLeast(minVersion) {
					supportsFeature = false
				}
			}
		}
	}

	return supportsFeature, nil
}
