/*
Copyright 2026 The Kubernetes Authors.
Copyright 2026 Google LLC

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

package util

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGetDeviceMajorMinor(t *testing.T) {
	// Create a temporary file for testing
	f, err := os.CreateTemp("", "test_device_major_minor")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	// Get expected major/minor directly
	fi, err := os.Stat(f.Name())
	if err != nil {
		t.Fatalf("Failed to stat temp file: %v", err)
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("Failed to cast to syscall.Stat_t")
	}
	expectedMajor := unix.Major(uint64(stat.Dev))
	expectedMinor := unix.Minor(uint64(stat.Dev))

	// Call the function under test
	major, minor, err := getDeviceMajorMinor(f.Name())
	if err != nil {
		t.Errorf("getDeviceMajorMinor returned error: %v", err)
	}

	if major != expectedMajor {
		t.Errorf("Expected major %d, got %d", expectedMajor, major)
	}
	if minor != expectedMinor {
		t.Errorf("Expected minor %d, got %d", expectedMinor, minor)
	}
}

func TestGetDeviceMajorMinor_NonExistentPath(t *testing.T) {
	_, _, err := getDeviceMajorMinor("/non/existent/path")
	if err == nil {
		t.Error("Expected error for non-existent path, got nil")
	}
}

func TestCheckAndApplyKernelParams(t *testing.T) {
	t.Parallel()

	// Helper to create a temp file with content
	createTempFile := func(dir, name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("failed to write temp file %s: %v", name, err)
		}
		return path
	}

	testCases := []struct {
		name               string
		setup              func(t *testing.T, tempDir string) (string, map[ParamName]string)
		expectedSysfsValue string
		expectError        bool
	}{
		{
			name: "Success_UpdateParameter",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				// Create dummy sysfs file
				sysfsPath := createTempFile(tempDir, "read_ahead_kb", "128")

				// Create config file
				configContent := `{
					"request_id": "req-1",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "max-read-ahead-kb", "value": "256"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				return configPath, map[ParamName]string{
					MaxReadAheadKb: sysfsPath,
				}
			},
			expectedSysfsValue: "256\n",
			expectError:        false,
		},
		{
			name: "Success_SkipInvalidParameterValue",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				// Create dummy sysfs file
				sysfsPath := createTempFile(tempDir, "read_ahead_kb", "128")

				// Create config file
				configContent := `{
					"request_id": "req-1",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "max-read-ahead-kb", "value": "2000000"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				return configPath, map[ParamName]string{
					MaxReadAheadKb: sysfsPath,
				}
			},
			expectedSysfsValue: "128",
			expectError:        false,
		},
		{
			name: "Success_UpdateMaxBackgroundRequests",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				sysfsPath := createTempFile(tempDir, "max_background", "12")
				configContent := `{
					"request_id": "req-2",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "fuse-max-background-requests", "value": "16"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				return configPath, map[ParamName]string{
					MaxBackgroundRequests: sysfsPath,
				}
			},
			expectedSysfsValue: "16\n",
			expectError:        false,
		},
		{
			name: "Success_UpdateCongestionWindowThreshold",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				sysfsPath := createTempFile(tempDir, "congestion_threshold", "10")
				configContent := `{
					"request_id": "req-3",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "fuse-congestion-window-threshold", "value": "12"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				return configPath, map[ParamName]string{
					CongestionWindowThreshold: sysfsPath,
				}
			},
			expectedSysfsValue: "12\n",
			expectError:        false,
		},
		{
			name: "Success_SkipInvalidMaxBackgroundRequests",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				sysfsPath := createTempFile(tempDir, "max_background", "12")
				configContent := `{
					"request_id": "req-skip-2",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "fuse-max-background-requests", "value": "1001"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				return configPath, map[ParamName]string{
					MaxBackgroundRequests: sysfsPath,
				}
			},
			expectedSysfsValue: "12",
			expectError:        false,
		},
		{
			name: "Success_SkipInvalidCongestionWindowThreshold",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				sysfsPath := createTempFile(tempDir, "congestion_threshold", "10")
				configContent := `{
					"request_id": "req-skip-3",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "fuse-congestion-window-threshold", "value": "-1"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				return configPath, map[ParamName]string{
					CongestionWindowThreshold: sysfsPath,
				}
			},
			expectedSysfsValue: "10",
			expectError:        false,
		},
		{
			name: "Success_NoUpdateNeeded",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				sysfsPath := createTempFile(tempDir, "read_ahead_kb", "256")

				configContent := `{
					"request_id": "req-1",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "max-read-ahead-kb", "value": "256"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				return configPath, map[ParamName]string{
					MaxReadAheadKb: sysfsPath,
				}
			},
			expectedSysfsValue: "256", // Content shouldn't change (no newline added if not updated)
			expectError:        false,
		},
		{
			name: "Success_ConfigFileMissing",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				sysfsPath := createTempFile(tempDir, "read_ahead_kb", "128")
				configPath := filepath.Join(tempDir, "missing.json")
				return configPath, map[ParamName]string{
					MaxReadAheadKb: sysfsPath,
				}
			},
			expectedSysfsValue: "128",
			expectError:        false,
		},
		{
			name: "Fail_ParseError",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				sysfsPath := createTempFile(tempDir, "read_ahead_kb", "128")
				configPath := createTempFile(tempDir, "invalid.json", "{invalid-json}")
				return configPath, map[ParamName]string{
					MaxReadAheadKb: sysfsPath,
				}
			},
			expectedSysfsValue: "128",
			expectError:        true,
		},
		{
			name: "Success_UnknownParameter",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				sysfsPath := createTempFile(tempDir, "read_ahead_kb", "128")
				configContent := `{
					"request_id": "req-1",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "unknown-param", "value": "256"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				// Map contains MaxReadAheadKb, but config has unknown parameter
				return configPath, map[ParamName]string{
					MaxReadAheadKb: sysfsPath,
				}
			},
			expectedSysfsValue: "128",
			expectError:        false,
		},
		{
			name: "Success_SysfsFileMissing",
			setup: func(t *testing.T, tempDir string) (string, map[ParamName]string) {
				configContent := `{
					"request_id": "req-1",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "max-read-ahead-kb", "value": "256"}
					]
				}`
				configPath := createTempFile(tempDir, "kernel_params.json", configContent)

				return configPath, map[ParamName]string{
					MaxReadAheadKb: filepath.Join(tempDir, "missing_sysfs"),
				}
			},
			expectedSysfsValue: "", // No file to check
			expectError:        false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			configPath, pathMap := tc.setup(t, tempDir)

			err := checkAndApplyKernelParams(configPath, pathMap, "test-prefix")

			if tc.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}

			// Verify sysfs file content if it exists and was part of the test
			for _, sysfsPath := range pathMap {
				if _, err := os.Stat(sysfsPath); err == nil {
					content, err := os.ReadFile(sysfsPath)
					if err != nil {
						t.Fatalf("failed to read sysfs file: %v", err)
					}
					if string(content) != tc.expectedSysfsValue {
						t.Errorf("sysfs value mismatch for %q: got %q, want %q", sysfsPath, string(content), tc.expectedSysfsValue)
					}
				}
			}
		})
	}
}

func TestValidateParamValue(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		paramName   ParamName
		paramValue  string
		expectError bool
	}{
		// MaxReadAheadKb tests (0 to 1048576)
		{"MaxReadAheadKb_Valid_Min", MaxReadAheadKb, "0", false},
		{"MaxReadAheadKb_Valid_Max", MaxReadAheadKb, "1048576", false},
		{"MaxReadAheadKb_Valid_Mid", MaxReadAheadKb, "512", false},
		{"MaxReadAheadKb_Invalid_Low", MaxReadAheadKb, "-1", true},
		{"MaxReadAheadKb_Invalid_High", MaxReadAheadKb, "1048577", true},

		// MaxBackgroundRequests tests (1 to 1000)
		{"MaxBackgroundRequests_Valid_Min", MaxBackgroundRequests, "1", false},
		{"MaxBackgroundRequests_Valid_Max", MaxBackgroundRequests, "1000", false},
		{"MaxBackgroundRequests_Valid_Mid", MaxBackgroundRequests, "16", false},
		{"MaxBackgroundRequests_Invalid_Low", MaxBackgroundRequests, "0", true},
		{"MaxBackgroundRequests_Invalid_High", MaxBackgroundRequests, "1001", true},

		// CongestionWindowThreshold tests (0 to 1000)
		{"CongestionWindowThreshold_Valid_Min", CongestionWindowThreshold, "0", false},
		{"CongestionWindowThreshold_Valid_Max", CongestionWindowThreshold, "1000", false},
		{"CongestionWindowThreshold_Valid_Mid", CongestionWindowThreshold, "12", false},
		{"CongestionWindowThreshold_Invalid_Low", CongestionWindowThreshold, "-1", true},
		{"CongestionWindowThreshold_Invalid_High", CongestionWindowThreshold, "1001", true},

		// Unknown parameter
		{"UnknownParam", ParamName("unknown-param"), "10", true},

		// Invalid data types
		{"NotAnInteger", MaxReadAheadKb, "abc", true},
		{"FloatValue", MaxBackgroundRequests, "10.5", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateParamValue(tc.paramName, tc.paramValue)
			if tc.expectError && err == nil {
				t.Errorf("validateParamValue(%q, %q): expected error, got nil", tc.paramName, tc.paramValue)
			} else if !tc.expectError && err != nil {
				t.Errorf("validateParamValue(%q, %q): unexpected error: %v", tc.paramName, tc.paramValue, err)
			}
		})
	}
}

func TestFuseMaxMaxPagesUpdateSupported(t *testing.T) {
	origPath := ProcSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		ProcSysFsFuseMaxPagesLimitPath = origPath
	})

	testCases := []struct {
		name           string
		setup          func(t *testing.T, tempDir string) string
		expectedResult bool
	}{
		{
			name: "FileDoesNotExist",
			setup: func(t *testing.T, tempDir string) string {
				return filepath.Join(tempDir, "missing_file")
			},
			expectedResult: false,
		},
		{
			name: "FileExists",
			setup: func(t *testing.T, tempDir string) string {
				path := filepath.Join(tempDir, "existing_file")
				if err := os.WriteFile(path, []byte("16\n"), 0644); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectedResult: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			ProcSysFsFuseMaxPagesLimitPath = tc.setup(t, tempDir)
			result := FuseMaxMaxPagesUpdateSupported()
			if result != tc.expectedResult {
				t.Errorf("Expected %v, got %v", tc.expectedResult, result)
			}
		})
	}
}

func TestReadFuseMaxPagesLimit(t *testing.T) {
	origPath := ProcSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		ProcSysFsFuseMaxPagesLimitPath = origPath
	})

	testCases := []struct {
		name          string
		setup         func(t *testing.T, tempDir string) string
		expectedValue int64
		expectError   bool
	}{
		{
			name: "FileDoesNotExist",
			setup: func(t *testing.T, tempDir string) string {
				return filepath.Join(tempDir, "missing_file")
			},
			expectedValue: 0,
			expectError:   true,
		},
		{
			name: "ValidContent",
			setup: func(t *testing.T, tempDir string) string {
				path := filepath.Join(tempDir, "valid_file")
				if err := os.WriteFile(path, []byte("  256\n"), 0644); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectedValue: 256,
			expectError:   false,
		},
		{
			name: "InvalidContent",
			setup: func(t *testing.T, tempDir string) string {
				path := filepath.Join(tempDir, "invalid_file")
				if err := os.WriteFile(path, []byte("abc\n"), 0644); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectedValue: 0,
			expectError:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			ProcSysFsFuseMaxPagesLimitPath = tc.setup(t, tempDir)
			val, err := ReadFuseMaxPagesLimit()
			if tc.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if val != tc.expectedValue {
					t.Errorf("Expected %d, got %d", tc.expectedValue, val)
				}
			}
		})
	}
}

func TestSetFuseMaxPagesLimit(t *testing.T) {
	origPath := ProcSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		ProcSysFsFuseMaxPagesLimitPath = origPath
	})

	testCases := []struct {
		name            string
		inputLimit      int64
		expectedContent string
	}{
		{
			name:            "WritePositive",
			inputLimit:      512,
			expectedContent: "512\n",
		},
		{
			name:            "WriteZero",
			inputLimit:      0,
			expectedContent: "0\n",
		},
		{
			name:            "WriteNegative",
			inputLimit:      -1,
			expectedContent: "-1\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			tempFile := filepath.Join(tempDir, "max_pages_limit")
			ProcSysFsFuseMaxPagesLimitPath = tempFile

			if err := SetFuseMaxPagesLimit(tc.inputLimit); err != nil {
				t.Fatalf("SetFuseMaxPagesLimit failed: %v", err)
			}

			bytes, err := os.ReadFile(tempFile)
			if err != nil {
				t.Fatalf("failed to read temp file: %v", err)
			}
			if string(bytes) != tc.expectedContent {
				t.Errorf("Expected file content %q, got %q", tc.expectedContent, string(bytes))
			}
		})
	}
}

func TestMountUsingElevatedFuseMaxPagesLimit(t *testing.T) {
	origPath := ProcSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		ProcSysFsFuseMaxPagesLimitPath = origPath
	})

	testCases := []struct {
		name                 string
		initialLimit         int64
		targetLimit          int64
		fn                   func(tempFile string) func() error
		expectedMountError   bool
		expectedLimitInMount int64
		expectedFinalLimit   int64
		expectPanic          bool
	}{
		{
			name:         "should temporarily increase limit and restore it",
			initialLimit: 256,
			targetLimit:  512,
			fn: func(tempFile string) func() error {
				return func() error {
					// Inside the mount function, the limit should be increased to 512
					limit, err := ReadFuseMaxPagesLimit()
					if err != nil {
						return err
					}
					if limit != 512 {
						return fmt.Errorf("expected limit during mount to be 512, got %d", limit)
					}
					return nil
				}
			},
			expectedFinalLimit: 256,
		},
		{
			name:         "should not change limit if target is smaller or equal",
			initialLimit: 256,
			targetLimit:  128,
			fn: func(tempFile string) func() error {
				return func() error {
					limit, err := ReadFuseMaxPagesLimit()
					if err != nil {
						return err
					}
					if limit != 256 {
						return fmt.Errorf("expected limit during mount to remain 256, got %d", limit)
					}
					return nil
				}
			},
			expectedFinalLimit: 256,
		},
		{
			name:         "should propagate error and still restore limit",
			initialLimit: 256,
			targetLimit:  512,
			fn: func(tempFile string) func() error {
				return func() error {
					return errors.New("mount failed")
				}
			},
			expectedMountError: true,
			expectedFinalLimit: 256,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			tempFile := filepath.Join(tempDir, "max_pages_limit")
			ProcSysFsFuseMaxPagesLimitPath = tempFile

			if err := SetFuseMaxPagesLimit(tc.initialLimit); err != nil {
				t.Fatalf("SetFuseMaxPagesLimit failed: %v", err)
			}

			err := MountUsingElevatedFuseMaxPagesLimit(tc.targetLimit, "test", tc.fn(tempFile))
			if tc.expectedMountError && err == nil {
				t.Errorf("Expected mount error, got nil")
			}
			if !tc.expectedMountError && err != nil {
				t.Errorf("Unexpected mount error: %v", err)
			}

			finalLimit, err := ReadFuseMaxPagesLimit()
			if err != nil {
				t.Fatalf("failed to read final limit: %v", err)
			}
			if finalLimit != tc.expectedFinalLimit {
				t.Errorf("Expected final limit to be %d, got %d", tc.expectedFinalLimit, finalLimit)
			}
		})
	}
}

func TestMountUsingElevatedFuseMaxPagesLimitPanic(t *testing.T) {
	origPath := ProcSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		ProcSysFsFuseMaxPagesLimitPath = origPath
	})

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "max_pages_limit")
	ProcSysFsFuseMaxPagesLimitPath = tempFile

	if err := SetFuseMaxPagesLimit(256); err != nil {
		t.Fatalf("SetFuseMaxPagesLimit failed: %v", err)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Expected panic, did not panic")
			}
		}()

		_ = MountUsingElevatedFuseMaxPagesLimit(512, "test", func() error {
			panic("mount panic")
		})
	}()

	finalLimit, err := ReadFuseMaxPagesLimit()
	if err != nil {
		t.Fatalf("failed to read final limit: %v", err)
	}
	if finalLimit != 256 {
		t.Errorf("Expected final limit to be restored to 256, got %d", finalLimit)
	}
}

type fakeEthtoolClient struct {
	mu             sync.Mutex
	features       map[string]bool
	featuresErr    error
	changeErr      error
	changeErrByKey map[string]error
	changeCalls    []map[string]bool
	closed         bool
}

func (f *fakeEthtoolClient) Features(intf string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.featuresErr != nil {
		return nil, f.featuresErr
	}
	out := make(map[string]bool, len(f.features))
	for k, v := range f.features {
		out[k] = v
	}
	return out, nil
}

func (f *fakeEthtoolClient) Change(intf string, config map[string]bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	reqCopy := make(map[string]bool, len(config))
	for k, v := range config {
		reqCopy[k] = v
	}
	f.changeCalls = append(f.changeCalls, reqCopy)
	if f.changeErr != nil {
		return f.changeErr
	}
	for k := range config {
		if f.changeErrByKey != nil {
			if err, ok := f.changeErrByKey[k]; ok && err != nil {
				return err
			}
		}
	}
	for k, v := range config {
		f.features[k] = v
	}
	return nil
}

func (f *fakeEthtoolClient) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func TestEnableHwGro(t *testing.T) {
	origNewEthtool := newEthtoolClient
	origEnableHwGro := EnableHwGroFunc
	t.Cleanup(func() {
		newEthtoolClient = origNewEthtool
		EnableHwGroFunc = origEnableHwGro
	})

	t.Run("EnableBothWhenRxGroHwAndRxLROAreFalse", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": false,
				"rx-lro":    false,
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(fakeEth.changeCalls) != 2 {
			t.Fatalf("expected 2 change calls, got %d", len(fakeEth.changeCalls))
		}
		if !reflect.DeepEqual(fakeEth.changeCalls[0], map[string]bool{"rx-gro-hw": true}) {
			t.Errorf("changeCalls[0] mismatch: %v", fakeEth.changeCalls[0])
		}
		if !reflect.DeepEqual(fakeEth.changeCalls[1], map[string]bool{"rx-lro": true}) {
			t.Errorf("changeCalls[1] mismatch: %v", fakeEth.changeCalls[1])
		}
		if !fakeEth.features["rx-gro-hw"] {
			t.Errorf("expected rx-gro-hw to be true")
		}
		if !fakeEth.features["rx-lro"] {
			t.Errorf("expected rx-lro to be true")
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("EnableBothWhenRxGroHwAndLargeReceiveOffloadAreFalse", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw":             false,
				"large-receive-offload": false,
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(fakeEth.changeCalls) != 2 {
			t.Fatalf("expected 2 change calls, got %d", len(fakeEth.changeCalls))
		}
		if !reflect.DeepEqual(fakeEth.changeCalls[0], map[string]bool{"rx-gro-hw": true}) {
			t.Errorf("changeCalls[0] mismatch: %v", fakeEth.changeCalls[0])
		}
		if !reflect.DeepEqual(fakeEth.changeCalls[1], map[string]bool{"large-receive-offload": true}) {
			t.Errorf("changeCalls[1] mismatch: %v", fakeEth.changeCalls[1])
		}
		if !fakeEth.features["rx-gro-hw"] {
			t.Errorf("expected rx-gro-hw to be true")
		}
		if !fakeEth.features["large-receive-offload"] {
			t.Errorf("expected large-receive-offload to be true")
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("IdempotentWhenBothAlreadyTrue", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": true,
				"rx-lro":    true,
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(fakeEth.changeCalls) != 0 {
			t.Fatalf("expected 0 change calls, got %d", len(fakeEth.changeCalls))
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("EnablesOnlyMissingFeatureWhenOneAlreadyTrue", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": true,
				"rx-lro":    false,
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(fakeEth.changeCalls) != 1 {
			t.Fatalf("expected 1 change call, got %d", len(fakeEth.changeCalls))
		}
		if !reflect.DeepEqual(fakeEth.changeCalls[0], map[string]bool{"rx-lro": true}) {
			t.Errorf("changeCalls[0] mismatch: %v", fakeEth.changeCalls[0])
		}
		if !fakeEth.features["rx-lro"] {
			t.Errorf("expected rx-lro to be true")
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("IdempotentAcrossConsecutiveCalls", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": false,
				"rx-lro":    false,
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err1 := EnableHwGroOnDefaultNIC()
		err2 := EnableHwGroOnDefaultNIC()

		// Assert
		if err1 != nil {
			t.Fatalf("unexpected error1: %v", err1)
		}
		if err2 != nil {
			t.Fatalf("unexpected error2: %v", err2)
		}
		if len(fakeEth.changeCalls) != 2 {
			t.Fatalf("expected 2 change calls across both runs, got %d", len(fakeEth.changeCalls))
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("RxGroHwChangeFailsButLROSucceeds", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": false,
				"rx-lro":    false,
			},
			changeErrByKey: map[string]error{
				"rx-gro-hw": errors.New("rx-gro-hw ioctl not supported"),
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err == nil {
			t.Fatalf("expected error because rx-gro-hw failed, got nil")
		}
		if !strings.Contains(err.Error(), "rx-gro-hw ioctl not supported") {
			t.Errorf("expected error to contain 'rx-gro-hw ioctl not supported', got %v", err)
		}
		if !fakeEth.features["rx-lro"] {
			t.Errorf("expected rx-lro to still succeed and become true despite rx-gro-hw failure")
		}
		if fakeEth.features["rx-gro-hw"] {
			t.Errorf("expected rx-gro-hw to remain false")
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("LROChangeFailsButRxGroHwSucceeds", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": false,
				"rx-lro":    false,
			},
			changeErrByKey: map[string]error{
				"rx-lro": errors.New("rx-lro ioctl failed"),
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err == nil {
			t.Fatalf("expected error because lro failed, got nil")
		}
		if !strings.Contains(err.Error(), "rx-lro ioctl failed") {
			t.Errorf("expected error to contain 'rx-lro ioctl failed', got %v", err)
		}
		if !fakeEth.features["rx-gro-hw"] {
			t.Errorf("expected rx-gro-hw to still succeed and become true despite lro failure")
		}
		if fakeEth.features["rx-lro"] {
			t.Errorf("expected rx-lro to remain false")
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("RxGroHwMissingInFeaturesButLROSucceeds", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-lro": false,
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err == nil {
			t.Fatalf("expected error because rx-gro-hw is missing in features, got nil")
		}
		if !strings.Contains(err.Error(), "rx-gro-hw feature not found in ethtool features") {
			t.Errorf("expected error to mention missing rx-gro-hw feature, got %v", err)
		}
		if len(fakeEth.changeCalls) != 1 {
			t.Fatalf("expected 1 ioctl change call for rx-lro, got %d", len(fakeEth.changeCalls))
		}
		if !reflect.DeepEqual(fakeEth.changeCalls[0], map[string]bool{"rx-lro": true}) {
			t.Errorf("changeCalls[0] mismatch: %v", fakeEth.changeCalls[0])
		}
		if !fakeEth.features["rx-lro"] {
			t.Errorf("expected rx-lro to be true")
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("LROMissingInFeaturesButRxGroHwSucceeds", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": false,
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err == nil {
			t.Fatalf("expected error because lro is missing in features, got nil")
		}
		if !strings.Contains(err.Error(), "lro feature not found in ethtool features") {
			t.Errorf("expected error to mention missing lro feature, got %v", err)
		}
		if len(fakeEth.changeCalls) != 1 {
			t.Fatalf("expected 1 ioctl change call for rx-gro-hw, got %d", len(fakeEth.changeCalls))
		}
		if !reflect.DeepEqual(fakeEth.changeCalls[0], map[string]bool{"rx-gro-hw": true}) {
			t.Errorf("changeCalls[0] mismatch: %v", fakeEth.changeCalls[0])
		}
		if !fakeEth.features["rx-gro-hw"] {
			t.Errorf("expected rx-gro-hw to be true")
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("ReturnsErrorWhenEthtoolClientCreationFails", func(t *testing.T) {
		// Arrange
		newEthtoolClient = func() (ethtoolClient, error) {
			return nil, errors.New("socket ioctl error")
		}
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "socket ioctl error") {
			t.Errorf("expected error to contain 'socket ioctl error', got %v", err)
		}
	})

	t.Run("SkipsWithoutErrorWhenNICNotPresentOnNonCOSHost", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			featuresErr: unix.ENODEV,
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err != nil {
			t.Fatalf("expected nil error when eth0 is absent (ENODEV), got: %v", err)
		}
		if len(fakeEth.changeCalls) != 0 {
			t.Fatalf("expected 0 change calls when eth0 is absent, got %d", len(fakeEth.changeCalls))
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("ReturnsErrorWhenFeaturesCallFails", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			featuresErr: errors.New("features error"),
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "features error") {
			t.Errorf("expected error to contain 'features error', got %v", err)
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("ReturnsErrorWhenBothFeaturesFail", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": false,
				"rx-lro":    false,
			},
			changeErr: errors.New("change error"),
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		// Act
		err := EnableHwGroOnDefaultNIC()

		// Assert
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to enable rx-gro-hw") {
			t.Errorf("expected error to contain 'failed to enable rx-gro-hw', got %v", err)
		}
		if !strings.Contains(err.Error(), "failed to enable rx-lro") {
			t.Errorf("expected error to contain 'failed to enable rx-lro', got %v", err)
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})

	t.Run("ReturnsErrorOnEmptyOrInvalidNICName", func(t *testing.T) {
		// Arrange
		EnableHwGroFunc = enableHwGroOnNIC
		invalidNICs := []string{"", "   ", "-K", "eth0;id", "eth0 /etc/shadow"}

		for _, nic := range invalidNICs {
			t.Run(fmt.Sprintf("NIC_%q", nic), func(t *testing.T) {
				// Act
				err := enableHwGroOnNIC(nic)

				// Assert
				if err == nil {
					t.Fatalf("expected error for NIC %q, got nil", nic)
				}
			})
		}
	})

	t.Run("ConcurrentCallsAreSerializedAndThreadSafe", func(t *testing.T) {
		// Arrange
		fakeEth := &fakeEthtoolClient{
			features: map[string]bool{
				"rx-gro-hw": false,
				"rx-lro":    false,
			},
		}
		newEthtoolClient = func() (ethtoolClient, error) { return fakeEth, nil }
		EnableHwGroFunc = enableHwGroOnNIC

		var wg sync.WaitGroup
		numWorkers := 10
		wg.Add(numWorkers)

		// Act
		for i := 0; i < numWorkers; i++ {
			go func() {
				defer wg.Done()
				err := EnableHwGroOnDefaultNIC()
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		wg.Wait()

		// Assert
		if len(fakeEth.changeCalls) != 2 {
			t.Fatalf("expected 2 change calls (one for rx-gro-hw and one for rx-lro), got %d", len(fakeEth.changeCalls))
		}
		if !fakeEth.closed {
			t.Errorf("expected client to be closed")
		}
	})
}
