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
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseKernelParamsConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		setupFile      func(t *testing.T) string
		expectedConfig *KernelParamsConfig
		expectError    bool
	}{
		{
			name: "Success",
			setupFile: func(t *testing.T) string {
				content := `{
					"request_id": "test-req-id",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "max-read-ahead-kb", "value": "2048"}
					]
				}`
				path := filepath.Join(t.TempDir(), "valid_params.json")
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectedConfig: &KernelParamsConfig{
				RequestID: "test-req-id",
				Timestamp: "2026-02-02T12:00:00Z",
				Parameters: []KernelParam{
					{Name: MaxReadAheadKb, Value: "2048"},
				},
			},
			expectError: false,
		},
		{
			name: "Fail_FileDoesNotExist",
			setupFile: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "non_existent.json")
			},
			expectError: true,
		},
		{
			name: "Fail_InvalidJSON",
			setupFile: func(t *testing.T) string {
				content := `invalid-json-content`
				path := filepath.Join(t.TempDir(), "invalid_params.json")
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectError: true,
		},
		{
			name: "Success_EmptyParameters",
			setupFile: func(t *testing.T) string {
				content := `{
					"request_id": "test-req-id",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": []
				}`
				path := filepath.Join(t.TempDir(), "empty_params.json")
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectedConfig: &KernelParamsConfig{
				RequestID:  "test-req-id",
				Timestamp:  "2026-02-02T12:00:00Z",
				Parameters: []KernelParam{},
			},
			expectError: false,
		},
		{
			name: "Success_PartialValues",
			setupFile: func(t *testing.T) string {
				content := `{
					"request_id": "",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "max-read-ahead-kb", "value": "2048"}
					]
				}`
				path := filepath.Join(t.TempDir(), "partial_values.json")
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectedConfig: &KernelParamsConfig{
				RequestID: "",
				Timestamp: "2026-02-02T12:00:00Z",
				Parameters: []KernelParam{
					{Name: MaxReadAheadKb, Value: "2048"},
				},
			},
			expectError: false,
		},
		{
			name: "Success_ExtraFields",
			setupFile: func(t *testing.T) string {
				content := `{
					"request_id": "test-req-id",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "max-read-ahead-kb", "value": "2048"}
					],
					"extra_field": "extra_value"
				}`
				path := filepath.Join(t.TempDir(), "extra_fields.json")
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectedConfig: &KernelParamsConfig{
				RequestID: "test-req-id",
				Timestamp: "2026-02-02T12:00:00Z",
				Parameters: []KernelParam{
					{Name: MaxReadAheadKb, Value: "2048"},
				},
			},
			expectError: false,
		},
		{
			name: "Success_ExtraFieldsInParameter",
			setupFile: func(t *testing.T) string {
				content := `{
					"request_id": "test-req-id",
					"timestamp": "2026-02-02T12:00:00Z",
					"parameters": [
						{"name": "max-read-ahead-kb", "value": "2048", "extra": "field"}
					]
				}`
				path := filepath.Join(t.TempDir(), "extra_fields_in_param.json")
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatalf("failed to write temp file: %v", err)
				}
				return path
			},
			expectedConfig: &KernelParamsConfig{
				RequestID: "test-req-id",
				Timestamp: "2026-02-02T12:00:00Z",
				Parameters: []KernelParam{
					{Name: MaxReadAheadKb, Value: "2048"},
				},
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			filePath := tc.setupFile(t)
			config, err := parseKernelParamsConfig(filePath)

			if tc.expectError {
				if err == nil {
					t.Errorf("parseKernelParamsConfig(%q) expected error, got nil", filePath)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseKernelParamsConfig(%q) unexpected error: %v", filePath, err)
			}

			if !reflect.DeepEqual(config, tc.expectedConfig) {
				t.Errorf("parseKernelParamsConfig(%q) = %+v, want %+v", filePath, config, tc.expectedConfig)
			}
		})
	}
}
