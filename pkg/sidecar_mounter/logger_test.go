/*
Copyright 2024 The Kubernetes Authors.
Copyright 2024 Google LLC

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
	"os"
	"path/filepath"
	"testing"
)

func TestClean(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Setup for existing file
	existingFile := filepath.Join(tmpDir, "existing.log")
	if err := os.WriteFile(existingFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	testCases := []struct {
		name        string
		w           *stderrWriter
		expectError bool
		verifyFunc  func(t *testing.T)
	}{
		{
			name: "empty error file",
			w:    &stderrWriter{errorFile: ""},
		},
		{
			name: "existing file",
			w:    &stderrWriter{errorFile: existingFile},
			verifyFunc: func(t *testing.T) {
				if _, err := os.Stat(existingFile); !os.IsNotExist(err) {
					t.Errorf("expected file to be deleted")
				}
			},
		},
		{
			name: "non-existing file",
			w:    &stderrWriter{errorFile: filepath.Join(tmpDir, "nonexistent.log")},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.w.Clean()
			if (err != nil) != tc.expectError {
				t.Errorf("expected error: %v, got: %v", tc.expectError, err)
			}
			if tc.verifyFunc != nil {
				tc.verifyFunc(t)
			}
		})
	}
}

func TestWrite(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	msg := []byte("hello world")
	validFile := filepath.Join(tmpDir, "error.log")

	testCases := []struct {
		name         string
		w            *stderrWriter
		msg          []byte
		expectedN    int
		expectError  bool
		expectedFile string
	}{
		{
			name:      "empty error file",
			w:         &stderrWriter{errorFile: ""},
			msg:       msg,
			expectedN: len(msg),
		},
		{
			name:         "valid file",
			w:            &stderrWriter{errorFile: validFile},
			msg:          msg,
			expectedN:    len(msg),
			expectedFile: validFile,
		},
		{
			name:        "invalid file path",
			w:           &stderrWriter{errorFile: filepath.Join(tmpDir, "nonexistent_dir", "error.log")},
			msg:         msg,
			expectedN:   0,
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := tc.w.Write(tc.msg)
			if (err != nil) != tc.expectError {
				t.Errorf("expected error: %v, got: %v", tc.expectError, err)
			}
			if n != tc.expectedN {
				t.Errorf("expected n=%d, got %d", tc.expectedN, n)
			}
			if tc.expectedFile != "" {
				b, err := os.ReadFile(tc.expectedFile)
				if err != nil {
					t.Fatalf("failed to read file: %v", err)
				}
				if string(b) != string(tc.msg) {
					t.Errorf("expected file content %q, got %q", string(tc.msg), string(b))
				}
			}
		})
	}
}

func TestWriteMsg(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	errMsg := "test error message"
	validFile := filepath.Join(tmpDir, "error.log")

	testCases := []struct {
		name         string
		w            *stderrWriter
		msg          string
		expectedFile string
	}{
		{
			name:         "valid file",
			w:            &stderrWriter{errorFile: validFile},
			msg:          errMsg,
			expectedFile: validFile,
		},
		{
			name: "invalid file path",
			w:    &stderrWriter{errorFile: filepath.Join(tmpDir, "nonexistent_dir", "error.log")},
			msg:  errMsg,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.w.WriteMsg(tc.msg)

			if tc.expectedFile != "" {
				b, err := os.ReadFile(tc.expectedFile)
				if err != nil {
					t.Fatalf("failed to read file: %v", err)
				}
				if string(b) != tc.msg {
					t.Errorf("expected file content %q, got %q", tc.msg, string(b))
				}
			}
		})
	}
}
