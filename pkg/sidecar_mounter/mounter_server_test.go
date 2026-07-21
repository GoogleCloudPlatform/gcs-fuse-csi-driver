/*
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

package sidecarmounter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/proto/mounter"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMounterServer_Mount(t *testing.T) {
	ctx := context.Background()

	tempDir := t.TempDir()
	dummySleepScript := filepath.Join(tempDir, "dummy-sleep.sh")
	if err := os.WriteFile(dummySleepScript, []byte("#!/bin/sh\nexec sleep 10\n"), 0755); err != nil {
		t.Fatalf("failed to create dummy sleep script: %v", err)
	}

	testCases := []struct {
		name              string
		mounterPath       string
		req               *mounter.MountRequest
		overrideTmpVolume bool
		expectedCode      codes.Code
		expectedError     string
		expectSuccess     bool
	}{
		{
			name:          "should return Internal error when MountRequest is nil",
			mounterPath:   "gcsfuse",
			req:           nil,
			expectedCode:  codes.Internal,
			expectedError: "mount request cannot be nil",
		},
		{
			name:        "should return Internal error when VolumeId is empty",
			mounterPath: "gcsfuse",
			req: &mounter.MountRequest{
				MountPoint: "/mnt/shared",
			},
			expectedCode:  codes.Internal,
			expectedError: "volume id cannot be empty",
		},
		{
			name:        "should return Internal error when MountPoint is empty",
			mounterPath: "gcsfuse",
			req: &mounter.MountRequest{
				VolumeId: "test-vol",
			},
			expectedCode:  codes.Internal,
			expectedError: "mount point cannot be empty",
		},
		{
			name:        "should return Internal error when prepareConfigFile fails due to invalid config flag syntax",
			mounterPath: "gcsfuse",
			req: &mounter.MountRequest{
				VolumeId:     "test-vol",
				MountPoint:   "/mnt/shared",
				MountOptions: []string{"logging:severity:nested:invalid=bad"},
			},
			expectedCode:  codes.Internal,
			expectedError: "failed to prepare config file",
		},
		{
			name:        "should return Internal error when prepareConfigFile fails writing to default unwritable tmp volume path",
			mounterPath: "gcsfuse",
			req: &mounter.MountRequest{
				VolumeId:   "test-vol",
				MountPoint: "/mnt/shared",
			},
			expectedCode:  codes.Internal,
			expectedError: "failed to prepare config file",
		},
		{
			name:              "should return Internal error when MountToNode fails",
			mounterPath:       "/nonexistent/binary/path",
			overrideTmpVolume: true,
			req: &mounter.MountRequest{
				VolumeId:   "test-vol",
				MountPoint: "/mnt/shared",
			},
			expectedCode:  codes.Internal,
			expectedError: "failed to start gcsfuse",
		},
		{
			name:              "should succeed when MountToNode succeeds",
			mounterPath:       dummySleepScript,
			overrideTmpVolume: true,
			req: &mounter.MountRequest{
				VolumeId:     "test-vol",
				MountPoint:   "/", // root "/" is always mounted
				MountOptions: []string{"disable-metrics-for-gke:true"},
			},
			expectSuccess: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			subCtx, cancel := context.WithCancel(ctx)
			ms := NewMounterServer(subCtx, New(tc.mounterPath))
			t.Cleanup(func() {
				cancel()
				ms.mounter.WaitGroup.Wait()
			})
			if tc.overrideTmpVolume {
				tmp := t.TempDir()
				ms.tmpDir = tmp
				ms.bufferDir = tmp
				ms.cacheDir = tmp
			}

			resp, err := ms.Mount(subCtx, tc.req)

			if tc.expectSuccess {
				if err != nil {
					t.Fatalf("Got unexpected error %v, expected success", err)
				}
				if resp == nil {
					t.Fatalf("Got nil MountResponse, expected non-nil response")
				}
				return
			}

			if err == nil {
				t.Fatalf("Got nil error, expected code %v", tc.expectedCode)
			}
			st, ok := status.FromError(err)
			if !ok || st.Code() != tc.expectedCode {
				t.Errorf("Got error code %v, expected %v", st.Code(), tc.expectedCode)
			}
			if !strings.Contains(err.Error(), tc.expectedError) {
				t.Errorf("Got error %v, expected containing substring %q", err, tc.expectedError)
			}
		})
	}
}
