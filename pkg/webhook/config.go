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
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

type Config struct {
	HostNetwork            bool   `json:"-"`
	ContainerImage         string `json:"-"`
	MetadataContainerImage string `json:"-"`
	ImagePullPolicy        string `json:"-"`
	//nolint:tagliatelle
	CPURequest resource.Quantity `json:"gke-gcsfuse/cpu-request,omitempty"`
	//nolint:tagliatelle
	CPULimit resource.Quantity `json:"gke-gcsfuse/cpu-limit,omitempty"`
	//nolint:tagliatelle
	MemoryRequest resource.Quantity `json:"gke-gcsfuse/memory-request,omitempty"`
	//nolint:tagliatelle
	MemoryLimit resource.Quantity `json:"gke-gcsfuse/memory-limit,omitempty"`
	//nolint:tagliatelle
	EphemeralStorageRequest resource.Quantity `json:"gke-gcsfuse/ephemeral-storage-request,omitempty"`
	//nolint:tagliatelle
	EphemeralStorageLimit resource.Quantity `json:"gke-gcsfuse/ephemeral-storage-limit,omitempty"`
}

func LoadConfig(containerImage, metadataContainerImage, imagePullPolicy, cpuRequest, cpuLimit, memoryRequest, memoryLimit, ephemeralStorageRequest, ephemeralStorageLimit string) *Config {
	return &Config{
		ContainerImage:          containerImage,
		MetadataContainerImage:  metadataContainerImage,
		ImagePullPolicy:         imagePullPolicy,
		CPURequest:              resource.MustParse(cpuRequest),
		CPULimit:                resource.MustParse(cpuLimit),
		MemoryRequest:           resource.MustParse(memoryRequest),
		MemoryLimit:             resource.MustParse(memoryLimit),
		EphemeralStorageRequest: resource.MustParse(ephemeralStorageRequest),
		EphemeralStorageLimit:   resource.MustParse(ephemeralStorageLimit),
	}
}

func FakeConfig() *Config {
	fakeImage1 := "fake-repo/fake-sidecar-image:v999.999.999-gke.0@sha256:c9cd4cde857ab8052f416609184e2900c0004838231ebf1c3817baa37f21d847"
	fakeImage2 := "fake-repo/fake-sidecar-image:v888.888.888-gke.0@sha256:c9cd4cde857ab8052f416609184e2900c0004838231ebf1c3817baa37f21d847"

	return LoadConfig(fakeImage1, fakeImage2, "Always", "250m", "250m", "256Mi", "256Mi", "5Gi", "5Gi")
}

func prepareResourceList(c *Config) (corev1.ResourceList, corev1.ResourceList) {
	limitsResourceList := corev1.ResourceList{}
	requestsResourceList := corev1.ResourceList{}

	checkZeroQuantity := func(rl map[corev1.ResourceName]resource.Quantity, rn corev1.ResourceName, q resource.Quantity) {
		if !q.IsZero() {
			rl[rn] = q
		}
	}

	checkZeroQuantity(limitsResourceList, corev1.ResourceCPU, c.CPULimit)
	checkZeroQuantity(limitsResourceList, corev1.ResourceMemory, c.MemoryLimit)
	checkZeroQuantity(limitsResourceList, corev1.ResourceEphemeralStorage, c.EphemeralStorageLimit)
	checkZeroQuantity(requestsResourceList, corev1.ResourceCPU, c.CPURequest)
	checkZeroQuantity(requestsResourceList, corev1.ResourceMemory, c.MemoryRequest)
	checkZeroQuantity(requestsResourceList, corev1.ResourceEphemeralStorage, c.EphemeralStorageRequest)

	return limitsResourceList, requestsResourceList
}

// populateResource assigns request and limits based on the following conditions:
//  1. If both of the request and limit are unset, assign the default values.
//  2. If one of the request or limit is set and another is unset, enforce them to be set as the same.
//
// Note: when the annotation limit is zero and request is unset, we set request to use the default value.
func populateResource(requestQuantity, limitQuantity *resource.Quantity, defaultRequestQuantity, defaultLimitQuantity resource.Quantity) {
	// Use defaults when no annotations are set.
	if requestQuantity.Format == "" && limitQuantity.Format == "" {
		*requestQuantity = defaultRequestQuantity
		*limitQuantity = defaultLimitQuantity
	}

	// Set request to equal default when limit is zero/unlimited and request is unset.
	if limitQuantity.IsZero() && requestQuantity.Format == "" {
		*requestQuantity = defaultRequestQuantity
	}

	// Set request to equal limit when request annotation is not provided.
	if requestQuantity.Format == "" {
		*requestQuantity = *limitQuantity
	}

	// Set limit to equal request when limit annotation is not provided.
	if limitQuantity.Format == "" {
		*limitQuantity = *requestQuantity
	}
}

// prepareConfig overwrittes config values set by user input from pod annotations,
// remaining values that are not specified by user are kept as the default config values.
func (si *SidecarInjector) prepareConfig(annotations map[string]string) (*Config, error) {
	config := &Config{
		ContainerImage:         si.Config.ContainerImage,
		MetadataContainerImage: si.Config.MetadataContainerImage,
		ImagePullPolicy:        si.Config.ImagePullPolicy,
	}

	jsonData, err := json.Marshal(annotations)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pod annotations: %w", err)
	}

	if err := json.Unmarshal(jsonData, config); err != nil {
		return nil, fmt.Errorf("failed to parse sidecar container resource allocation from pod annotations: %w", err)
	}

	populateResource(&config.CPURequest, &config.CPULimit, si.Config.CPURequest, si.Config.CPULimit)
	populateResource(&config.MemoryRequest, &config.MemoryLimit, si.Config.MemoryRequest, si.Config.MemoryLimit)
	populateResource(&config.EphemeralStorageRequest, &config.EphemeralStorageLimit, si.Config.EphemeralStorageRequest, si.Config.EphemeralStorageLimit)

	return config, nil
}

func LogPodMutation(pod *corev1.Pod, sidecarConfig *Config) {
	klog.Infof("mutating Pod. Name: %q, GenerateName: %q, Namespace: %q, Sidecar Image: %s, CPU Request: %q, CPU limit: %q, Memory request: %q, Memory limit: %q, Ephemeral storage request: %q, Ephemeral storage limit: %q, Pull policy: %s",
		pod.Name,
		pod.GenerateName,
		pod.Namespace,
		sidecarConfig.ContainerImage,
		sidecarConfig.CPURequest.String(),
		sidecarConfig.CPULimit.String(),
		sidecarConfig.MemoryRequest.String(),
		sidecarConfig.MemoryLimit.String(),
		sidecarConfig.EphemeralStorageRequest.String(),
		sidecarConfig.EphemeralStorageLimit.String(),
		sidecarConfig.ImagePullPolicy,
	)
}
