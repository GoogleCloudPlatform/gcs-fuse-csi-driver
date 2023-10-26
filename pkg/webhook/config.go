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
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

type Config struct {
	ContainerImage                string
	ImagePullPolicy               string
	CPULimit                      resource.Quantity
	MemoryLimit                   resource.Quantity
	EphemeralStorageLimit         resource.Quantity
	TerminationGracePeriodSeconds int
}

func LoadConfig(containerImage, imagePullPolicy, cpuLimit, memoryLimit, ephemeralStorageLimit string) (*Config, error) {
	c, err := resource.ParseQuantity(cpuLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CPU limit %q: %w", cpuLimit, err)
	}
	m, err := resource.ParseQuantity(memoryLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to parse memory limit %q: %w", cpuLimit, err)
	}
	e, err := resource.ParseQuantity(ephemeralStorageLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ephemeral storage limit %q: %w", cpuLimit, err)
	}
	cfg := &Config{
		ContainerImage:                containerImage,
		ImagePullPolicy:               imagePullPolicy,
		CPULimit:                      c,
		MemoryLimit:                   m,
		EphemeralStorageLimit:         e,
		TerminationGracePeriodSeconds: 30,
	}

	return cfg, nil
}

func FakeConfig() *Config {
	c, _ := LoadConfig("fake-repo/fake-sidecar-image:v999.999.999-gke.0@sha256:c9cd4cde857ab8052f416609184e2900c0004838231ebf1c3817baa37f21d847", "Always", "100m", "30Mi", "5Gi")

	return c
}
