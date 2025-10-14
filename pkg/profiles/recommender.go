/*
Copyright 2025 Google LLC

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

package profiles

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/cloud_provider/clientset"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	// StorageClass param keys
	workloadTypeKey                          = "workloadType"
	workloadTypeInferenceKey                 = "inference"
	workloadTypeTrainingKey                  = "training"
	workloadTypeCheckpointingKey             = "checkpointing"
	fuseFileCacheMediumPriorityKey           = "fuseFileCacheMediumPriority"
	fuseMemoryAllocatableFactorKey           = "fuseMemoryAllocatableFactor"
	fuseEphemeralStorageAllocatableFactorKey = "fuseEphemeralStorageAllocatableFactor"
)

// ProfileConfig holds the consolidated configuration for a volume profile,
// derived from PersistentVolume annotations and StorageClass parameters.
type ProfileConfig struct {
	pvDetails                             *pvDetails          // Details extracted from the PersistentVolume's annotations.
	fileCacheMediumPriority               map[string][]string // Parsed priority map for file cache mediums.
	fuseMemoryAllocatableFactor           float64             // Factor for calculating FUSE memory allocation.
	fuseEphemeralStorageAllocatableFactor float64             // Factor for calculating FUSE ephemeral storage allocation.
	VolumeAttributes                      map[string]string   // Arbitrary volume attributes sourced from the StorageClass.
	mountOptions                          []string            // Mount options sourced from the StorageClass.
}

// pvDetails holds a summary of information about a PersistentVolume,
// typically extracted from its annotations.
type pvDetails struct {
	numObjects     int64  // The number of objects reported by the PV.
	totalSizeBytes int64  // The total size in bytes reported by the PV.
	name           string // The name of the PersistentVolume.
}

// BuildProfileConfig constructs a ProfileConfig by fetching and validating
// information from the PersistentVolume and StorageClass associated with the given targetPath.
// It extracts volume details from PV annotations and configuration parameters from the SC.
// volumeAttributeKeys is used to filter parameters from the StorageClass that should be
// treated as volume attributes.
func BuildProfileConfig(targetPath string, clientset clientset.Interface, volumeAttributeKeys map[string]struct{}) (*ProfileConfig, error) {
	// Get the PV name from target path.
	_, volumeName, err := util.ParsePodIDVolumeFromTargetpath(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get pv name from target path %q: %v", targetPath, err)
	}

	// Get the PV object using the PV name.
	pv, err := clientset.GetPV(volumeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "pv %q not found: %v", volumeName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get pv %q: %v", volumeName, err)
	}

	// Get the PV details from the PV object's annotations.
	pvDetails, err := pvDetailsFromAnnotations(pv)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get PV details from annotations for %q: %v", pv.Name, err)
	}

	// Get the StorageClassName from the PV object.
	scName := pv.Spec.StorageClassName
	if scName == "" {
		// Since the feature is opt-in, the user should specify a StorageClassName.
		return nil, status.Errorf(codes.InvalidArgument, "pv %q has empty StorageClassName", pv.Name)
	}

	// Get the StorageClass object from the StorageClassName.
	sc, err := clientset.GetSC(scName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "sc %q not found: %v", scName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get StorageClass %q: %v", scName, err)
	}

	// Get the parameters from the StorageClass.
	if sc.Parameters == nil {
		return nil, status.Errorf(codes.InvalidArgument, "sc %q found but has nil Parameters map for volume %q", scName, pv.Name)
	}

	// Validate the workloadType parameter for the profile.
	workloadType, ok := sc.Parameters[workloadTypeKey]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "missing workloadType parameter in StorageClass %q for PV %q", scName, pv.Name)
	}
	if err := validateWorkloadType(workloadType); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to validate workloadType parameter in StorageClass %q for PV %q: %v", scName, pv.Name, err)
	}

	// Get the file cache medium priority from the StorageClass parameters.
	fileCacheMediumPriorityStr, ok := sc.Parameters[fuseFileCacheMediumPriorityKey]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "missing fuseFileCacheMediumPriority in StorageClass %q for PV %q", scName, pv.Name)
	}
	fileCacheMediumPriority, err := parseFileCacheMediumPriority(fileCacheMediumPriorityStr)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse fileCacheMediumPriority in StorageClass %q for PV %q: %v", scName, pv.Name, err)
	}

	// Get the fuse memory allocatable factor from the StorageClass parameters.
	fuseMemoryAllocatableFactorVal, err := parseFloatParameterNonNegative(sc.Parameters, fuseMemoryAllocatableFactorKey)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse fuse memory allocatable factor param in StorageClass %q for PV %q: %v", scName, pv.Name, err)
	}

	// Get the fuse ephemeral storage allocatable factor from the StorageClass parameters.
	fuseEphemeralStorageAllocatableFactorVal, err := parseFloatParameterNonNegative(sc.Parameters, fuseEphemeralStorageAllocatableFactorKey)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse fuse ephemeral storage allocatable factor param in StorageClass %q for PV %q: %v", scName, pv.Name, err)
	}

	// Parse volume attributes from the StorageClass parameters. Only select from keys recognized by the CSI Driver as volume attributes.
	volumeAttributes := selectFromMapIfKeysMatch(sc.Parameters, volumeAttributeKeys)

	return &ProfileConfig{
		pvDetails:                             pvDetails,
		fileCacheMediumPriority:               fileCacheMediumPriority,
		fuseEphemeralStorageAllocatableFactor: fuseEphemeralStorageAllocatableFactorVal,
		fuseMemoryAllocatableFactor:           fuseMemoryAllocatableFactorVal,
		VolumeAttributes:                      volumeAttributes,
		mountOptions:                          sc.MountOptions,
	}, nil
}

// parseFloatParameterNonNegative extracts a parameter by key from the params map,
// parses it as a float64, and returns an error if the key is missing, the value
// is not a valid float, or the value is negative.
func parseFloatParameterNonNegative(params map[string]string, key string) (float64, error) {
	stringVal, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing %q", key)
	}
	floatVal, err := strconv.ParseFloat(stringVal, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %q: %w", key, err)
	}
	if floatVal < 0 {
		return 0, fmt.Errorf("%q is < 0", key)
	}
	return floatVal, nil
}

// parseFileCacheMediumPriority parses a comma-separated string of key:value pairs
// into a map. Each value can be a pipe-separated list of strings.
// Example input: "hdd:val1|val2,ssd:val3"
// Output: {"hdd": ["val1", "val2"], "ssd": ["val3"]}
// It returns an error if the input string is malformed.
func parseFileCacheMediumPriority(input string) (map[string][]string, error) {
	result := make(map[string][]string)
	if strings.TrimSpace(input) == "" {
		return result, nil
	}
	pairs := strings.Split(input, ",")
	for i, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed pair found at index %d: %q", i, pair)
		}
		key := strings.TrimSpace(parts[0])
		valueString := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("found pair with empty key at index %d: %q", i, pair)
		}
		var values []string
		if valueString != "" {
			rawValues := strings.Split(valueString, "|")
			for _, val := range rawValues {
				trimmedVal := strings.TrimSpace(val)
				if trimmedVal != "" {
					values = append(values, trimmedVal)
				}
			}
		}
		result[key] = values
	}
	return result, nil
}

// pvDetailsFromAnnotations extracts and parses the number of objects and total size
// annotations from the given PersistentVolume. It returns a pvDetails struct containing
// the parsed values. An error is returned if the required annotations are missing,
// empty, or cannot be parsed into their expected integer types.
func pvDetailsFromAnnotations(
	pv *corev1.PersistentVolume) (*pvDetails, error) {
	// Extract annotations
	pvAnnotations := pv.GetAnnotations()
	if pvAnnotations == nil {
		// Return internal error, since the mount shouldn't have started if the PV hasn't finished scanning.
		return nil, fmt.Errorf("PV %q has no annotations", pv.Name)
	}

	// Get annotation values as strings
	numObjectsStr, numObjectsFound := pvAnnotations[annotationNumObjects]
	totalSizeStr, totalSizeFound := pvAnnotations[annotationTotalSize]

	// Check for missing annotations
	var missingAnnotations []string
	if !numObjectsFound {
		missingAnnotations = append(missingAnnotations, annotationNumObjects)
	}
	if !totalSizeFound {
		missingAnnotations = append(missingAnnotations, annotationTotalSize)
	}

	if len(missingAnnotations) > 0 {
		// Return internal error, since the mount shouldn't have started if the PV hasn't finished scanning.
		return nil, fmt.Errorf("PV %q is missing required annotations: %s", pv.Name, strings.Join(missingAnnotations, ", "))
	}

	// Parse annotations into int64
	var parseErrors []string
	numObjects, err := strconv.ParseInt(numObjectsStr, 10, 64)
	if err != nil {
		parseErrors = append(parseErrors, fmt.Sprintf("failed to parse %s value %q: %v", annotationNumObjects, numObjectsStr, err))
	}
	totalSizeBytes, err := strconv.ParseInt(totalSizeStr, 10, 64)
	if err != nil {
		parseErrors = append(parseErrors, fmt.Sprintf("failed to parse %s value %q: %v", annotationTotalSize, totalSizeStr, err))
	}
	if len(parseErrors) > 0 {
		errorMsg := strings.Join(parseErrors, "; ")
		// Return internal error, since the annotations should have been validated either by GCW or the scanner controller.
		return nil, fmt.Errorf("invalid annotation format on PV %q: %s", pv.Name, errorMsg)
	}

	return &pvDetails{
		name:           pv.Name,
		numObjects:     numObjects,
		totalSizeBytes: totalSizeBytes,
	}, nil
}

// selectFromMapIfKeysMatch returns a new map containing entries from the 'target' map
// only if their keys are also present in the 'keys' map. The values
// in the returned map are taken from the 'target' map.
func selectFromMapIfKeysMatch(target map[string]string, keys map[string]struct{}) map[string]string {
	result := make(map[string]string)
	keysCaseInsensitive := map[string]struct{}{}
	for key := range keys {
		keysCaseInsensitive[strings.ToLower(key)] = struct{}{}
	}

	for key, val := range target {
		if _, keyExists := keysCaseInsensitive[strings.ToLower(key)]; keyExists {
			result[key] = val
		}
	}
	return result
}

// validateWorkloadType checks if the workload type is valid.
func validateWorkloadType(workloadType string) error {
	switch workloadType {
	case workloadTypeInferenceKey, workloadTypeTrainingKey, workloadTypeCheckpointingKey:
	default:
		return fmt.Errorf("invalid %q parameter %q", workloadTypeKey, workloadType)
	}
	return nil
}
