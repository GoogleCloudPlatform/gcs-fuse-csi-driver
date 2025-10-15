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

package util

import (
	"fmt"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
)

const (
	// Annotation keys
	AnnotationPrefix          = "gke-gcsfuse"
	AnnotationStatus          = AnnotationPrefix + "/bucket-scan-status"
	AnnotationNumObjects      = AnnotationPrefix + "/bucket-scan-num-objects"
	AnnotationTotalSize       = AnnotationPrefix + "/bucket-scan-total-size-bytes"
	AnnotationLastUpdatedTime = AnnotationPrefix + "/bucket-scan-last-updated-time"
	AnnotationHNSEnabled      = AnnotationPrefix + "/bucket-scan-hns-enabled"

	ScanOverride = "override"
)

var (
	requiredOverrideAnnotations = []string{
		AnnotationNumObjects,
		AnnotationTotalSize,
		AnnotationHNSEnabled,
	}
)

// ValidateStorageProfilesOverrideStatus returns error for incorrect usage of profile override annotations
func ValidateStorageProfilesOverrideStatus(pv *corev1.PersistentVolume) error {
	if pv.Annotations[AnnotationStatus] != ScanOverride {
		if annotationsUsed := PvAnnotationIntersection(pv, []string{
			AnnotationStatus,
			AnnotationNumObjects,
			AnnotationTotalSize,
			AnnotationHNSEnabled,
		}); len(annotationsUsed) > 0 {
			return status.Errorf(codes.InvalidArgument, "scanner annotations for PV %q found in non-override mode: %+v", pv.Name, annotationsUsed)
		}
		return nil
	}

	_, _, _, err := ParseOverrideStatus(pv)
	if err != nil {
		return err
	}

	return nil
}

// pvAnnotationIntersection returns the intersection of the provided annotation keys and
// the annotation keys found in the PV.
func PvAnnotationIntersection(pv *corev1.PersistentVolume, annotations []string) []string {
	var intersection []string
	for _, key := range annotations {
		if _, exists := pv.Annotations[key]; exists {
			intersection = append(intersection, key)
		}
	}
	return intersection
}

// parseNonNegativeIntegerFromString returns an error if the string fails
// to be parsed as an integer, or if the integer is negative.
func ParseNonNegativeIntegerFromString(val string) (int64, error) {
	valInt, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, err
	}
	if valInt < 0 {
		return 0, fmt.Errorf("value must be non-negative, got: %q", val)
	}
	return valInt, nil
}

// parseOverrideStatus checks that, if the override mode is set, the
// PV has the required annotations with valid format/types.
// Returns the required annotations (parsed) if valid, or returns an error otherwise.
func ParseOverrideStatus(pv *v1.PersistentVolume) (int64, int64, bool, error) {
	// Enforce required annotations for override mode and validate formats.
	var numObjects int64
	var totalSizeBytes int64
	var isHNSEnabled bool
	for _, key := range requiredOverrideAnnotations {
		if _, exists := pv.Annotations[key]; !exists {
			return numObjects, totalSizeBytes, isHNSEnabled, status.Errorf(codes.InvalidArgument, "status %q requires annotation %q", ScanOverride, key)
		}
		switch key {
		case AnnotationNumObjects:
			val, err := ParseNonNegativeIntegerFromString(pv.Annotations[key])
			if err != nil {
				return numObjects, totalSizeBytes, isHNSEnabled, status.Errorf(codes.InvalidArgument, "invalid value for annotation %q: %v", key, err)
			}
			numObjects = val
		case AnnotationTotalSize:
			val, err := ParseNonNegativeIntegerFromString(pv.Annotations[key])
			if err != nil {
				return numObjects, totalSizeBytes, isHNSEnabled, status.Errorf(codes.InvalidArgument, "invalid value for annotation %q: %v", key, err)
			}
			totalSizeBytes = val
		case AnnotationHNSEnabled:
			val, err := strconv.ParseBool(pv.Annotations[AnnotationHNSEnabled])
			if err != nil {
				return numObjects, totalSizeBytes, isHNSEnabled, status.Errorf(codes.InvalidArgument, "invalid value for annotation %q: %v", key, err)
			}
			isHNSEnabled = val
		default:
			// This should never happen, but it's safer to check.
			return numObjects, totalSizeBytes, isHNSEnabled, status.Errorf(codes.Internal, "unexpected key for override mode requiredAnnotations: %q", key)
		}
	}
	return numObjects, totalSizeBytes, isHNSEnabled, nil
}
