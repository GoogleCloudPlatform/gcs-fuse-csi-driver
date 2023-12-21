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
	"k8s.io/apimachinery/pkg/api/resource"
)

type Config struct {
	ContainerImage  string `json:"-"`
	ImagePullPolicy string `json:"-"`
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

func LoadConfig(containerImage, imagePullPolicy, cpuRequest, cpuLimit, memoryRequest, memoryLimit, ephemeralStorageRequest, ephemeralStorageLimit string) *Config {
	return &Config{
		ContainerImage:          containerImage,
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
	return LoadConfig("fake-repo/fake-sidecar-image:v999.999.999-gke.0@sha256:c9cd4cde857ab8052f416609184e2900c0004838231ebf1c3817baa37f21d847", "Always", "250m", "250m", "256Mi", "256Mi", "5Gi", "5Gi")
}
