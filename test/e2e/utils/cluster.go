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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path"
	"sort"
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

// Capacity Advisor settings for queryCapacityAdvice.
// TODO(b/570275744): Clean up these settings when queryCapacityAdvice uses gcloud.
const (
	// prowCapacityAdvisorProject is allowlisted for STANDARD queries, which are not GA yet.
	prowCapacityAdvisorProject = "prow-gob-internal-boskos-01"
	capacityAdvisorEndpoint    = "https://compute.googleapis.com/compute/beta"
	// capacityAdvisorURLFormat takes the endpoint, project and region.
	capacityAdvisorURLFormat         = "%s/projects/%s/regions/%s/advice/capacity"
	capacityAdvisorProvisioningModel = "STANDARD"
	capacityAdvisorTargetShape       = "ANY_SINGLE_ZONE"
	capacityAdvisorInstanceSelection = "instance-selection-1"
	capacityAdvisorZonePrefix        = "zones/"
	capacityAdvisorTimeout           = 30 * time.Second
	// capacityAdvisorMaxErrorBytes caps how much of an error response is logged.
	capacityAdvisorMaxErrorBytes = 4096
	// capacityAdvisorStockoutScore is returned, with a random zone, when no zone in the region has capacity.
	capacityAdvisorStockoutScore = 0.1
)

// capacityAdvisorFallbackRegions are the fallback regions queried by Capacity Advisor
// (after testParams.GkeClusterRegion, which defaults to "us-central1") to mitigate stockouts.
var capacityAdvisorFallbackRegions = []string{
	"us-east4",
	"us-east1",
	"us-west1",
	"us-west4",
	"europe-west1",
	"europe-west3",
}

// zbSupportedZones maps Capacity Advisor candidate regions (testParams.GkeClusterRegion and
// capacityAdvisorFallbackRegions) to the GCE zones in that region that support GCS Zonal Buckets
// (RAPID storage class) and have rapid_zonal_bytes quota in Boskos.
// See: https://cloud.google.com/storage/docs/locations#location-z
var zbSupportedZones = map[string][]string{
	"us-central1":  {"us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"},
	"us-east4":     {"us-east4-a", "us-east4-b", "us-east4-c"},
	"us-east1":     {"us-east1-b", "us-east1-d"},
	"us-west1":     {"us-west1-a", "us-west1-b", "us-west1-c"},
	"us-west4":     {"us-west4-a", "us-west4-b", "us-west4-c"},
	"europe-west1": {"europe-west1-b", "europe-west1-c", "europe-west1-d"},
}

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
// which naturally excludes AI-only zones, so they can be passed to Capacity Advisor as distributionPolicy.zones.
func queryRegionalStandardZones(testParams *TestParameters, region string) []string {
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
		return nil
	}

	// gcloud formats list projections with semicolons, commas, or whitespace, so split on all of them.
	return strings.FieldsFunc(string(regionOut), func(r rune) bool {
		return r == ';' || r == ',' || unicode.IsSpace(r)
	})
}

// gcloudAccessToken returns an access token for the active gcloud account.
// TODO(b/570275744): Remove when queryCapacityAdvice uses gcloud.
func gcloudAccessToken(testParams *TestParameters) (string, error) {
	out, err := gcloudCommand(testParams, "auth", "print-access-token").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("failed to get access token: %w, stderr: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}

		return "", fmt.Errorf("failed to get access token: %w", err)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("failed to get access token: gcloud returned an empty token")
	}

	return token, nil
}

// apiErrorMessage returns the message of a Google API error response body, or else the body on one line.
// TODO(b/570275744): Remove when queryCapacityAdvice uses gcloud.
func apiErrorMessage(body []byte) string {
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return apiErr.Error.Message
	}

	return strings.Join(strings.Fields(string(body)), " ")
}

// queryCapacityAdvice calls queryRegionalStandardZones to get standard GKE zones in the given region,
// and queries Capacity Advisor with the given access token to return the recommended zone and obtainability score.
// Note: gcloud does not support the STANDARD provisioning model yet, so call the API directly.
// See: https://cloud.google.com/compute/docs/reference/rest/beta/advice/capacity
// TODO(b/570275744): Replace the POST with gcloud command when gcloud supports STANDARD provisioning model.
func queryCapacityAdvice(testParams *TestParameters, region, token string) (string, float64, error) {
	// Restrict capacity search to standard regional compute zones to avoid non-GKE AI zones.
	distributionPolicy := map[string]any{"targetShape": capacityAdvisorTargetShape}
	var zones []string
	if testParams.EnableZB {
		zones = zbSupportedZones[region]
		if len(zones) == 0 {
			return "", 0, fmt.Errorf("no ZB-supported zones available in region %q", region)
		}
	} else {
		zones = queryRegionalStandardZones(testParams, region)
	}
	if len(zones) > 0 {
		zoneConfigs := make([]map[string]string, 0, len(zones))
		for _, zone := range zones {
			zoneConfigs = append(zoneConfigs, map[string]string{"zone": capacityAdvisorZonePrefix + zone})
		}
		distributionPolicy["zones"] = zoneConfigs
	}

	// Construct request using the body that 'gcloud beta compute advice capacity' sends (verified via --log-http), but with STANDARD.
	reqBody, err := json.Marshal(map[string]any{
		"instanceProperties": map[string]any{
			"scheduling": map[string]any{"provisioningModel": capacityAdvisorProvisioningModel},
		},
		"instanceFlexibilityPolicy": map[string]any{
			"instanceSelections": map[string]any{
				capacityAdvisorInstanceSelection: map[string]any{"machineTypes": []string{testParams.NodeMachineType}},
			},
		},
		"distributionPolicy": distributionPolicy,
		"size":               testParams.NumNodes,
	})
	if err != nil {
		return "", 0, fmt.Errorf("failed to marshal capacity advice request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), capacityAdvisorTimeout)
	defer cancel()

	reqURL := fmt.Sprintf(capacityAdvisorURLFormat, capacityAdvisorEndpoint, prowCapacityAdvisorProject, region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create capacity advice request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("failed to query capacity advice: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, capacityAdvisorMaxErrorBytes))
		return "", 0, fmt.Errorf("failed to query capacity advice: HTTP %d: %s", resp.StatusCode, apiErrorMessage(errBody))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read capacity advice response: %w", err)
	}

	var advice struct {
		Recommendations []struct {
			Scores struct {
				Obtainability float64 `json:"obtainability"`
			} `json:"scores"`
			Shards []struct {
				Zone string `json:"zone"`
			} `json:"shards"`
		} `json:"recommendations"`
	}
	if err := json.Unmarshal(respBody, &advice); err != nil {
		return "", 0, fmt.Errorf("failed to parse capacity advice response %q: %w", respBody, err)
	}

	// Check for a zone and a score above the stockout threshold.
	recs := advice.Recommendations
	if len(recs) == 0 || len(recs[0].Shards) == 0 || recs[0].Shards[0].Zone == "" {
		return "", 0, fmt.Errorf("no zone or score returned in capacity advice: %q", respBody)
	}
	if recs[0].Scores.Obtainability <= capacityAdvisorStockoutScore {
		return "", 0, fmt.Errorf("no zone has capacity (obtainability %.2f)", recs[0].Scores.Obtainability)
	}
	// The zone is a URL, so keep only its name.
	return path.Base(recs[0].Shards[0].Zone), recs[0].Scores.Obtainability, nil
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

	// Fall back across candidate regions to mitigate stockouts.
	// testParams.GkeClusterRegion (which defaults to us-central1) is prioritized first,
	// followed by capacityAdvisorFallbackRegions with typically lower contention.
	candidateRegions := []string{testParams.GkeClusterRegion}
	for _, r := range capacityAdvisorFallbackRegions {
		if r != testParams.GkeClusterRegion && (!testParams.EnableZB || len(zbSupportedZones[r]) > 0) {
			candidateRegions = append(candidateRegions, r)
		}
	}

	var nodeLocations string

	// Use Capacity Advisor to select an unconstrained region for both Standard and Autopilot clusters.
	if testParams.UseCapacityAdvisor {
		type capacityResult struct {
			region string
			zone   string
			score  float64
		}
		var results []capacityResult

		// The access token is valid for every region, so fetch it once.
		// TODO(b/570275744): Remove the token fetch when queryCapacityAdvice uses gcloud.
		if token, err := gcloudAccessToken(testParams); err != nil {
			klog.Warningf("Skipping Capacity Advisor queries: %v", err)
		} else {
			for _, region := range candidateRegions {
				zone, score, err := queryCapacityAdvice(testParams, region, token)
				if err != nil {
					klog.Warningf("Capacity Advisor query failed for region %q: %v", region, err)
					continue
				}
				klog.Infof("Capacity Advisor for region %q: zone=%q, obtainability=%.2f", region, zone, score)
				results = append(results, capacityResult{region: region, zone: zone, score: score})
			}
		}

		if len(results) > 0 {
			// Sort descending by obtainability score. Stable sort preserves priority for earlier candidate regions on ties.
			sort.SliceStable(results, func(i, j int) bool {
				return results[i].score > results[j].score
			})

			best := results[0]
			klog.Infof("Stack-ranked candidate regions:")
			for idx, r := range results {
				klog.Infof("  #%d: region=%q zone=%q score=%.2f", idx+1, r.region, r.zone, r.score)
			}
			klog.Infof("Selecting highest-scoring region %q (zone: %q, score: %.2f)", best.region, best.zone, best.score)

			// Note: Updating GkeClusterRegion (and GkeClusterZone for ZB) here implicitly updates the
			// --test-bucket-location passed to Ginkgo. This ensures that the
			// GCS buckets created during tests are co-located in this fallback
			// region/zone, preventing cross-region or cross-zone data transfer.
			testParams.GkeClusterRegion = best.region
			testParams.GkeClusterZone = best.zone
			// Autopilot manages node placement dynamically across zones within the region,
			// so --node-locations is only applied to Standard clusters.
			if !testParams.UseGKEAutopilot {
				nodeLocations = best.zone
			}
		} else {
			klog.Warningf("All Capacity Advisor queries failed, falling back to default regional node allocation in %s", testParams.GkeClusterRegion)
		}
	}

	// gcloud interprets --num-nodes as a per-zone count. When node-locations pins a
	// single zone, numNodes is the total count across the cluster. Without pinned node-locations,
	// GKE defaults to allocating nodes across all zones in the region (typically 3 zones),
	// so scale numNodes down (minimum 1) to keep the total cluster node count consistent.
	numNodes := testParams.NumNodes
	if !testParams.UseGKEAutopilot && nodeLocations == "" {
		numNodes = max(1, testParams.NumNodes/3)
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
		"--num-nodes", strconv.Itoa(numNodes), "--image-type", testParams.NodeImageType,
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
