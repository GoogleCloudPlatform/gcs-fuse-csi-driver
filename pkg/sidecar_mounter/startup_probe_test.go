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
	"encoding/gob"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/util"
)

// encodeDaemonizeMessages encodes msgs the way daemonize.sendMsg does, as interface values holding pointers.
func encodeDaemonizeMessages(t *testing.T, msgs ...any) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	encoder := gob.NewEncoder(&buf)
	for _, msg := range msgs {
		if err := encoder.Encode(&msg); err != nil {
			t.Fatalf("failed to encode %v: %v", msg, err)
		}
	}

	return &buf
}

func TestReadMountOutcome(t *testing.T) {
	testCases := []struct {
		name      string
		input     *bytes.Buffer
		expectErr error
		errText   string
	}{
		{
			name:  "successful mount",
			input: encodeDaemonizeMessages(t, &outcomeMsg{Successful: true}),
		},
		{
			name:  "log messages before a successful mount",
			input: encodeDaemonizeMessages(t, &logMsg{Msg: []byte("Creating a mount at ...")}, &outcomeMsg{Successful: true}),
		},
		{
			name:    "failed mount",
			input:   encodeDaemonizeMessages(t, &outcomeMsg{ErrorMsg: "mountWithArgs: bucket not found"}),
			errText: "mountWithArgs: bucket not found",
		},
		{
			name:      "gcsfuse exits without reporting",
			input:     encodeDaemonizeMessages(t, &logMsg{Msg: []byte("Starting ...")}),
			expectErr: errNoMountOutcome,
		},
		{
			name:    "unexpected bytes",
			input:   bytes.NewBufferString("not a gob stream"),
			errText: "failed to decode",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
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
