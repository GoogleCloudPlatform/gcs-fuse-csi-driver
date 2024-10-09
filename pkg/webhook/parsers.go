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

// parseSidecarContainerImage supports our Privately Hosted Sidecar Image option
// by iterating the container list and finding a container named "gke-gcsfuse-sidecar"
// If we find "gke-gcsfuse-sidecar":
//   - extract the container image and check if the image is valid
//   - removes the container definition from the container list.
//   - remove any mentions of "gke-gcsfuse-sidecar" from initContainer list.
//   - return image
func parseSidecarContainerImage(pod *corev1.Pod) (string, error) {
	var image string

	// Find container named "gke-gcsfuse-sidecar" (SidecarContainerName), extract its image, and remove from list.
	if index, present := containerPresent(pod.Spec.Containers, SidecarContainerName); present {
		image = pod.Spec.Containers[index].Image

		if _, _, _, err := parsers.ParseImageName(image); err != nil {
			return "", fmt.Errorf("could not parse input image: %q, error: %w", image, err)
		}

		if image != "" {
			copy(pod.Spec.Containers[index:], pod.Spec.Containers[index+1:])
			pod.Spec.Containers = pod.Spec.Containers[:len(pod.Spec.Containers)-1]
		}
	}

	// Remove any mention of gke-gcsfuse-sidecar from init container list.
	if index, present := containerPresent(pod.Spec.InitContainers, SidecarContainerName); present {
		copy(pod.Spec.InitContainers[index:], pod.Spec.InitContainers[index+1:])
		pod.Spec.InitContainers = pod.Spec.InitContainers[:len(pod.Spec.InitContainers)-1]
	}

	return image, nil
}
