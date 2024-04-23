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

package sidecarmounter

import (
	"os"
	"reflect"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"gopkg.in/yaml.v3"
)

var (
	defaultFlagMap = map[string]string{
		"app-name":        GCSFuseAppName,
		"temp-dir":        "test-buffer-dir/temp-dir",
		"config-file":     "test-config-file",
		"foreground":      "",
		"uid":             "0",
		"gid":             "0",
		"prometheus-port": "0",
	}

	defaultConfigFileFlagMap = map[string]string{
		"logging:file-path": "/dev/fd/1",
		"logging:format":    "json",
		"cache-dir":         "",
	}

	invalidArgs = []string{
		"temp-dir",
		"config-file",
		"foreground",
		"log-file",
		"log-format",
		"key-file",
		"token-url",
		"reuse-token-from-url",
		"o",
		"logging:log-rotate:max-file-size-mb:test",
		"logging:log-rotate:backup-file-count:test",
		"logging:log-rotate:compress:test",
		"cache-dir",
		"experimental-local-file-cache",
	}
)

func TestPrepareMountArgs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                  string
		mc                    *MountConfig
		expectedArgs          map[string]string
		expectedConfigMapArgs map[string]string
	}{
		{
			name: "should return valid args correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
			},
			expectedArgs:          defaultFlagMap,
			expectedConfigMapArgs: defaultConfigFileFlagMap,
		},
		{
			name: "should return valid args with options correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
				Options:    []string{"uid=100", "gid=200", "debug_gcs", "max-conns-per-host=10", "implicit-dirs", "write:create-empty-file:false", "logging:severity:error", "write:create-empty-file:true"},
			},
			expectedArgs: map[string]string{
				"implicit-dirs":      "",
				"app-name":           GCSFuseAppName,
				"temp-dir":           "test-buffer-dir/temp-dir",
				"config-file":        "test-config-file",
				"foreground":         "",
				"uid":                "100",
				"gid":                "200",
				"prometheus-port":    "0",
				"debug_gcs":          "",
				"max-conns-per-host": "10",
			},
			expectedConfigMapArgs: map[string]string{
				"logging:file-path":       "/dev/fd/1",
				"logging:format":          "json",
				"logging:severity":        "error",
				"write:create-empty-file": "true",
				"cache-dir":               "",
			},
		},
		{
			name: "should return valid args with bool options correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
				Options:    []string{"uid=100", "gid=200", "debug_gcs", "max-conns-per-host=10", "implicit-dirs=true"},
			},
			expectedArgs: map[string]string{
				"implicit-dirs=true": "",
				"app-name":           GCSFuseAppName,
				"temp-dir":           "test-buffer-dir/temp-dir",
				"config-file":        "test-config-file",
				"foreground":         "",
				"uid":                "100",
				"gid":                "200",
				"prometheus-port":    "0",
				"debug_gcs":          "",
				"max-conns-per-host": "10",
			},
			expectedConfigMapArgs: defaultConfigFileFlagMap,
		},
		{
			name: "should return valid args with error correctly",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
				Options:    invalidArgs,
			},
			expectedArgs:          defaultFlagMap,
			expectedConfigMapArgs: defaultConfigFileFlagMap,
		},
		{
			name: "should return valid args with custom app-name",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
				Options:    []string{"app-name=Vertex"},
			},
			expectedArgs: map[string]string{
				"app-name":        GCSFuseAppName + "-Vertex",
				"temp-dir":        "test-buffer-dir/temp-dir",
				"config-file":     "test-config-file",
				"foreground":      "",
				"uid":             "0",
				"gid":             "0",
				"prometheus-port": "0",
			},
			expectedConfigMapArgs: defaultConfigFileFlagMap,
		},
		{
			name: "should return valid args when file cache is disabled",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
				Options:    []string{"file-cache:max-size-mb:0"},
			},
			expectedArgs: defaultFlagMap,
			expectedConfigMapArgs: map[string]string{
				"logging:file-path":      "/dev/fd/1",
				"logging:format":         "json",
				"cache-dir":              "",
				"file-cache:max-size-mb": "0",
			},
		},
		{
			name: "should return valid args when file cache is enabled with unlimited size",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
				Options:    []string{"file-cache:max-size-mb:-1"},
			},
			expectedArgs: defaultFlagMap,
			expectedConfigMapArgs: map[string]string{
				"logging:file-path":      "/dev/fd/1",
				"logging:format":         "json",
				"cache-dir":              "test-cache-dir",
				"file-cache:max-size-mb": "-1",
			},
		},
		{
			name: "should return valid args when file cache is enabled with a max size",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
				Options:    []string{"file-cache:max-size-mb:100"},
			},
			expectedArgs: defaultFlagMap,
			expectedConfigMapArgs: map[string]string{
				"logging:file-path":      "/dev/fd/1",
				"logging:format":         "json",
				"cache-dir":              "test-cache-dir",
				"file-cache:max-size-mb": "100",
			},
		},
		{
			name: "should return valid args when metrics is enabled",
			mc: &MountConfig{
				BucketName: "test-bucket",
				BufferDir:  "test-buffer-dir",
				CacheDir:   "test-cache-dir",
				ConfigFile: "test-config-file",
				Options:    []string{util.EnableMetricsForGKE + ":true"},
			},
			expectedArgs: map[string]string{
				"app-name":        GCSFuseAppName,
				"temp-dir":        "test-buffer-dir/temp-dir",
				"config-file":     "test-config-file",
				"foreground":      "",
				"uid":             "0",
				"gid":             "0",
				"prometheus-port": "8080",
			},
			expectedConfigMapArgs: defaultConfigFileFlagMap,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)

		tc.mc.prepareMountArgs()
		if !reflect.DeepEqual(tc.mc.FlagMap, tc.expectedArgs) {
			t.Errorf("Got args %v, but expected %v", tc.mc.FlagMap, tc.expectedArgs)
		}

		if !reflect.DeepEqual(tc.mc.ConfigFileFlagMap, tc.expectedConfigMapArgs) {
			t.Errorf("Got config file args %v, but expected %v", tc.mc.ConfigFileFlagMap, tc.expectedConfigMapArgs)
		}
	}
}

func TestPrepareConfigFile(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		mc             *MountConfig
		configMapArgs  map[string]string
		expectedConfig map[string]interface{}
		expectedErr    bool
	}{
		{
			name: "should create valid config file correctly",
			mc: &MountConfig{
				ConfigFile: "./test-config-file.yaml",
				ConfigFileFlagMap: map[string]string{
					"logging:file-path":                     "/dev/fd/1",
					"logging:format":                        "json",
					"logging:severity":                      "error",
					"write:create-empty-file":               "true",
					"file-cache:max-size-mb":                "10000",
					"file-cache:cache-file-for-range-read":  "true",
					"metadata-cache:stat-cache-max-size-mb": "1000",
					"metadata-cache:type-cache-max-size-mb": "-1",
					"cache-dir":                             "/gcsfuse-cache/.volumes/volume-name",
				},
			},
			expectedConfig: map[string]interface{}{
				"logging": map[string]interface{}{
					"file-path": "/dev/fd/1",
					"format":    "json",
					"severity":  "error",
				},
				"write": map[string]interface{}{
					"create-empty-file": true,
				},
				"file-cache": map[string]interface{}{
					"max-size-mb":               10000,
					"cache-file-for-range-read": true,
				},
				"metadata-cache": map[string]interface{}{
					"stat-cache-max-size-mb": 1000,
					"type-cache-max-size-mb": -1,
				},
				"cache-dir": "/gcsfuse-cache/.volumes/volume-name",
			},
		},
		{
			name: "should throw error when incorrect flag is passed",
			mc: &MountConfig{
				ConfigFile: "./test-config-file.yaml",
				ConfigFileFlagMap: map[string]string{
					"logging:file-path": "/dev/fd/1",
					"logging:format":    "json",
					"logging":           "invalid",
				},
			},
			expectedErr: true,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)

		err := tc.mc.prepareConfigFile()

		if (err != nil) != tc.expectedErr {
			t.Errorf("Got error %v, but expected error %v", err, tc.expectedErr)
		}

		if tc.expectedErr {
			continue
		}

		data, err := os.ReadFile(tc.mc.ConfigFile)
		if err != nil {
			t.Errorf("failed to read generated config file %q", tc.mc.ConfigFile)
		}

		var config map[string]interface{}

		err = yaml.Unmarshal(data, &config)
		if err != nil {
			t.Errorf("failed to parse generated config file %q", tc.mc.ConfigFile)
		}

		if !reflect.DeepEqual(config, tc.expectedConfig) {
			t.Errorf("Got config %v, but expected %v", config, tc.expectedConfig)
		}

		os.Remove(tc.mc.ConfigFile)
	}
}
