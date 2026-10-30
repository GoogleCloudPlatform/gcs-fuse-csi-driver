/*
Copyright 2018 The Kubernetes Authors.
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
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
	"google.golang.org/grpc/codes"
)

// encodeStatusMessages encodes msgs the way gcsfuse mountstatus.ReportStatus does, one JSON object per line.
func encodeStatusMessages(t *testing.T, msgs ...statusPayload) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, msg := range msgs {
		if err := encoder.Encode(msg); err != nil {
			t.Fatalf("failed to encode %v: %v", msg, err)
		}
	}

	return &buf
}

func TestReadMountOutcome(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		input     *bytes.Buffer
		expectErr error
		errText   string
	}{
		{
			name:  "successful mount",
			input: encodeStatusMessages(t, statusPayload{Status: codes.OK, Error: "File system has been successfully mounted."}),
		},
		{
			name: "retries before a successful mount",
			input: encodeStatusMessages(t,
				statusPayload{Status: codes.PermissionDenied, Error: "googleapi: Error 403"},
				statusPayload{Status: codes.NotFound, Error: "bucket does not exist"},
				statusPayload{Status: codes.OK}),
		},
		{
			name:      "gcsfuse exits while retrying",
			input:     encodeStatusMessages(t, statusPayload{Status: codes.PermissionDenied, Error: "googleapi: Error 403"}),
			expectErr: errNoMountOutcome,
		},
		{
			name:      "gcsfuse exits without reporting",
			input:     &bytes.Buffer{},
			expectErr: errNoMountOutcome,
		},
		{
			name:    "unexpected bytes",
			input:   bytes.NewBufferString("not json"),
			errText: "failed to decode",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := readMountOutcome(tc.input)
			switch {
			case tc.expectErr != nil:
				if !errors.Is(err, tc.expectErr) {
					t.Fatalf("got error %v, want %v", err, tc.expectErr)
				}
			case tc.errText != "":
				if err == nil || !strings.Contains(err.Error(), tc.errText) {
					t.Fatalf("got error %v, want one containing %q", err, tc.errText)
				}
			case err != nil:
				t.Fatalf("got unexpected error %v", err)
			}
		})
	}
}

const deniedLine = "sidecar bucket access check error: failed to get GCS bucket \"b\": PermissionDenied"

// writeVolumes lays out a volume base path the way it looks once the sidecar has read the mount configs:
// one directory per volume without a socket, the flags-for-defaulting file, and whatever files the test case needs.
func writeVolumes(t *testing.T, volumes map[string]map[string]string) string {
	t.Helper()

	basePath := t.TempDir()
	if volumes == nil {
		return filepath.Join(basePath, ".volumes")
	}
	if err := os.WriteFile(filepath.Join(basePath, "flags-for-defaulting"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for volume, files := range volumes {
		dir := filepath.Join(basePath, volume)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	return basePath
}

func TestCheckStartupProbe(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		volumes map[string]map[string]string
		errText string
	}{
		{
			name:    "nonexistent volume base path",
			volumes: nil,
		},
		{
			name:    "no volumes",
			volumes: map[string]map[string]string{},
		},
		{
			name: "all volumes ready",
			volumes: map[string]map[string]string{
				"vol-a": {util.ReadyFileName: ""},
				"vol-b": {util.ReadyFileName: ""},
			},
		},
		{
			name: "one volume still mounting",
			volumes: map[string]map[string]string{
				"vol-a": {util.ReadyFileName: ""},
				"vol-b": {},
			},
			errText: `gcsfuse mount not ready: volume "vol-b": mount in progress`,
		},
		{
			name: "error file content is reported as is",
			volumes: map[string]map[string]string{
				"vol-a": {util.ErrorFileName: deniedLine + "\n"},
			},
			errText: `gcsfuse mount not ready: volume "vol-a": ` + deniedLine,
		},
		{
			name: "ready volume is not failed by its error file",
			volumes: map[string]map[string]string{
				"vol-a": {util.ReadyFileName: "", util.ErrorFileName: deniedLine},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckStartupProbe(writeVolumes(t, tc.volumes))
			if tc.errText == "" {
				if err != nil {
					t.Fatalf("got unexpected error %v", err)
				}

				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errText) {
				t.Fatalf("got error %v, want one containing %q", err, tc.errText)
			}
		})
	}
}

func TestRemoveStaleReadyFiles(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		volumes map[string]map[string]string
		// wantFiles lists the files left in the volume directories, as <volume>/<file>.
		wantFiles []string
	}{
		{
			name:    "nonexistent volume base path",
			volumes: nil,
		},
		{
			name: "no ready files",
			volumes: map[string]map[string]string{
				"vol-a": {util.ErrorFileName: deniedLine},
			},
			wantFiles: []string{filepath.Join("vol-a", util.ErrorFileName)},
		},
		{
			name: "ready files are removed and other files are kept",
			volumes: map[string]map[string]string{
				"vol-a": {util.ReadyFileName: "", util.ErrorFileName: deniedLine},
				"vol-b": {util.ReadyFileName: ""},
				"vol-c": {},
			},
			wantFiles: []string{filepath.Join("vol-a", util.ErrorFileName)},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			basePath := writeVolumes(t, tc.volumes)

			RemoveStaleReadyFiles(basePath)

			matches, err := filepath.Glob(filepath.Join(basePath, "*", "*"))
			if err != nil {
				t.Fatal(err)
			}
			var gotFiles []string
			for _, m := range matches {
				rel, err := filepath.Rel(basePath, m)
				if err != nil {
					t.Fatal(err)
				}
				gotFiles = append(gotFiles, rel)
			}
			if !reflect.DeepEqual(gotFiles, tc.wantFiles) {
				t.Errorf("expected remaining files %v, got %v", tc.wantFiles, gotFiles)
			}
		})
	}
}
