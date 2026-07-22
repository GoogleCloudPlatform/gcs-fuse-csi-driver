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
	"context"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConvertLabelsStringToMap(t *testing.T) {
	t.Parallel()
	t.Run("parsing labels string into map", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name           string
			labels         string
			expectedOutput map[string]string
			expectedError  bool
		}{
			// Success test cases
			{
				name:           "should return empty map when labels string is empty",
				labels:         "",
				expectedOutput: map[string]string{},
				expectedError:  false,
			},
			{
				name:   "single label string",
				labels: "key=value",
				expectedOutput: map[string]string{
					"key": "value",
				},
				expectedError: false,
			},
			{
				name:   "multiple label string",
				labels: "key1=value1,key2=value2",
				expectedOutput: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				expectedError: false,
			},
			{
				name:   "multiple labels string with whitespaces gets trimmed",
				labels: "key1=value1, key2=value2",
				expectedOutput: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				expectedError: false,
			},
			// Failure test cases
			{
				name:           "malformed labels string (no keys and values)",
				labels:         ",,",
				expectedOutput: nil,
				expectedError:  true,
			},
			{
				name:           "malformed labels string (incorrect format)",
				labels:         "foo,bar",
				expectedOutput: nil,
				expectedError:  true,
			},
			{
				name:           "malformed labels string (missing key)",
				labels:         "key1=value1,=bar",
				expectedOutput: nil,
				expectedError:  true,
			},
			{
				name:           "malformed labels string (missing key and value)",
				labels:         "key1=value1,=bar,=",
				expectedOutput: nil,
				expectedError:  true,
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			output, err := ConvertLabelsStringToMap(tc.labels)
			if tc.expectedError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if err != nil {
				if !tc.expectedError {
					t.Errorf("Did not expect error but got: %v", err)
				}

				continue
			}

			if !reflect.DeepEqual(output, tc.expectedOutput) {
				t.Errorf("Got labels %v, but expected %v", output, tc.expectedOutput)
			}
		}
	})

	t.Run("checking google requirements", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name          string
			labels        string
			expectedError bool
		}{
			{
				name: "64 labels at most",
				labels: `k1=v,k2=v,k3=v,k4=v,k5=v,k6=v,k7=v,k8=v,k9=v,k10=v,k11=v,k12=v,k13=v,k14=v,k15=v,k16=v,k17=v,k18=v,k19=v,k20=v,
                         k21=v,k22=v,k23=v,k24=v,k25=v,k26=v,k27=v,k28=v,k29=v,k30=v,k31=v,k32=v,k33=v,k34=v,k35=v,k36=v,k37=v,k38=v,k39=v,k40=v,
                         k41=v,k42=v,k43=v,k44=v,k45=v,k46=v,k47=v,k48=v,k49=v,k50=v,k51=v,k52=v,k53=v,k54=v,k55=v,k56=v,k57=v,k58=v,k59=v,k60=v,
                         k61=v,k62=v,k63=v,k64=v,k65=v`,
				expectedError: true,
			},
			{
				name:          "label key must have atleast 1 char",
				labels:        "=v",
				expectedError: true,
			},
			{
				name:          "label key can only contain lowercase chars, digits, _ and -)",
				labels:        "k*=v",
				expectedError: true,
			},
			{
				name:          "label key can only contain lowercase chars)",
				labels:        "K=v",
				expectedError: true,
			},
			{
				name:          "label key may not have over 63 characters",
				labels:        "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij1234=v",
				expectedError: true,
			},
			{
				name:          "label value can only contain lowercase chars, digits, _ and -)",
				labels:        "k1=###",
				expectedError: true,
			},
			{
				name:          "label value can only contain lowercase chars)",
				labels:        "k1=V",
				expectedError: true,
			},
			{
				name:          "label key cannot contain . and /",
				labels:        "kubernetes.io/created-for/pvc/namespace=v",
				expectedError: true,
			},
			{
				name:          "label value cannot contain . and /",
				labels:        "kubernetes_io_created-for_pvc_namespace=v./",
				expectedError: true,
			},
			{
				name:          "label value may not have over 63 chars",
				labels:        "v=abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij1234",
				expectedError: true,
			},
			{
				name:          "label key can have up to 63 chars",
				labels:        "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij123=v",
				expectedError: false,
			},
			{
				name:          "label value can have up to 63 chars",
				labels:        "k=abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij123",
				expectedError: false,
			},
			{
				name:          "label key can contain - and _",
				labels:        "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij-_=v",
				expectedError: false,
			},
			{
				name:          "label value can contain - and _",
				labels:        "k=abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij-_",
				expectedError: false,
			},
			{
				name:          "label value can have 0 chars",
				labels:        "kubernetes_io_created-for_pvc_namespace=",
				expectedError: false,
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			_, err := ConvertLabelsStringToMap(tc.labels)

			if tc.expectedError && err == nil {
				t.Errorf("Expected error but got none")
			}

			if !tc.expectedError && err != nil {
				t.Errorf("Did not expect error but got: %v", err)
			}
		}
	})
}

func TestParseEndpoint(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		endpoint        string
		expectedScheme  string
		expectedAddress string
		expectedError   bool
	}{
		{
			name:            "should parse unix endpoint correctly",
			endpoint:        "unix:/csi/csi.sock",
			expectedScheme:  "unix",
			expectedAddress: "/csi/csi.sock",
			expectedError:   false,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		scheme, address, err := ParseEndpoint(tc.endpoint, false)
		if tc.expectedError && err == nil {
			t.Errorf("Expected error but got none")
		}
		if err != nil {
			if !tc.expectedError {
				t.Errorf("Did not expect error but got: %v", err)
			}

			continue
		}

		if !reflect.DeepEqual(scheme, tc.expectedScheme) {
			t.Errorf("Got scheme %v, but expected %v", scheme, tc.expectedScheme)
		}

		if !reflect.DeepEqual(address, tc.expectedAddress) {
			t.Errorf("Got address %v, but expected %v", address, tc.expectedAddress)
		}
	}
}

func TestParsePodIDVolumeFromTargetpath(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name           string
		targetPath     string
		expectedPodID  string
		expectedVolume string
		expectedError  bool
	}{
		{
			name:           "should parse Pod ID correctly",
			targetPath:     "/var/lib/kubelet/pods/d2013878-3d56-45f9-89ec-0826612c89b6/volumes/kubernetes.io~csi/test-volume/mount",
			expectedPodID:  "d2013878-3d56-45f9-89ec-0826612c89b6",
			expectedVolume: "test-volume",
			expectedError:  false,
		},
		{
			name:           "should return error",
			targetPath:     "/foo/bar/volumes",
			expectedPodID:  "",
			expectedVolume: "",
			expectedError:  true,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		podID, volume, err := ParsePodIDVolumeFromTargetpath(tc.targetPath)
		if tc.expectedError && err == nil {
			t.Errorf("Expected error but got none")
		}
		if err != nil {
			if !tc.expectedError {
				t.Errorf("Did not expect error but got: %v", err)
			}

			continue
		}

		if !reflect.DeepEqual(podID, tc.expectedPodID) {
			t.Errorf("Got pod ID %v, but expected %v", podID, tc.expectedPodID)
		}
		if !reflect.DeepEqual(volume, tc.expectedVolume) {
			t.Errorf("Got volume %v, but expected %v", volume, tc.expectedVolume)
		}
	}
}

func TestPrepareEmptyDir(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                     string
		targetPath               string
		expectedEmptyDirBasePath string
		expectedError            bool
	}{
		{
			name:                     "should return emptyDir path correctly",
			targetPath:               "/var/lib/kubelet/pods/d2013878-3d56-45f9-89ec-0826612c89b6/volumes/kubernetes.io~csi/test-volume/mount",
			expectedEmptyDirBasePath: fmt.Sprintf("/var/lib/kubelet/pods/d2013878-3d56-45f9-89ec-0826612c89b6/volumes/kubernetes.io~empty-dir/%v/.volumes/test-volume", SidecarContainerTmpVolumeName),
			expectedError:            false,
		},
		{
			name:                     "should return error",
			targetPath:               "/foo/bar/volumes",
			expectedEmptyDirBasePath: "",
			expectedError:            true,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		emptyDirBasePath, err := PrepareEmptyDir(tc.targetPath, false)
		if tc.expectedError && err == nil {
			t.Errorf("Expected error but got none")
		}
		if err != nil {
			if !tc.expectedError {
				t.Errorf("Did not expect error but got: %v", err)
			}

			continue
		}

		if !reflect.DeepEqual(emptyDirBasePath, tc.expectedEmptyDirBasePath) {
			t.Errorf("Got emptyDirBasePath %v, but expected %v", emptyDirBasePath, tc.expectedEmptyDirBasePath)
		}
	}
}

func TestGetSocketBasePath(t *testing.T) {
	fuseSocketDir := "/tmp/fuse-sockets"

	testCases := []struct {
		targetPath   string
		expectedBase string
		parseError   bool
	}{
		{
			targetPath:   "/var/lib/kubelet/pods/pod-xyz123/volumes/kubernetes.io~csi/pvc-abc456/mount",
			expectedBase: filepath.Join(fuseSocketDir, fmt.Sprintf("%x", sha1.Sum([]byte("pod-xyz123_pvc-abc456")))),
			parseError:   false,
		},
		{
			targetPath:   "/var/lib/kubelet/pods/pod-def789/volumes/kubernetes.io~csi/data-uvw012/mount",
			expectedBase: filepath.Join(fuseSocketDir, fmt.Sprintf("%x", sha1.Sum([]byte("pod-def789_data-uvw012")))),
			parseError:   false,
		},
		{
			targetPath:   "/invalid/path",
			expectedBase: "",
			parseError:   true,
		},
	}

	// Create the fuse socket directory for the test
	err := os.MkdirAll(fuseSocketDir, 0755)
	if err != nil {
		t.Fatalf("failed to create fuse socket directory: %v", err)
	}
	defer os.RemoveAll(fuseSocketDir) // Clean up after the test

	for _, tc := range testCases {
		actualBase := GetSocketBasePath(tc.targetPath, fuseSocketDir)

		if tc.parseError {
			podID, volumeName, err := ParsePodIDVolumeFromTargetpath(tc.targetPath)
			if err == nil {
				t.Errorf("GetSocketBasePath(%q, %q) expected ParsePodIDVolumeFromTargetpath to return an error, but got podID: %q, volumeName: %q", tc.targetPath, fuseSocketDir, podID, volumeName)
			}
		} else {
			if actualBase != tc.expectedBase {
				t.Errorf("GetSocketBasePath(%q, %q) = %q, expected %q", tc.targetPath, fuseSocketDir, actualBase, tc.expectedBase)
			}
		}
	}
}

func TestCheckAndDeleteStaleFile(t *testing.T) {

	t.Parallel()
	base, err := os.MkdirTemp("", "stale-file-test")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	defer os.RemoveAll(base)

	testCases := []struct {
		fileName      string
		expectExist   bool
		expectedError error
	}{
		{
			fileName:      "socket",
			expectExist:   false,
			expectedError: nil,
		},
		{
			fileName:      "token.sock",
			expectExist:   true,
			expectedError: nil,
		},
	}

	for _, tc := range testCases {
		filePath := filepath.Join(base, tc.fileName)
		if tc.expectExist {
			_, err := os.Create(filePath)
			if err != nil {
				t.Errorf("got error: %v", err)
			}
			defer os.Remove(filePath)
		}

		actualErr := CheckAndDeleteStaleFile(base, tc.fileName)
		if actualErr != tc.expectedError {
			t.Errorf("got error: %v but expected: %v", actualErr, tc.expectedError)
		}
	}
}

func TestRemoveBucketSuffixIfPresentAndReturnVolumeId(t *testing.T) {
	t.Parallel()
	t.Run("removing bucket suffix if present and returning volume id", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name          string
			bucketName    string
			expectedValue string
		}{
			{
				name:          "should return bucket name without suffix, no special character",
				bucketName:    "bucket-name:1234567890123456789",
				expectedValue: "bucket-name",
			},
			{
				name:          "should return bucket name without suffix, special character",
				bucketName:    "bucket-name:1234567890123456789@us-central1",
				expectedValue: "bucket-name",
			},
			{
				name:          "should return bucket name without suffix, no colon with special character",
				bucketName:    "bucket-name@us-central1",
				expectedValue: "bucket-name@us-central1",
			},
			{
				name:          "should return bucket name without suffix, two colons",
				bucketName:    "bucket-name:1234567890123456789:12",
				expectedValue: "bucket-name",
			},
			{
				name:          "should return bucket name without suffix, no special character and no colon",
				bucketName:    "bucket-name",
				expectedValue: "bucket-name",
			},
			{
				name:          "should return bucket name without suffix, no bucket name",
				bucketName:    ":xyz",
				expectedValue: "",
			},
		}

		for _, tc := range testCases {
			t.Logf("test case: %s", tc.name)
			actual := ParseVolumeID(tc.bucketName)
			if actual != tc.expectedValue {
				t.Errorf("Got value %v, but expected %v", actual, tc.expectedValue)
			}
		}
	})
}

func TestIsGKEIdentityProvider(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                          string
		identityProvider              string
		expectedIsGKEIdentityProvider bool
	}{
		{
			name:                          "should return true for prod GKE identity provider",
			identityProvider:              "https://container.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/my-cluster",
			expectedIsGKEIdentityProvider: true,
		},
		{
			name:                          "should return true for staging GKE identity provider",
			identityProvider:              "https://staging-container.sandbox.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/my-cluster",
			expectedIsGKEIdentityProvider: true,
		},
		{
			name:                          "should return true for staging2 GKE identity provider",
			identityProvider:              "https://staging2-container.sandbox.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/my-cluster",
			expectedIsGKEIdentityProvider: true,
		},
		{
			name:                          "should return true for test GKE identity provider",
			identityProvider:              "https://test-container.sandbox.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/my-cluster",
			expectedIsGKEIdentityProvider: true,
		},
		{
			name:                          "should return true for dev sandbox GKE identity provider",
			identityProvider:              "https://amacaskill-gke-sandbox-test-container.sandbox.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/my-cluster",
			expectedIsGKEIdentityProvider: true,
		},
		{
			name:                          "should return false for custom identityProvider",
			identityProvider:              "//iam.googleapis.com/projects/326181500027/locations/global/workloadIdentityPools/wi-pool-amacaskill-k8s-cluster-3/providers/wi-p-amacaskill-k8s-cluster-3",
			expectedIsGKEIdentityProvider: false,
		},
		{
			name:                          "should return false for missing https, identityProvider",
			identityProvider:              "//container.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/my-cluster",
			expectedIsGKEIdentityProvider: false,
		},
		{
			name:                          "should return false, extra fields after gke identity provider",
			identityProvider:              "https://container.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/my-cluster/extra",
			expectedIsGKEIdentityProvider: false,
		},
	}

	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		gotIsGKEIdentityProvider := IsGKEIdentityProvider(tc.identityProvider)
		if tc.expectedIsGKEIdentityProvider != gotIsGKEIdentityProvider {
			t.Errorf("got isGKEIdentityProvider = %t for identityProvider %q, but expected %t", gotIsGKEIdentityProvider, tc.identityProvider, tc.expectedIsGKEIdentityProvider)
		}
	}
}

func TestGetCloudProfilerServiceVersion(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                   string
		podName                string
		podUID                 string
		expectedServiceVersion string
	}{
		{
			name:                   "both empty",
			podName:                "",
			podUID:                 "",
			expectedServiceVersion: "",
		},
		{
			name:                   "short pod name and standard uid",
			podName:                "test-pod",
			podUID:                 "12345678-1234-1234-1234-123456789012",
			expectedServiceVersion: "test-pod_12345678-1234-1234-1234-123456789012",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetCloudProfilerServiceVersion(tc.podName, tc.podUID)
			if got != tc.expectedServiceVersion {
				t.Errorf("GetCloudProfilerServiceVersion(%q, %q) = %q (len %d), want %q (len %d)", tc.podName, tc.podUID, got, len(got), tc.expectedServiceVersion, len(tc.expectedServiceVersion))
			}
		})
	}
}

func TestCheckNotSymlink(t *testing.T) {
	t.Parallel()

	// Set up a temporary directory for the test files
	base, err := os.MkdirTemp("", "symlink-test")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	defer os.RemoveAll(base)

	// Create test files, directories, and symlinks
	normalFile := filepath.Join(base, "normal.txt")
	os.WriteFile(normalFile, []byte("test data"), 0644)

	normalDir := filepath.Join(base, "normal_dir")
	os.MkdirAll(normalDir, 0755)

	symlinkToFile := filepath.Join(base, "symlink_file")
	os.Symlink(normalFile, symlinkToFile)

	symlinkToDir := filepath.Join(base, "symlink_dir")
	os.Symlink(normalDir, symlinkToDir)

	nonExistentPath := filepath.Join(base, "does_not_exist")

	testCases := []struct {
		name          string
		path          string
		expectedError bool
	}{
		{
			name:          "regular file should pass",
			path:          normalFile,
			expectedError: false,
		},
		{
			name:          "regular directory should pass",
			path:          normalDir,
			expectedError: false,
		},
		{
			name:          "symlink to file should fail",
			path:          symlinkToFile,
			expectedError: true,
		},
		{
			name:          "symlink to directory should fail",
			path:          symlinkToDir,
			expectedError: true,
		},
		{
			name:          "non-existent path should fail",
			path:          nonExistentPath,
			expectedError: true, // os.Lstat returns an error for non-existent files
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckNotSymlink(tc.path)

			if tc.expectedError && err == nil {
				t.Errorf("Expected error but got none")
			}

			if !tc.expectedError && err != nil {
				t.Errorf("Did not expect error but got: %v", err)
			}
		})
	}
}

func TestIsDirMounted(t *testing.T) {
	tempDir := t.TempDir()
	fakeProcMounts := filepath.Join(tempDir, "mounts")
	mountedPath := "/path/to/mounted"
	content := fmt.Sprintf("device %s fuse.gcsfuse rw 0 0\n", mountedPath)
	if err := os.WriteFile(fakeProcMounts, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write fake proc mounts: %v", err)
	}

	oldProcMountsPath := procMountsPath
	procMountsPath = fakeProcMounts
	t.Cleanup(func() {
		procMountsPath = oldProcMountsPath
	})

	mounted, err := IsDirMounted(mountedPath)
	if err != nil {
		t.Fatalf("IsDirMounted failed: %v", err)
	}
	if !mounted {
		t.Errorf("expected path %q to be mounted", mountedPath)
	}

	notMounted, err := IsDirMounted("/path/to/not/mounted")
	if err != nil {
		t.Fatalf("IsDirMounted failed: %v", err)
	}
	if notMounted {
		t.Errorf("expected path not to be mounted")
	}
}

func TestWaitForPathMounted(t *testing.T) {
	mountedPath := "/path/to/mounted"
	mountContent := fmt.Sprintf("device %s fuse.gcsfuse rw 0 0\n", mountedPath)

	t.Run("already mounted", func(t *testing.T) {
		tempDir := t.TempDir()
		fakeProcMounts := filepath.Join(tempDir, "mounts")
		if err := os.WriteFile(fakeProcMounts, []byte(mountContent), 0644); err != nil {
			t.Fatalf("failed to write fake proc mounts: %v", err)
		}

		oldProcMountsPath := procMountsPath
		procMountsPath = fakeProcMounts
		t.Cleanup(func() {
			procMountsPath = oldProcMountsPath
		})

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		if err := WaitForPathMounted(ctx, mountedPath); err != nil {
			t.Fatalf("WaitForPathMounted failed: %v", err)
		}
	})

	t.Run("mounted after delay", func(t *testing.T) {
		tempDir := t.TempDir()
		fakeProcMounts := filepath.Join(tempDir, "mounts")
		if err := os.WriteFile(fakeProcMounts, []byte(""), 0644); err != nil {
			t.Fatalf("failed to write fake proc mounts: %v", err)
		}

		oldProcMountsPath := procMountsPath
		procMountsPath = fakeProcMounts
		t.Cleanup(func() {
			procMountsPath = oldProcMountsPath
		})

		timer := time.AfterFunc(500*time.Millisecond, func() {
			_ = os.WriteFile(fakeProcMounts, []byte(mountContent), 0644)
		})
		defer timer.Stop()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := WaitForPathMounted(ctx, mountedPath); err != nil {
			t.Fatalf("WaitForPathMounted failed: %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		tempDir := t.TempDir()
		fakeProcMounts := filepath.Join(tempDir, "mounts")
		if err := os.WriteFile(fakeProcMounts, []byte(""), 0644); err != nil {
			t.Fatalf("failed to write fake proc mounts: %v", err)
		}

		oldProcMountsPath := procMountsPath
		procMountsPath = fakeProcMounts
		t.Cleanup(func() {
			procMountsPath = oldProcMountsPath
		})

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		err := WaitForPathMounted(ctx, mountedPath)
		if err == nil {
			t.Fatal("expected error on timeout, got nil")
		}
		if st, ok := status.FromError(err); !ok || st.Code() != codes.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded error code, got %v", err)
		}
	})

	t.Run("context canceled", func(t *testing.T) {
		tempDir := t.TempDir()
		fakeProcMounts := filepath.Join(tempDir, "mounts")
		if err := os.WriteFile(fakeProcMounts, []byte(""), 0644); err != nil {
			t.Fatalf("failed to write fake proc mounts: %v", err)
		}

		oldProcMountsPath := procMountsPath
		procMountsPath = fakeProcMounts
		t.Cleanup(func() {
			procMountsPath = oldProcMountsPath
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := WaitForPathMounted(ctx, mountedPath)
		if err == nil {
			t.Fatal("expected error on canceled context, got nil")
		}
		if st, ok := status.FromError(err); !ok || st.Code() != codes.Canceled {
			t.Errorf("expected Canceled error code, got %v", err)
		}
	})
}
