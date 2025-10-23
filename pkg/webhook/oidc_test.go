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

package webhook

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseCredentialConfigurationConfigMap(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		configMapName       string
		configMapsInCluster []*corev1.ConfigMap
		expectError         bool
		expectedFilename    string
		expectedCredConfig  *CredentialConfig
	}{
		{
			name:          "valid credential configuration",
			configMapName: "test-credentials",
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
							"credential_source": {
								"file": "/var/run/service-account/token"
							}
						}`,
					},
				},
			},
			expectError:      false,
			expectedFilename: "credential-configuration.json",
			expectedCredConfig: &CredentialConfig{
				Audience: "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
				CredentialSource: struct {
					File string `json:"file"`
				}{
					File: "/var/run/service-account/token",
				},
			},
		},
		{
			name:                "configmap not found",
			configMapName:       "missing-credentials",
			configMapsInCluster: []*corev1.ConfigMap{},
			expectError:         true,
		},
		{
			name:          "empty configmap data",
			configMapName: "empty-credentials",
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "empty-credentials",
						Namespace: "default",
					},
					Data: map[string]string{},
				},
			},
			expectError: true,
		},
		{
			name:          "invalid json in configmap",
			configMapName: "invalid-credentials",
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "invalid-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{"invalid": json}`,
					},
				},
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewSimpleClientset()

			// Create the configmaps.
			for _, configMap := range tc.configMapsInCluster {
				_, err := fakeClient.CoreV1().ConfigMaps(configMap.Namespace).Create(context.Background(), configMap, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("failed to create configmap: %v", err)
				}
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
			}

			filename, credConfig, err := parseCredentialConfigurationConfigMap(fakeClient, pod, tc.configMapName)

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if filename != tc.expectedFilename {
				t.Errorf("expected filename %q, got %q", tc.expectedFilename, filename)
			}

			if diff := cmp.Diff(tc.expectedCredConfig, credConfig); diff != "" {
				t.Errorf("credential config mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAppendWorkloadCredentialConfigurationVolumes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		configMapName       string
		pod                 *corev1.Pod
		configMapsInCluster []*corev1.ConfigMap
		expectError         bool
		expectedFilename    string
		expectedVolumes     []corev1.Volume
	}{
		{
			name:          "successful volume injection",
			configMapName: "test-credentials",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{},
				},
			},
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
							"credential_source": {
								"file": "/var/run/service-account/token"
							}
						}`,
					},
				},
			},
			expectError:      false,
			expectedFilename: "credential-configuration.json",
			expectedVolumes: []corev1.Volume{
				{
					Name: SidecarContainerWITokenVolumeName,
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{
									ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
										Audience:          "https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
										ExpirationSeconds: &tokenExpirationSeconds,
										Path:              "token",
									},
								},
							},
							DefaultMode: &defaultMode,
						},
					},
				},
				{
					Name: SidecarContainerWICredentialConfigMapVolumeName,
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-credentials",
							},
							DefaultMode: &defaultMode,
						},
					},
				},
			},
		},
		{
			name:          "configmap volume already exists",
			configMapName: "test-credentials",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: SidecarContainerWICredentialConfigMapVolumeName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "existing-configmap",
									},
								},
							},
						},
					},
				},
			},
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
							"credential_source": {
								"file": "/var/run/service-account/token"
							}
						}`,
					},
				},
			},
			expectError:      false,
			expectedFilename: "credential-configuration.json",
			expectedVolumes: []corev1.Volume{
				{
					Name: SidecarContainerWICredentialConfigMapVolumeName,
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "existing-configmap",
							},
						},
					},
				},
				{
					Name: SidecarContainerWITokenVolumeName,
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{
									ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
										Audience:          "https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
										ExpirationSeconds: &tokenExpirationSeconds,
										Path:              "token",
									},
								},
							},
							DefaultMode: &defaultMode,
						},
					},
				},
			},
		},
		{
			name:                "configmap parsing fails",
			configMapName:       "missing-credentials",
			pod:                 &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"}},
			configMapsInCluster: []*corev1.ConfigMap{},
			expectError:         true,
		},
		{
			name:          "different token file path - custom directory",
			configMapName: "custom-path-credentials",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{},
				},
			},
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "custom-path-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
							"credential_source": {
								"file": "/custom/token/path/service-account-token"
							}
						}`,
					},
				},
			},
			expectError:      false,
			expectedFilename: "credential-configuration.json",
			expectedVolumes: []corev1.Volume{
				{
					Name: SidecarContainerWITokenVolumeName,
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{
									ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
										Audience:          "https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
										ExpirationSeconds: &tokenExpirationSeconds,
										Path:              "service-account-token",
									},
								},
							},
							DefaultMode: &defaultMode,
						},
					},
				},
				{
					Name: SidecarContainerWICredentialConfigMapVolumeName,
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "custom-path-credentials",
							},
							DefaultMode: &defaultMode,
						},
					},
				},
			},
		},
		{
			name:          "root level token file",
			configMapName: "root-level-credentials",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{},
				},
			},
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "root-level-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
							"credential_source": {
								"file": "/token"
							}
						}`,
					},
				},
			},
			expectError:      false,
			expectedFilename: "credential-configuration.json",
			expectedVolumes: []corev1.Volume{
				{
					Name: SidecarContainerWITokenVolumeName,
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{
									ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
										Audience:          "https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
										ExpirationSeconds: &tokenExpirationSeconds,
										Path:              "token",
									},
								},
							},
							DefaultMode: &defaultMode,
						},
					},
				},
				{
					Name: SidecarContainerWICredentialConfigMapVolumeName,
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "root-level-credentials",
							},
							DefaultMode: &defaultMode,
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewSimpleClientset()

			// Create the configmaps.
			for _, configMap := range tc.configMapsInCluster {
				_, err := fakeClient.CoreV1().ConfigMaps(configMap.Namespace).Create(context.Background(), configMap, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("failed to create configmap: %v", err)
				}
			}

			originalVolumeCount := len(tc.pod.Spec.Volumes)

			filename, credConfig, err := appendWorkloadCredentialConfigurationVolumes(fakeClient, tc.pod, tc.configMapName)

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if filename != tc.expectedFilename {
				t.Errorf("expected filename %q, got %q", tc.expectedFilename, filename)
			}

			if credConfig == nil {
				t.Errorf("expected credential config but got nil")
				return
			}

			// Check that volumes were added
			if len(tc.pod.Spec.Volumes) <= originalVolumeCount {
				t.Errorf("expected volumes to be added, original count: %d, new count: %d", originalVolumeCount, len(tc.pod.Spec.Volumes))
			}

			// Verify the expected volumes are present
			if diff := cmp.Diff(tc.expectedVolumes, tc.pod.Spec.Volumes); diff != "" {
				t.Errorf("volumes mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSidecarContainerCredentialConfiguration(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		credConfig     *SidecarContainerCredentialConfiguration
		config         *Config
		expectedEnvVar *corev1.EnvVar
		expectedMounts []corev1.VolumeMount
	}{
		{
			name: "valid credential configuration",
			credConfig: &SidecarContainerCredentialConfiguration{
				GacEnv: &corev1.EnvVar{
					Name:  "GOOGLE_APPLICATION_CREDENTIALS",
					Value: "/etc/workload-identity/credential-configuration.json",
				},
				CredentialVolumeMounts: []corev1.VolumeMount{
					{
						Name:      SidecarContainerWITokenVolumeName,
						MountPath: "/var/run/service-account",
					},
					{
						Name:      SidecarContainerWICredentialConfigMapVolumeName,
						MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath,
					},
				},
			},
			config: FakeConfig(),
			expectedEnvVar: &corev1.EnvVar{
				Name:  "GOOGLE_APPLICATION_CREDENTIALS",
				Value: "/etc/workload-identity/credential-configuration.json",
			},
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      SidecarContainerWITokenVolumeName,
					MountPath: "/var/run/service-account",
				},
				{
					Name:      SidecarContainerWICredentialConfigMapVolumeName,
					MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath,
				},
			},
		},
		{
			name:           "nil credential configuration",
			credConfig:     nil,
			config:         FakeConfig(),
			expectedEnvVar: nil,
			expectedMounts: nil,
		},
		{
			name: "credential configuration with nil GAC env",
			credConfig: &SidecarContainerCredentialConfiguration{
				GacEnv: nil,
				CredentialVolumeMounts: []corev1.VolumeMount{
					{
						Name:      SidecarContainerWITokenVolumeName,
						MountPath: "/var/run/service-account",
					},
				},
			},
			config:         FakeConfig(),
			expectedEnvVar: nil,
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      SidecarContainerWITokenVolumeName,
					MountPath: "/var/run/service-account",
				},
			},
		},
		{
			name: "credential configuration with custom token path",
			credConfig: &SidecarContainerCredentialConfiguration{
				GacEnv: &corev1.EnvVar{
					Name:  "GOOGLE_APPLICATION_CREDENTIALS",
					Value: "/etc/workload-identity/credential-configuration.json",
				},
				CredentialVolumeMounts: []corev1.VolumeMount{
					{
						Name:      SidecarContainerWITokenVolumeName,
						MountPath: "/custom/token/path",
					},
					{
						Name:      SidecarContainerWICredentialConfigMapVolumeName,
						MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath,
					},
				},
			},
			config: FakeConfig(),
			expectedEnvVar: &corev1.EnvVar{
				Name:  "GOOGLE_APPLICATION_CREDENTIALS",
				Value: "/etc/workload-identity/credential-configuration.json",
			},
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      SidecarContainerWITokenVolumeName,
					MountPath: "/custom/token/path",
				},
				{
					Name:      SidecarContainerWICredentialConfigMapVolumeName,
					MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath,
				},
			},
		},
		{
			name: "credential configuration with root-level token",
			credConfig: &SidecarContainerCredentialConfiguration{
				GacEnv: &corev1.EnvVar{
					Name:  "GOOGLE_APPLICATION_CREDENTIALS",
					Value: "/etc/workload-identity/credential-configuration.json",
				},
				CredentialVolumeMounts: []corev1.VolumeMount{
					{
						Name:      SidecarContainerWITokenVolumeName,
						MountPath: "/",
					},
					{
						Name:      SidecarContainerWICredentialConfigMapVolumeName,
						MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath,
					},
				},
			},
			config: FakeConfig(),
			expectedEnvVar: &corev1.EnvVar{
				Name:  "GOOGLE_APPLICATION_CREDENTIALS",
				Value: "/etc/workload-identity/credential-configuration.json",
			},
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      SidecarContainerWITokenVolumeName,
					MountPath: "/",
				},
				{
					Name:      SidecarContainerWICredentialConfigMapVolumeName,
					MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath,
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			container := GetNativeSidecarContainerSpec(tc.config, tc.credConfig)

			// Check environment variables
			var foundGacEnv *corev1.EnvVar
			for _, env := range container.Env {
				if env.Name == "GOOGLE_APPLICATION_CREDENTIALS" {
					foundGacEnv = &env
					break
				}
			}

			if tc.expectedEnvVar == nil {
				if foundGacEnv != nil {
					t.Errorf("expected no GOOGLE_APPLICATION_CREDENTIALS env var, but found: %v", foundGacEnv)
				}
			} else {
				if foundGacEnv == nil {
					t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS env var but found none")
				} else if diff := cmp.Diff(tc.expectedEnvVar, foundGacEnv); diff != "" {
					t.Errorf("GOOGLE_APPLICATION_CREDENTIALS env var mismatch (-want +got):\n%s", diff)
				}
			}

			// Check volume mounts - find credential-related mounts
			var credentialMounts []corev1.VolumeMount
			for _, mount := range container.VolumeMounts {
				if mount.Name == SidecarContainerWITokenVolumeName || mount.Name == SidecarContainerWICredentialConfigMapVolumeName {
					credentialMounts = append(credentialMounts, mount)
				}
			}

			// Handle nil case for comparison
			var expectedMounts []corev1.VolumeMount
			if tc.expectedMounts != nil {
				expectedMounts = tc.expectedMounts
			}

			if diff := cmp.Diff(expectedMounts, credentialMounts); diff != "" {
				t.Errorf("credential volume mounts mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWorkloadIdentityIntegration(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		configMapName       string
		configMapsInCluster []*corev1.ConfigMap
		expectError         bool
		expectedGacValue    string
		expectedTokenPath   string
	}{
		{
			name:          "full integration test",
			configMapName: "workload-identity-credentials",
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "workload-identity-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/123456/locations/global/workloadIdentityPools/my-pool/providers/my-provider",
							"credential_source": {
								"file": "/var/run/service-account/token"
							}
						}`,
					},
				},
			},
			expectError:       false,
			expectedGacValue:  fmt.Sprintf("%s/%s", SidecarContainerWICredentialConfigMapVolumeMountPath, "credential-configuration.json"),
			expectedTokenPath: filepath.Dir("/var/run/service-account/token"),
		},
		{
			name:          "integration test with custom token path",
			configMapName: "custom-path-workload-identity-credentials",
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "custom-path-workload-identity-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/789/locations/global/workloadIdentityPools/custom-pool/providers/custom-provider",
							"credential_source": {
								"file": "/opt/workload-identity/tokens/jwt-token"
							}
						}`,
					},
				},
			},
			expectError:       false,
			expectedGacValue:  fmt.Sprintf("%s/%s", SidecarContainerWICredentialConfigMapVolumeMountPath, "credential-configuration.json"),
			expectedTokenPath: filepath.Dir("/opt/workload-identity/tokens/jwt-token"),
		},
		{
			name:          "integration test with deeply nested token path",
			configMapName: "nested-path-workload-identity-credentials",
			configMapsInCluster: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "nested-path-workload-identity-credentials",
						Namespace: "default",
					},
					Data: map[string]string{
						"credential-configuration.json": `{
							"audience": "//iam.googleapis.com/projects/999/locations/global/workloadIdentityPools/nested-pool/providers/nested-provider",
							"credential_source": {
								"file": "/very/deeply/nested/directory/structure/for/token/file/service-account.token"
							}
						}`,
					},
				},
			},
			expectError:       false,
			expectedGacValue:  fmt.Sprintf("%s/%s", SidecarContainerWICredentialConfigMapVolumeMountPath, "credential-configuration.json"),
			expectedTokenPath: filepath.Dir("/very/deeply/nested/directory/structure/for/token/file/service-account.token"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewSimpleClientset()

			// Create the configmaps.
			for _, configMap := range tc.configMapsInCluster {
				_, err := fakeClient.CoreV1().ConfigMaps(configMap.Namespace).Create(context.Background(), configMap, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("failed to create configmap: %v", err)
				}
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{},
				},
			}

			// Step 1: Parse and inject volumes
			filename, credConfig, err := appendWorkloadCredentialConfigurationVolumes(fakeClient, pod, tc.configMapName)

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Step 2: Create sidecar credential configuration
			sidecarCredentialConfig := &SidecarContainerCredentialConfiguration{
				GacEnv: &corev1.EnvVar{
					Name:  "GOOGLE_APPLICATION_CREDENTIALS",
					Value: fmt.Sprintf("%s/%s", SidecarContainerWICredentialConfigMapVolumeMountPath, filename),
				},
				CredentialVolumeMounts: []corev1.VolumeMount{
					{Name: SidecarContainerWITokenVolumeName, MountPath: filepath.Dir(credConfig.CredentialSource.File)},
					{Name: SidecarContainerWICredentialConfigMapVolumeName, MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath},
				},
			}

			// Step 3: Create sidecar container with credentials
			config := FakeConfig()
			container := GetNativeSidecarContainerSpec(config, sidecarCredentialConfig)

			// Verify GOOGLE_APPLICATION_CREDENTIALS environment variable
			var foundGacEnv *corev1.EnvVar
			for _, env := range container.Env {
				if env.Name == "GOOGLE_APPLICATION_CREDENTIALS" {
					foundGacEnv = &env
					break
				}
			}

			if foundGacEnv == nil {
				t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS env var but found none")
			} else if foundGacEnv.Value != tc.expectedGacValue {
				t.Errorf("expected GAC value %q, got %q", tc.expectedGacValue, foundGacEnv.Value)
			}

			// Verify volume mounts
			var tokenMount, configMapMount *corev1.VolumeMount
			for _, mount := range container.VolumeMounts {
				if mount.Name == SidecarContainerWITokenVolumeName {
					tokenMount = &mount
				}
				if mount.Name == SidecarContainerWICredentialConfigMapVolumeName {
					configMapMount = &mount
				}
			}

			if tokenMount == nil {
				t.Errorf("expected token volume mount but found none")
			} else if tokenMount.MountPath != tc.expectedTokenPath {
				t.Errorf("expected token mount path %q, got %q", tc.expectedTokenPath, tokenMount.MountPath)
			}

			if configMapMount == nil {
				t.Errorf("expected configmap volume mount but found none")
			} else if configMapMount.MountPath != SidecarContainerWICredentialConfigMapVolumeMountPath {
				t.Errorf("expected configmap mount path %q, got %q", SidecarContainerWICredentialConfigMapVolumeMountPath, configMapMount.MountPath)
			}

			// Verify pod volumes were created
			if len(pod.Spec.Volumes) != 2 {
				t.Errorf("expected 2 volumes, got %d", len(pod.Spec.Volumes))
			}

			var tokenVolume, configMapVolume *corev1.Volume
			for _, volume := range pod.Spec.Volumes {
				if volume.Name == SidecarContainerWITokenVolumeName {
					tokenVolume = &volume
				}
				if volume.Name == SidecarContainerWICredentialConfigMapVolumeName {
					configMapVolume = &volume
				}
			}

			if tokenVolume == nil {
				t.Errorf("expected token volume but found none")
			}

			if configMapVolume == nil {
				t.Errorf("expected configmap volume but found none")
			}
		})
	}
}

func TestCredentialSourceFilePathHandling(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name               string
		credentialFilePath string
		expectedDirectory  string
		expectedTokenPath  string
	}{
		{
			name:               "standard service account token path",
			credentialFilePath: "/var/run/service-account/token",
			expectedDirectory:  "/var/run/service-account",
			expectedTokenPath:  "token",
		},
		{
			name:               "custom token directory",
			credentialFilePath: "/opt/workload-identity/tokens/jwt-token",
			expectedDirectory:  "/opt/workload-identity/tokens",
			expectedTokenPath:  "jwt-token",
		},
		{
			name:               "deeply nested token path",
			credentialFilePath: "/very/deeply/nested/directory/structure/for/token/file/service-account.token",
			expectedDirectory:  "/very/deeply/nested/directory/structure/for/token/file",
			expectedTokenPath:  "service-account.token",
		},
		{
			name:               "root level token file",
			credentialFilePath: "/token",
			expectedDirectory:  "/",
			expectedTokenPath:  "token",
		},
		{
			name:               "single character token file",
			credentialFilePath: "/t",
			expectedDirectory:  "/",
			expectedTokenPath:  "t",
		},
		{
			name:               "token with extension",
			credentialFilePath: "/secrets/workload/identity.jwt",
			expectedDirectory:  "/secrets/workload",
			expectedTokenPath:  "identity.jwt",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewSimpleClientset()

			// Create a test configmap with the specific credential file path
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-credentials",
					Namespace: "default",
				},
				Data: map[string]string{
					"credential-configuration.json": fmt.Sprintf(`{
						"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/test-pool/providers/test-provider",
						"credential_source": {
							"file": "%s"
						}
					}`, tc.credentialFilePath),
				},
			}

			_, err := fakeClient.CoreV1().ConfigMaps("default").Create(context.Background(), configMap, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to create configmap: %v", err)
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{},
				},
			}

			// Test the volume injection with the custom file path
			filename, credConfig, err := appendWorkloadCredentialConfigurationVolumes(fakeClient, pod, "test-credentials")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Verify the directory extraction worked correctly
			expectedDir := filepath.Dir(tc.credentialFilePath)
			actualDir := filepath.Dir(credConfig.CredentialSource.File)

			if actualDir != expectedDir {
				t.Errorf("expected directory %q, got %q", expectedDir, actualDir)
			}

			if actualDir != tc.expectedDirectory {
				t.Errorf("expected directory %q, got %q", tc.expectedDirectory, actualDir)
			}

			// Verify the projected token configuration
			var tokenVolume *corev1.Volume
			for _, volume := range pod.Spec.Volumes {
				if volume.Name == SidecarContainerWITokenVolumeName {
					tokenVolume = &volume
					break
				}
			}

			if tokenVolume == nil {
				t.Errorf("expected token volume but found none")
				return
			}

			if tokenVolume.Projected == nil || len(tokenVolume.Projected.Sources) == 0 {
				t.Errorf("expected projected volume sources but found none")
				return
			}

			tokenProjection := tokenVolume.Projected.Sources[0].ServiceAccountToken
			if tokenProjection == nil {
				t.Errorf("expected service account token projection but found none")
				return
			}

			expectedTokenPath := filepath.Base(tc.credentialFilePath)
			if tokenProjection.Path != expectedTokenPath {
				t.Errorf("expected token path %q, got %q", expectedTokenPath, tokenProjection.Path)
			}

			if tokenProjection.Path != tc.expectedTokenPath {
				t.Errorf("expected token path %q, got %q", tc.expectedTokenPath, tokenProjection.Path)
			}

			// Test sidecar credential configuration creation
			sidecarCredentialConfig := &SidecarContainerCredentialConfiguration{
				GacEnv: &corev1.EnvVar{
					Name:  "GOOGLE_APPLICATION_CREDENTIALS",
					Value: fmt.Sprintf("%s/%s", SidecarContainerWICredentialConfigMapVolumeMountPath, filename),
				},
				CredentialVolumeMounts: []corev1.VolumeMount{
					{Name: SidecarContainerWITokenVolumeName, MountPath: tc.expectedDirectory},
					{Name: SidecarContainerWICredentialConfigMapVolumeName, MountPath: SidecarContainerWICredentialConfigMapVolumeMountPath},
				},
			}

			// Test sidecar container creation with custom mount path
			config := FakeConfig()
			container := GetNativeSidecarContainerSpec(config, sidecarCredentialConfig)

			// Verify the mount path in the container
			var tokenMount *corev1.VolumeMount
			for _, mount := range container.VolumeMounts {
				if mount.Name == SidecarContainerWITokenVolumeName {
					tokenMount = &mount
					break
				}
			}

			if tokenMount == nil {
				t.Errorf("expected token volume mount but found none")
			} else if tokenMount.MountPath != tc.expectedDirectory {
				t.Errorf("expected token mount path %q, got %q", tc.expectedDirectory, tokenMount.MountPath)
			}
		})
	}
}
