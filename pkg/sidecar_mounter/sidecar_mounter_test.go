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
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"k8s.io/klog/v2"
)

// captureKlogOutput redirects klog output to a buffer for the duration of fn,
// then restores klog to logging to stderr.
func captureKlogOutput(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	klog.SetOutput(&buf)
	klog.LogToStderr(false)

	defer func() {
		klog.LogToStderr(true)
		klog.SetOutput(os.Stderr)
	}()

	fn()
	klog.Flush()

	return buf.String()
}

func hasErrorLine(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "E") {
			return true
		}
	}

	return false
}

func hasInfoLine(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "I") {
			return true
		}
	}

	return false
}

func TestLogVolumeTotalSize_MissingDir(t *testing.T) {
	tmpDir := t.TempDir()
	missingDir := filepath.Join(tmpDir, "does-not-exist")

	output := captureKlogOutput(t, func() {
		logVolumeTotalSize(missingDir)
	})

	if hasErrorLine(output) {
		t.Errorf("expected no Error-level klog line for a missing directory, got output: %q", output)
	}
}

func TestLogVolumeTotalSize_ExistingDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	output := captureKlogOutput(t, func() {
		logVolumeTotalSize(tmpDir)
	})

	if hasErrorLine(output) {
		t.Errorf("expected no Error-level klog line for an existing directory, got output: %q", output)
	}
	if !hasInfoLine(output) || !strings.Contains(output, "total volume size of") {
		t.Errorf("expected an Infof total-size log line for an existing directory, got output: %q", output)
	}
}

func TestLogVolumeTotalSize_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits behave differently on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, permission checks are bypassed")
	}

	tmpDir := t.TempDir()
	restrictedDir := filepath.Join(tmpDir, "restricted")
	if err := os.Mkdir(restrictedDir, 0o755); err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(restrictedDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	if err := os.Chmod(restrictedDir, 0o000); err != nil {
		t.Fatalf("failed to chmod test dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(restrictedDir, 0o755)
	})

	output := captureKlogOutput(t, func() {
		logVolumeTotalSize(restrictedDir)
	})

	if !hasErrorLine(output) {
		t.Errorf("expected an Error-level klog line for a permission-denied error, got output: %q", output)
	}
}
