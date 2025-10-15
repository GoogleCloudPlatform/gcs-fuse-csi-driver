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
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPvAnnotationIntersection(t *testing.T) {
	// Define the structure for our test cases
	testCases := []struct {
		name        string
		pv          *corev1.PersistentVolume
		annotations []string
		want        []string
	}{
		{
			name: "Partial match",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"key1": "value1",
						"key3": "value3",
					},
				},
			},
			annotations: []string{"key1", "key2", "key3"},
			want:        []string{"key1", "key3"},
		},
		{
			name: "Full match",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"key1": "value1",
						"key2": "value2",
					},
				},
			},
			annotations: []string{"key1", "key2"},
			want:        []string{"key1", "key2"},
		},
		{
			name: "No match",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"key1": "value1",
					},
				},
			},
			annotations: []string{"key2", "key3"},
			want:        nil, // Expect nil because the initial slice is never appended to
		},
		{
			name: "PV has no annotations",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: nil,
				},
			},
			annotations: []string{"key1", "key2"},
			want:        nil,
		},
		{
			name: "Input annotation list is empty",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"key1": "value1",
					},
				},
			},
			annotations: []string{},
			want:        nil,
		},
	}

	// Iterate over the test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := PvAnnotationIntersection(tc.pv, tc.annotations)

			// Use reflect.DeepEqual for robust slice comparison
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pvAnnotationIntersection() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateStorageProfilesOverrideStatus(t *testing.T) {
	testCases := []struct {
		name        string
		pv          *corev1.PersistentVolume
		wantErr     bool
		wantErrCode codes.Code
	}{
		// --- Non-Override Mode Tests ---
		{
			name: "Non-override mode: metadata but no annotations",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{},
			},
			wantErr: false,
		},
		{
			name: "Non-override mode: success with no conflicting annotations",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"some-other-annotation": "value",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Non-override mode: failure with conflicting annotations",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationStatus:     "some-other-status",
						AnnotationNumObjects: "123", // This should not be here
					},
				},
			},
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			name:    "Non-override mode: success with no annotations at all",
			pv:      &corev1.PersistentVolume{},
			wantErr: false,
		},
		// --- Override Mode Tests (Happy Path) ---
		{
			name: "Override mode: success with all required and valid annotations",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationStatus:     ScanOverride,
						AnnotationNumObjects: "1000",
						AnnotationTotalSize:  "2048",
						AnnotationHNSEnabled: "true",
					},
				},
			},
			wantErr: false,
		},
		// --- Override Mode Tests (Failure Cases) ---
		{
			name: "Override mode: failure with missing required annotation",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationStatus:     ScanOverride,
						AnnotationNumObjects: "1000",
						// annotationTotalSize is missing
						AnnotationHNSEnabled: "true",
					},
				},
			},
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			name: "Override mode: failure with invalid numObjects value",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationStatus:     ScanOverride,
						AnnotationNumObjects: "not-a-number",
						AnnotationTotalSize:  "2048",
						AnnotationHNSEnabled: "true",
					},
				},
			},
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			name: "Override mode: failure with negative totalSize value",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationStatus:     ScanOverride,
						AnnotationNumObjects: "1000",
						AnnotationTotalSize:  "-2048",
						AnnotationHNSEnabled: "true",
					},
				},
			},
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			name: "Override mode: failure with invalid boolean value",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationStatus:     ScanOverride,
						AnnotationNumObjects: "1000",
						AnnotationTotalSize:  "2048",
						AnnotationHNSEnabled: "yes", // not a valid bool
					},
				},
			},
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			err := ValidateStorageProfilesOverrideStatus(tc.pv)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Expected an error but got nil")
				}
				if s, ok := status.FromError(err); !ok || s.Code() != tc.wantErrCode {
					t.Errorf("Expected error with code %v, but got %v", tc.wantErrCode, s.Code())
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}
		})
	}
}
