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

	"github.com/onsi/ginkgo/v2"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	DefaultNamespace              = "default"
	MinGCSFuseKernelParamsVersion = "v3.7.0-gke.0"
	MinGCSFuseTestConfigVersion   = "v3.5.0-gke.0"

	testConfigUrlFormat                  = "https://raw.githubusercontent.com/GoogleCloudPlatform/gcsfuse/%s/tools/integration_tests/test_config.yaml"
	flagFileCacheCapacity                = "file-cache-max-size-mb="
	flagReadOnly                         = "o=ro"
	flagLogFile                          = "log-file="
	flagCacheDir                         = "cache-dir="
	flagLogSeverity                      = "log-severity="
	flagLogFormat                        = "log-format="
	flagFileCacheEnableParallelDownloads = "file-cache-enable-parallel-downloads"
	flagFileCacheEnableODirect           = "file-cache-enable-o-direct"
	flagEnableKernelReader               = "enable-kernel-reader"
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

func ReadConfigMap(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, name string,
) (map[string]string, error) {

	cm, err := client.CoreV1().
		ConfigMaps(namespace).
		Get(ctx, name, metav1.GetOptions{})

	if err != nil {
		return nil, err
	}

	return cm.Data, nil
}

func UpsertConfigMap(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, name string,
	data map[string]string,
) error {

	cmClient := client.CoreV1().ConfigMaps(namespace)

	// First try create
	_, err := cmClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: data,
	}, metav1.CreateOptions{})

	if err == nil {
		return nil
	}

	if !apierrors.IsAlreadyExists(err) {
		return err
	}

	// Exists → update with retry on conflict
	return wait.ExponentialBackoffWithContext(ctx, configMapBackoff, func(ctx context.Context) (bool, error) {
		existing, err := cmClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if existing.Data == nil {
			existing.Data = make(map[string]string)
		}
		for k, v := range data {
			existing.Data[k] = v
		}

		_, err = cmClient.Update(ctx, existing, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return false, nil
		}

		return true, err
	})
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
	Flags      []string       `yaml:"flags"`
	Compatible TestBucketType `yaml:"compatible"`
	Run        string         `yaml:"run"`
	RunOnGke   bool           `yaml:"run_on_gke"`
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
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to fetch test config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch test config: status code %d", resp.StatusCode)
	}

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
	return strings.Contains(flag, "file-cache-enable-parallel-downloads") && !strings.Contains(flag, "file-cache-enable-parallel-downloads=false")
}

func IsFileCacheEnabled(testName string) bool {
	return testName == "read_cache"
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
		FileCacheCapacity: "50Mi",
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

		// The following flags are in the disallowedFlags list in sidecar_mounter_config.go.
		// They cannot be passed via mountOptions and must be parsed to configure the test environment manually.
		// See: https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/main/pkg/sidecar_mounter/sidecar_mounter_config.go#L83

		// Group 1: Flags that are parsed manually and should not be passed to mountOptions.

		// file-cache:max-size-mb is used by the CSI driver to enable the file cache feature and configure the cache volume.
		// If not provided or set to 0, the cache directory will not be created.
		// See: https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/main/pkg/sidecar_mounter/sidecar_mounter_config.go#L237-L241
		if strings.HasPrefix(f, flagFileCacheCapacity) {
			parsed.FileCacheCapacity = strings.TrimPrefix(f, flagFileCacheCapacity) + "Mi"
			continue
		}

		// "o" is disallowed and will be used in SetupVolume to set the readOnly flag for test pod.
		if f == flagReadOnly {
			parsed.ReadOnly = true
			continue
		}

		// "cache-dir" is disallowed to prevent storage exhaustion. The gcsfuse e2e tests hardcoded the cache dir in their test config.
		// We parse this to manually set up the cache volume in the test pod to align with the gcsfuse test config.
		if strings.HasPrefix(f, flagCacheDir) {
			parsed.CacheDir = strings.TrimPrefix(f, flagCacheDir)
			continue
		}

		// "log-severity" is disallowed so we parse it into the test pod configuration.
		if strings.HasPrefix(f, flagLogSeverity) {
			parsed.LogSeverity = strings.TrimPrefix(f, flagLogSeverity)
			continue
		}

		// Group 2: The following flags are translated to config file representation and passed to mountOptions.

		// "log-file" is disallowed. The gcsfuse e2e tests hardcoded the log file in their test config.s
		// We parse this to configure the test pod to align with the gcsfuse test config.
		if strings.HasPrefix(f, flagLogFile) {
			parsed.LogFilePath = strings.TrimPrefix(f, flagLogFile)
			f = "logging:file-path:" + parsed.LogFilePath
		} else if f == flagFileCacheEnableParallelDownloads || strings.HasPrefix(f, flagFileCacheEnableParallelDownloads+"=") {
			val := "true"
			if strings.Contains(f, "=") {
				val = strings.SplitN(f, "=", 2)[1]
			}
			f = "file-cache:enable-parallel-downloads:" + val
		} else if f == flagFileCacheEnableODirect || strings.HasPrefix(f, flagFileCacheEnableODirect+"=") {
			val := "true"
			if strings.Contains(f, "=") {
				val = strings.SplitN(f, "=", 2)[1]
			}
			f = "file-cache:enable-o-direct:" + val
		} else if f == flagEnableKernelReader || strings.HasPrefix(f, flagEnableKernelReader+"=") {
			val := "true"
			if strings.Contains(f, "=") {
				val = strings.SplitN(f, "=", 2)[1]
			}
			f = "file-system:enable-kernel-reader:" + val
		} else if strings.HasPrefix(f, flagLogFormat) {
			// "log-format" is disallowed so we need to format it to config file format
			f = "logging:format:" + strings.TrimPrefix(f, flagLogFormat)
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
