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
	"encoding/json"
	"fmt"
	"os"
)

// File kernel_params_contract.go defines the strict schema and contract used for
// inter-process communication between the GCSFuse Sidecar and the GKE GCSFuse CSI Driver.
//
// Purpose:
// This file facilitates the "Zero Configuration" feature where GCSFuse automatically
// determines optimal kernel settings (e.g., for Zonal Buckets) and communicates them
// to the CSI Driver for enforcement.
//
//
// CRITICAL:
// This file acts as a shared contract. Any changes here must be compatible with
// both the producer (GCSFuse) and the consumer (CSI Driver).
//
// BREAKING CHANGES
// 1. Renaming any JSON tag (e.g., changing `json:"request_id"` to `json:"id"`).
// 2. Removing an existing field from a struct.
// 3. Changing the data type of a field (e.g., string to int).
// 4. Changing the string value of an existing ParamName constant.
//
// NON-BREAKING CHANGES:
// 1. Adding a new field with a new JSON tag.
// 2. Adding a new ParamName constant.
// Follow this guide to make any changes to this contract: TODO(mohit)

// ParamName acts as an Enum for the parameter keys to ensure contract safety from typo errors.
type ParamName string

const (
	MaxReadAheadKb            ParamName = "max-read-ahead-kb"
	MaxBackgroundRequests     ParamName = "fuse-max-background-requests"
	CongestionWindowThreshold ParamName = "fuse-congestion-window-threshold"
)

// KernelParam represents an individual parameter setting.
type KernelParam struct {
	Name  ParamName `json:"name"`
	Value string    `json:"value"`
}

/*
KernelParamsConfig acts as the primary container for kernel settings.
Example Json config:

	{
		  "request_id": "10c8148f-b26f-4ba4-9d09-e854311eb5d6",
		  "timestamp": "2026-02-04T02:26:45.622211445Z",
		  "parameters": [
		    {
		      "name": "max-read-ahead-kb",
		      "value": "16384"
		    },
		    {
		      "name": "fuse-congestion-window-threshold",
		      "value": "12"
		    },
		    {
		      "name": "fuse-max-background-requests",
		      "value": "16"
		    }
		  ]
	}
*/
type KernelParamsConfig struct {
	RequestID  string        `json:"request_id"`
	Timestamp  string        `json:"timestamp"` // Format: 2026-01-12T16:23:05.636831Z
	Parameters []KernelParam `json:"parameters"`
}

// parseKernelParamsConfig parses the Kernel Parameter Configuration written by GCSFuse at kernelParamsFilePath.
// It returns error in case of contract mismatch or parsing error.
// GCSFuse writes this file atomically so it's safe to read this file at any point.
func parseKernelParamsConfig(kernelParamsFilePath string) (*KernelParamsConfig, error) {
	data, err := os.ReadFile(kernelParamsFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read kernel params file %q: %w", kernelParamsFilePath, err)
	}
	var config KernelParamsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal kernel params config: %w", err)
	}
	return &config, nil
}
