/*
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
	ContainerImage        string
	ImageVersion          string
	ImagePullPolicy       string
	CPULimit              resource.Quantity
	MemoryLimit           resource.Quantity
	EphemeralStorageLimit resource.Quantity
}

func LoadConfig(containerImage, imageVersion, imagePullPolicy, cpuLimit, memoryLimit, ephemeralStorageLimit string) (*Config, error) {
	c, err := resource.ParseQuantity(cpuLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CPU limit %q: %v", cpuLimit, err)
	}
	m, err := resource.ParseQuantity(memoryLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to parse memory limit %q: %v", cpuLimit, err)
	}
	e, err := resource.ParseQuantity(ephemeralStorageLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ephemeral storage limit %q: %v", cpuLimit, err)
	}
	cfg := &Config{
		ContainerImage:        containerImage,
		ImageVersion:          imageVersion,
		ImagePullPolicy:       imagePullPolicy,
		CPULimit:              c,
		MemoryLimit:           m,
		EphemeralStorageLimit: e,
	}
	return cfg, nil
}

func FakeConfig() *Config {
	c, _ := LoadConfig("fake-sidecar-image", "latest", "Always", "100m", "30Mi", "5Gi")
	return c
}
