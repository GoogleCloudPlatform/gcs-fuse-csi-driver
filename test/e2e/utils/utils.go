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
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

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
	DefaultNamespace              = "default"
	MinGCSFuseKernelParamsVersion = "v3.7.0-gke.0"
	MinGCSFuseTestConfigVersion   = "v3.7.0-gke.0"
	MinGCSFusProfilesVersion      = "v3.7.1-gke.0"
	MinGCSFuseGrpcMetricsVersion  = "v3.8.0-gke.0"

	GcsfuseVersionVarName = "gcsfuse-version"

	testConfigUrlFormat                  = "https://raw.githubusercontent.com/GoogleCloudPlatform/gcsfuse/%s/tools/integration_tests/test_config.yaml"
	flagFileCacheCapacity                = "file-cache-max-size-mb"
	flagOptions                          = "o"
	flagCacheDir                         = "cache-dir"
	flagLogSeverity                      = "log-severity"
	flagFileCacheEnableParallelDownloads = "file-cache-enable-parallel-downloads"
	flagFileCacheEnableODirect           = "file-cache-enable-o-direct"
	flagEnableKernelReader               = "enable-kernel-reader"
	flagFileCacheEnableCrc               = "file-cache-enable-crc"
	flagFileCacheCacheFileForRangeRead   = "file-cache-cache-file-for-range-read"
	flagLogFile                          = "log-file"
	flagLogFormat                        = "log-format"
)

var (
	MasterBranchName = "master"

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

	// disallowedFlagsMapping maps the disallowed flags to their config file representation.
	// See: https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/585b8addb42335e0742be7059fd6570c78b62bc6/pkg/sidecar_mounter/sidecar_mounter_config.go#L83
	// Note: If you add more disallowed flags in sidecar_mounter_config.go or use new ones in gcsfuse tests,
	// you should update this mapping accordingly to ensure they are correctly translated to the config file format.
	disallowedFlagsMapping = map[string]string{
		"log-format": "logging:format",
		"log-file":   "logging:file-path",
		"o":          "o",
		"cache-dir":  "cache-dir",

		// The following flags are currently unused in the gcsfuse e2e tests.
		"temp-dir":             "temp-dir",
		"config-file":          "config-file",
		"foreground":           "foreground",
		"prometheus-port":      "prometheus-port",
		"key-file":             "gcs-auth:key-file",
		"token-url":            "gcs-auth:token-url",
		"reuse-token-from-url": "gcs-auth:reuse-token-from-url",
		"kernel-params-file":   "file-system:kernel-params-file",
	}
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

func LoadTestConfig(gcsfuseVersion string) error {
	_, branch := GCSFuseBranch(gcsfuseVersion)
	klog.Infof("LoadTestConfig: Loading test config for GCSFuse branch %v", branch)
	url := fmt.Sprintf(testConfigUrlFormat, branch)
	klog.Infof("LoadTestConfig: Fetching test config from %v", url)
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
		klog.Errorf("LoadTestConfig: failed to parse test config: %v", err)
		return fmt.Errorf("failed to parse test config: %w", err)
	}

	klog.Infof("LoadTestConfig: Successfully loaded and parsed %d test packages", len(config))
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

	for _, f := range strings.Split(flagStr, ",") {
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

		// The following flags are disallowed in the CSI driver's mountOptions.
		// Because the sidecar disallowed flag map only contains the CLI format flags and not
		// the config file format flags, we work around this in the test suite by using
		// the config file format (x:y) instead.
		// See: https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/585b8addb42335e0742be7059fd6570c78b62bc6/pkg/sidecar_mounter/sidecar_mounter_config.go#L83
		if configPrefix, ok := disallowedFlagsMapping[flagName]; ok {
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

// Checks if the gcsfuse version supports test_config.yaml for integration tests.
func IsReadFromTestConfig(gcsfuseVersionStr string) bool {
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
					Name:    webhook.GcsFuseSidecarName,
					Image:   image,
					Command: []string{"/gcsfuse", "--version"},
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
