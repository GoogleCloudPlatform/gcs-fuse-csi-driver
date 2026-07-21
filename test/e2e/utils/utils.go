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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	sidecarmounter "github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/sidecar_mounter"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"gopkg.in/yaml.v3"
)

const (
	DefaultNamespace                         = "default"
	MinGCSFuseKernelParamsVersion            = "v3.7.0-gke.0"
	MinGCSFuseTestConfigVersion              = "v3.7.0-gke.0"
	MinGCSFuseMetricsCardinalityFixesVersion = "v3.7.2-gke.0" // The minimum version where we stop exporting metrics if a pod has more than 10 GCSFuse volumes
	MinGCSFuseGrpcMetricsVersion             = "v3.8.0-gke.0"

	GcsfuseVersionVarName = "gcsfuse-version"

	testConfigUrlFormat                  = "https://raw.githubusercontent.com/GoogleCloudPlatform/gcsfuse/%s/tools/integration_tests/test_config.yaml"
	flagFileCacheCapacity                = "file-cache-max-size-mb"
	flagOptions                          = "o"
	flagCacheDir                         = "cache-dir"
	flagLogSeverity                      = "log-severity"
	flagFileCacheEnableParallelDownloads = "file-cache-enable-parallel-downloads"
	flagLogFile                          = "log-file"
	flagLogFormat                        = "log-format"
)

var (
	MasterBranchName = "master"
	GCSFusePRNumber  = os.Getenv("GCSFUSE_PR_NUMBER")

	// Use release branch for corresponding gcsfuse version. This ensures we
	// can pick up test fixes without requiring a new gcsfuse release.
	gcsfuseReleaseBranchFormat = "v%v.%v.%v_release"
	configMapBackoff           = wait.Backoff{
		Duration: 200 * time.Millisecond,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    5,
	}
	httpRetryBackoff = wait.Backoff{
		Duration: 500 * time.Millisecond,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    3,
	}

	// Test packages loaded from test_config.yaml in gcsfuse repository.
	LoadedTestPackages TestPackages
)

func EnsureVariable(v *string, set bool, msgOnError string) {
	if set && len(*v) == 0 {
		klog.Fatal(msgOnError)
	} else if !set && len(*v) != 0 {
		klog.Fatal(msgOnError)
	}
}

func isVariableSet(v string) bool {
	return len(v) != 0
}

func runCommand(action string, cmd *exec.Cmd) error {
	cmd.Stdout = ginkgo.GinkgoWriter
	cmd.Stdin = os.Stdin
	cmd.Stderr = ginkgo.GinkgoWriter

	klog.Infof("%s", action)
	klog.Infof("cmd env=%v", cmd.Env)
	klog.Infof("cmd path=%v", cmd.Path)
	klog.Infof("cmd args=%s", cmd.Args)

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Wait()
}

func GCSFuseBranch(gcsfuseVersionStr string) (*version.Version, string) {
	v, err := version.ParseSemantic(gcsfuseVersionStr)
	// When the gcsfuse binary is built using the head commit or has a pre-release tag that does not contain "-gke",
	// (e.g. v3.3.0-gke.1) it is a development build. Always use the master branch for these builds.
	if err != nil || (v.PreRelease() != "" && !strings.Contains(gcsfuseVersionStr, "-gke")) {
		return nil, MasterBranchName
	}

	return v, fmt.Sprintf(gcsfuseReleaseBranchFormat, v.Major(), v.Minor(), v.Patch())
}

type TestPackage struct {
	// TestBucket is the name of the bucket used for the tests.
	// Loaded from `test_bucket` field in e.g. tools/integration_tests/test_config.yaml.
	TestBucket string `yaml:"test_bucket"`
	// MountedDirectory is the primary directory where the bucket is mounted.
	// Loaded from `mounted_directory` field in the yaml.
	MountedDirectory string `yaml:"mounted_directory"`
	// MountedDirectorySecondary is an optional secondary directory for dual-mount scenarios.
	// Loaded from `mounted_directory_secondary` field in the yaml.
	MountedDirectorySecondary string `yaml:"mounted_directory_secondary"`
	// OnlyDir specifies a subdirectory within the bucket to mount, instead of the root.
	// Loaded from `only_dir` field in the yaml.
	OnlyDir string `yaml:"only_dir"`
	// Configs is a list of test configurations (flags, compatibility, etc.) for this package.
	// Loaded from `configs` field in the yaml.
	Configs []TestConfig `yaml:"configs"`
}

type TestConfig struct {
	Flags          []string       `yaml:"flags"`
	Compatible     TestBucketType `yaml:"compatible"`
	Run            string         `yaml:"run"`
	RunOnGke       bool           `yaml:"run_on_gke"`
	SecondaryFlags []string       `yaml:"secondary_flags"`
}

type TestBucketType struct {
	Flat  bool `yaml:"flat"`
	HNS   bool `yaml:"hns"`
	Zonal bool `yaml:"zonal"`
}

type TestPackages map[string][]TestPackage

// LoadTestConfig loads the test_config.yaml from the gcsfuse repository.
// If GCSFUSE_PR_NUMBER is set, it fetches the config from the PR's head commit.
// Otherwise, it resolves the release branch from the gcsfuse version string.
func LoadTestConfig(gcsfuseVersion string) error {
	if GCSFusePRNumber != "" {
		return loadTestConfigFromPR(GCSFusePRNumber)
	}
	_, branch := GCSFuseBranch(gcsfuseVersion)
	klog.Infof("LoadTestConfig: Loading test config for GCSFuse branch %v", branch)
	url := fmt.Sprintf(testConfigUrlFormat, branch)
	return fetchAndParseTestConfig(url)
}

// loadTestConfigFromPR queries the GitHub API to resolve the head commit SHA for the given PR Number.
// It then fetches the raw test_config.yaml from that specific commit via raw.githubusercontent.com.
// Finally, it parses the YAML content to populate LoadedTestPackages for dynamic test generation.
func loadTestConfigFromPR(prNumber string) error {
	klog.Infof("loadTestConfigFromPR: Fetching PR info for PR %v", prNumber)
	apiURL := fmt.Sprintf("https://api.github.com/repos/GoogleCloudPlatform/gcsfuse/pulls/%s", prNumber)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	// GitHub API requires a User-Agent header.
	req.Header.Set("User-Agent", "gcs-fuse-csi-driver-e2e")

	// Fetch PR info from GitHub API with exponential backoff.
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}
	var resp *http.Response
	err = wait.ExponentialBackoff(httpRetryBackoff, func() (bool, error) {
		var httpErr error
		resp, httpErr = httpClient.Do(req)
		if httpErr != nil {
			klog.Warningf("Failed to fetch PR info, retrying: %v", httpErr)
			return false, nil
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			klog.Warningf("Failed to fetch PR info (status %d), retrying", resp.StatusCode)
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to fetch PR info after %d retries: %w", httpRetryBackoff.Steps, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read PR info body: %w", err)
	}

	// Define a minimal struct to extract only the head commit SHA from the PR response.
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return fmt.Errorf("failed to unmarshal PR info: %w", err)
	}

	// Extract the resolved SHA.
	sha := pr.Head.SHA
	if sha == "" {
		return fmt.Errorf("failed to resolve head SHA for PR %s", prNumber)
	}
	klog.Infof("loadTestConfigFromPR: Found PR head SHA %v", sha)

	configURL := fmt.Sprintf(testConfigUrlFormat, sha)
	return fetchAndParseTestConfig(configURL)
}

// fetchAndParseTestConfig fetches the test_config.yaml from the given URL,
// parses it, and stores the result in LoadedTestPackages.
func fetchAndParseTestConfig(url string) error {
	klog.Infof("fetchAndParseTestConfig: Fetching test config from %v", url)
	var resp *http.Response
	err := wait.ExponentialBackoff(httpRetryBackoff, func() (bool, error) {
		var httpErr error
		resp, httpErr = http.Get(url)
		if httpErr != nil {
			klog.Warningf("Failed to fetch test config, retrying: %v", httpErr)
			return false, nil
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			klog.Warningf("Failed to fetch test config (status %d), retrying", resp.StatusCode)
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to fetch test config after %d retries: %w", httpRetryBackoff.Steps, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read test config body: %w", err)
	}

	config, err := ParseTestConfig(body)
	if err != nil {
		return fmt.Errorf("failed to parse test config: %w", err)
	}

	klog.Infof("fetchAndParseTestConfig: Successfully loaded and parsed %d test packages", len(config))
	LoadedTestPackages = config
	return nil
}

func ParseTestConfig(body []byte) (TestPackages, error) {
	var config TestPackages
	if err := yaml.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal test config: %w", err)
	}

	return config, nil
}

func IsParallelDownloadsEnabled(flag string) bool {
	return strings.Contains(flag, flagFileCacheEnableParallelDownloads) && !strings.Contains(flag, flagFileCacheEnableParallelDownloads+"=false")
}

func IsFileCacheEnabled(flagStr string) bool {
	return strings.Contains(flagStr, flagCacheDir)
}

func IsOnlyDirEnabled(testPackage TestPackage) bool {
	return testPackage.OnlyDir != ""
}

type ParsedConfig struct {
	FileCacheCapacity string
	ReadOnly          bool
	LogFilePath       string
	LogSeverity       string
	CacheDir          string
	BillingProject    string
	MountOptions      []string
}

// ParseConfigFlags parses a comma-separated string of flags into a ParsedConfig struct.
// It removed the leading '-' from each flag and appends the flag to MountOptions.
func ParseConfigFlags(flagStr string) ParsedConfig {
	parsed := ParsedConfig{
		FileCacheCapacity: "-1Mi",
		ReadOnly:          false,
		LogFilePath:       "",
		LogSeverity:       "info",
		CacheDir:          "",
		MountOptions:      []string{},
	}

	// Replace commas with spaces to handle both comma-separated (used in tests)
	// and space-separated flag strings.
	// See: https://github.com/GoogleCloudPlatform/gcsfuse/blob/376c8c1638cd62fcefb3e614c02ae9901f59f3c1/tools/integration_tests/test_config.yaml#L511
	flagStr = strings.ReplaceAll(flagStr, ",", " ")
	for _, f := range strings.Fields(flagStr) {
		// Trim spaces and all leading '-' characters
		f = strings.TrimLeft(strings.TrimSpace(f), "-")
		if f == "" {
			continue
		}

		flagName, flagValue, found := strings.Cut(f, "=")

		if found && flagValue == "" {
			klog.Infof("ParseConfigFlags: Swallowing flag %q because its value is empty", f)
			continue
		}

		// file-cache:max-size-mb is used by the CSI driver to enable the file cache feature and configure the cache volume.
		// If not provided or set to 0, the cache directory will not be created.
		// See: https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/e46b334ea7365351d55853a95ae185c25ea64df6/pkg/sidecar_mounter/sidecar_mounter_config.go#L237-L241
		if found && flagName == flagFileCacheCapacity {
			parsed.FileCacheCapacity = flagValue + "Mi"
			continue
		}
		// "log-severity" is parsed here because the test defaults to "info" and we want to override it.
		// See: https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/585b8addb42335e0742be7059fd6570c78b62bc6/test/e2e/specs/testdriver.go#L245
		if found && flagName == flagLogSeverity {
			parsed.LogSeverity = flagValue
			continue
		}

		if found && flagName == "billing-project" {
			// Override the hardcoded billing project from gcsfuse test_config.yaml
			// with the actual project where the CSI driver tests are running.
			// See: https://github.com/GoogleCloudPlatform/gcsfuse/blob/376c8c1638cd62fcefb3e614c02ae9901f59f3c1/tools/integration_tests/test_config.yaml#L228
			flagValue = os.Getenv(ProjectEnvVar)
			parsed.BillingProject = flagValue
			f = "billing-project=" + flagValue
		}

		// The following flags are disallowed in the CSI driver's mountOptions.
		// Because the sidecar disallowed flag map only contains the CLI format flags and not
		// the config file format flags, we work around this in the test suite by using
		// the config file format (x:y) instead.
		// See: https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/585b8addb42335e0742be7059fd6570c78b62bc6/pkg/sidecar_mounter/sidecar_mounter_config.go#L83
		if configPrefix, ok := sidecarmounter.DisallowedFlags[flagName]; ok {
			switch flagName {
			case flagOptions:
				// "o" is disallowed and will be used in SetupVolume to set the readOnly flag for test pod.
				if flagValue == "ro" {
					parsed.ReadOnly = true
				}
				continue
			case flagCacheDir:
				// "cache-dir" is disallowed to prevent storage exhaustion. The gcsfuse e2e tests hardcoded the cache dir in their test config.
				// We parse this to manually set up the cache volume in the test pod to align with the gcsfuse test config,
				// while the CSI driver's sidecar-mounter will also automatically populate the "cache-dir" in its config file flag map
				// if the file cache is enabled.
				// See: https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/585b8addb42335e0742be7059fd6570c78b62bc6/pkg/sidecar_mounter/sidecar_mounter_config.go#L239-L241
				parsed.CacheDir = flagValue
				continue
			case flagLogFile:
				// LogFilePath is used by the test framework to tail the log file from the test pod.
				parsed.LogFilePath = flagValue
			case flagLogFormat:
				// "log-format" is disallowed, but we want to translate it to logging:format and pass it to the driver.
			default:
				// Note: If GCSFuse adds a new test that uses one of these flags in disallowedFlagsMapping
				// but not present in the cases above, it will be swallowed here. We log it to help identify
				// cases that require a manual update to ParseConfigFlags.
				klog.Infof("ParseConfigFlags: Swallowing disallowed flag %q", f)
				continue
			}

			if found {
				f = configPrefix + ":" + flagValue
			}
		}

		parsed.MountOptions = append(parsed.MountOptions, f)
	}

	return parsed
}

// IsReadFromTestConfig checks if the gcsfuse version supports test_config.yaml for integration tests.
// It also returns true when GCSFUSE_PR_NUMBER is set, since the PR is assumed to have a test_config.yaml.
func IsReadFromTestConfig(gcsfuseVersionStr string) bool {
	if GCSFusePRNumber != "" {
		return true
	}

	if gcsfuseVersionStr == "" {
		return false
	}

	v, branch := GCSFuseBranch(gcsfuseVersionStr)
	if branch == MasterBranchName {
		return true
	}

	return v.AtLeast(version.MustParseSemantic(MinGCSFuseTestConfigVersion))
}

// Extracts the only-dir UUID from the mountOptions string.
func ExtractOnlyDirFromMountOptions(mountOptionsStr string) string {
	for _, option := range strings.Split(mountOptionsStr, ",") {
		if strings.HasPrefix(option, "only-dir=") {
			return strings.TrimPrefix(option, "only-dir=")
		}
	}
	return ""
}

// FetchGCSFuseVersion deploys a temporary pod to safely retrieve the specific GCSFuse version
// from the test cluster. This is particularly needed for Managed Driver scenarios,
// bypassing the Ginkgo framework to avoid cyclic dependency issues cleanly.
func FetchGCSFuseVersion(ctx context.Context, cl clientset.Interface) (string, error) {
	configMaps, err := cl.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=gcsfusecsi-image-config",
	})
	if err != nil {
		return "", fmt.Errorf("failed to list configmaps: %v", err)
	}
	if len(configMaps.Items) != 1 {
		return "", fmt.Errorf("expected one config map `gcsfusecsi-image-config` but found %d", len(configMaps.Items))
	}

	sidecarImageConfig := configMaps.Items[0]
	image := sidecarImageConfig.Data["sidecar-image"]
	if image == "" {
		return "", fmt.Errorf("expected data for key `sidecar-image` in the config map `gcsfusecsi-image-config`")
	}

	// Deploy a temporary Pod to run the gcsfuse binary and fetch its version.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "gcsfuse-version-fetcher-",
			Namespace:    DefaultNamespace,
		},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: ptr.To(int64(0)),
			Containers: []corev1.Container{
				{
					Name:            webhook.GcsFuseSidecarName,
					Image:           image,
					ImagePullPolicy: corev1.PullAlways,
					Command:         []string{"/gcsfuse", "--version"},
				},
				{
					Name:    "sleeper",
					Image:   "busybox",
					Command: []string{"sleep", "infinity"},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
			Tolerations: []corev1.Toleration{
				{Operator: corev1.TolerationOpExists},
			},
		},
	}

	createdPod, err := cl.CoreV1().Pods(DefaultNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("Pods.Create failed with error: %v", err)
	}
	if createdPod != nil {
		defer cl.CoreV1().Pods(DefaultNamespace).Delete(context.Background(), createdPod.Name, metav1.DeleteOptions{})
	}

	// Wait for pod to be running
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		p, err := cl.CoreV1().Pods(DefaultNamespace).Get(ctx, createdPod.Name, metav1.GetOptions{})
		if err != nil {
			klog.Infof("Failed to get pod %s, retrying: %v", createdPod.Name, err)
			return false, nil
		}
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodSucceeded {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("timeout waiting for pod %s to be running: %v", createdPod.Name, err)
	}

	klog.Infof("Pod %s is running, waiting 60s before fetching GCSFuse version from logs", createdPod.Name)
	time.Sleep(60 * time.Second)

	// Fetch logs from the specific container running the gcsfuse binary.
	// We retry reading logs because it might take time for the logs to propagate to the apiserver.
	var logs []byte
	req := cl.CoreV1().Pods(DefaultNamespace).GetLogs(createdPod.Name, &corev1.PodLogOptions{Container: webhook.GcsFuseSidecarName})
	err = wait.PollUntilContextTimeout(ctx, 30*time.Second, time.Minute*10, true, func(ctx context.Context) (bool, error) {
		logs, err = req.DoRaw(ctx)
		if err != nil {
			klog.Infof("failed to read pod logs, retrying: %v", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to read pod logs: %w", err)
	}

	output := string(logs)

	// Parse the version string from the standard format: "gcsfuse version 3.7.1-gke.0 ..."
	l := strings.Split(strings.TrimSpace(output), " ")
	if len(l) <= 2 {
		return "", fmt.Errorf("unexpected version output format: %s", output)
	}
	return l[2], nil
}

// ExpandFlagVariables allows for the expansion of custom parameterized fields
// (e.g., ${BUCKET_NAME}) in a flag string based on the provided vars map.
// It is used to dynamically populate flags parsed from the gcsfuse test_config.yaml file.
// See: https://github.com/GoogleCloudPlatform/gcsfuse/blob/6ed3eeadfdbe6fdc59fc297520e1c311388068bf/tools/integration_tests/test_config.yaml#L402
func ExpandFlagVariables(flag string, vars map[string]string) string {
	return os.Expand(flag, func(envVar string) string {
		if val, ok := vars[envVar]; ok {
			return val
		}
		return os.Getenv(envVar)
	})
}
