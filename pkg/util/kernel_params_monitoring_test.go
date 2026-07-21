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
	origPath := procSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		procSysFsFuseMaxPagesLimitPath = origPath
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
			procSysFsFuseMaxPagesLimitPath = tc.setup(t, tempDir)
			result := FuseMaxMaxPagesUpdateSupported()
			if result != tc.expectedResult {
				t.Errorf("Expected %v, got %v", tc.expectedResult, result)
			}
		})
	}
}

func TestReadFuseMaxPagesLimit(t *testing.T) {
	origPath := procSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		procSysFsFuseMaxPagesLimitPath = origPath
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
			procSysFsFuseMaxPagesLimitPath = tc.setup(t, tempDir)
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
	origPath := procSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		procSysFsFuseMaxPagesLimitPath = origPath
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
			procSysFsFuseMaxPagesLimitPath = tempFile

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
	origPath := procSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		procSysFsFuseMaxPagesLimitPath = origPath
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
			procSysFsFuseMaxPagesLimitPath = tempFile

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
	origPath := procSysFsFuseMaxPagesLimitPath
	t.Cleanup(func() {
		procSysFsFuseMaxPagesLimitPath = origPath
	})

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "max_pages_limit")
	procSysFsFuseMaxPagesLimitPath = tempFile

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
