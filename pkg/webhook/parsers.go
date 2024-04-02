package webhook

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/kubernetes/pkg/util/parsers"
)

var minimumSupportedVersion = version.MustParseGeneric("1.29.0")

// iterates the container list,
// if a container is named "gke-gcsfuse-sidecar",
// extract the container image and check if the image is valid,
// then removes this container from the container list.
func parseSidecarContainerImage(pod *corev1.Pod) (string, error) {
	var image string
	var index int
	for i, c := range pod.Spec.Containers {
		if c.Name == SidecarContainerName {
			image = c.Image
			index = i

			if _, _, _, err := parsers.ParseImageName(image); err != nil {
				return "", fmt.Errorf("could not parse input image: %q, error: %w", image, err)
			}
		}
	}

	if image != "" {
		copy(pod.Spec.Containers[index:], pod.Spec.Containers[index+1:])
		pod.Spec.Containers = pod.Spec.Containers[:len(pod.Spec.Containers)-1]
	}

	return image, nil
}
