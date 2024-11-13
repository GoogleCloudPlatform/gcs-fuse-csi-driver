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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/kubernetes/pkg/util/parsers"
)

var minimumSupportedVersion = version.MustParseGeneric("1.29.0")

func ParseBool(str string) (bool, error) {
	switch str {
	case "True", "true":
		return true, nil
	case "False", "false":
		return false, nil
	default:
		return false, fmt.Errorf("could not parse string to bool: the acceptable values for %q are 'True', 'true', 'false' or 'False'", str)
	}
}

// ExtractImageAndDeleteContainer supports the injection of custom sidecar images.
// We iterate the container list and find a container named "containerName"
// If we find "containerName":
//   - extract the container image
//   - removes the container definition from the container list.
//   - verifies if the image is valid
//   - return image
//
// We support custom sidecar images because:
//   - Requirement for Privately Hosted Sidecar Image feature, for clusters running with limited internet access.
//   - Allow fast testing of new sidecar image on a production environment, usually related to a new gcsfuse binary.
func ExtractImageAndDeleteContainer(podSpec *corev1.PodSpec, containerName string) (string, error) {
	var image string

	// Find Container named containerName, extract its image, and remove from list.
	if index, present := containerPresent(podSpec.Containers, containerName); present {
		image = podSpec.Containers[index].Image

		// The next webhook step is to reinject the sidecar, removing user declaration to prevent dual injection creation failures.
		copy(podSpec.Containers[index:], podSpec.Containers[index+1:])
		podSpec.Containers = podSpec.Containers[:len(podSpec.Containers)-1]

		if _, _, _, err := parsers.ParseImageName(image); err != nil {
			return "", fmt.Errorf("could not parse input image: %q, error: %w", image, err)
		}
	}

	// Find initContainer named containerName, extract its image, and remove from list.
	if index, present := containerPresent(podSpec.InitContainers, containerName); present {
		image = podSpec.InitContainers[index].Image

		// The next webhook step is to reinject the sidecar, removing user declaration to prevent dual injection creation failures.
		copy(podSpec.InitContainers[index:], podSpec.InitContainers[index+1:])
		podSpec.InitContainers = podSpec.InitContainers[:len(podSpec.InitContainers)-1]

		if _, _, _, err := parsers.ParseImageName(image); err != nil {
			return "", fmt.Errorf("could not parse input image: %q, error: %w", image, err)
		}
	}

	return image, nil
}
