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
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"k8s.io/klog/v2"
)

const (
	Mb = 1024 * 1024

	TrueStr  = "true"
	FalseStr = "false"

	// mount options that both CSI mounter and sidecar mounter should understand.
	EnableMetricsForGKE = "enable-metrics-for-gke"
)

var (
	targetPathRegexp       = regexp.MustCompile(`/var/lib/kubelet/pods/(.*)/volumes/kubernetes\.io~csi/(.*)/mount`)
	emptyReplacementRegexp = regexp.MustCompile(`kubernetes\.io~csi/(.*)/mount`)
)

// ConvertLabelsStringToMap converts the labels from string to map
// example: "key1=value1,key2=value2" gets converted into {"key1": "value1", "key2": "value2"}
func ConvertLabelsStringToMap(labels string) (map[string]string, error) {
	const labelsDelimiter = ","
	const labelsKeyValueDelimiter = "="

	labelsMap := make(map[string]string)
	if labels == "" {
		return labelsMap, nil
	}

	// Following rules enforced for label keys
	// 1. Keys have a minimum length of 1 character and a maximum length of 63 characters, and cannot be empty.
	// 2. Keys and values can contain only lowercase letters, numeric characters, underscores, and dashes.
	// 3. Keys must start with a lowercase letter.
	regexKey := regexp.MustCompile(`^\p{Ll}[\p{Ll}0-9_-]{0,62}$`)
	checkLabelKeyFn := func(key string) error {
		if !regexKey.MatchString(key) {
			return fmt.Errorf("label value %q is invalid (should start with lowercase letter / lowercase letter, digit, _ and - chars are allowed / 1-63 characters", key)
		}

		return nil
	}

	// Values can be empty, and have a maximum length of 63 characters.
	regexValue := regexp.MustCompile(`^[\p{Ll}0-9_-]{0,63}$`)
	checkLabelValueFn := func(value string) error {
		if !regexValue.MatchString(value) {
			return fmt.Errorf("label value %q is invalid (lowercase letter, digit, _ and - chars are allowed / 0-63 characters", value)
		}

		return nil
	}

	keyValueStrings := strings.Split(labels, labelsDelimiter)
	for _, keyValue := range keyValueStrings {
		keyValue := strings.Split(keyValue, labelsKeyValueDelimiter)

		if len(keyValue) != 2 {
			return nil, fmt.Errorf("labels %q are invalid, correct format: 'key1=value1,key2=value2'", labels)
		}

		key := strings.TrimSpace(keyValue[0])
		if err := checkLabelKeyFn(key); err != nil {
			return nil, err
		}

		value := strings.TrimSpace(keyValue[1])
		if err := checkLabelValueFn(value); err != nil {
			return nil, err
		}

		labelsMap[key] = value
	}

	const maxNumberOfLabels = 64
	if len(labelsMap) > maxNumberOfLabels {
		return nil, fmt.Errorf("more than %d labels is not allowed, given: %d", maxNumberOfLabels, len(labelsMap))
	}

	return labelsMap, nil
}

func ParseEndpoint(endpoint string, cleanupSocket bool) (string, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		klog.Fatal(err.Error())
	}

	var addr string
	switch u.Scheme {
	case "unix":
		addr = u.Path
		if cleanupSocket {
			if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
				klog.Fatalf("Failed to remove %s, error: %s", addr, err)
			}
		}
	case "tcp":
		addr = u.Host
	default:
		klog.Fatalf("%v endpoint scheme not supported", u.Scheme)
	}

	return u.Scheme, addr, nil
}

func ParsePodIDVolumeFromTargetpath(targetPath string) (string, string, error) {
	matched := targetPathRegexp.FindStringSubmatch(targetPath)
	if len(matched) < 3 {
		return "", "", fmt.Errorf("targetPath %v does not contain Pod ID or volume information", targetPath)
	}
	podID := matched[1]
	volume := matched[2]

	return podID, volume, nil
}

func PrepareEmptyDir(targetPath string, createEmptyDir bool) (string, error) {
	_, _, err := ParsePodIDVolumeFromTargetpath(targetPath)
	if err != nil {
		return "", fmt.Errorf("failed to parse volume name from target path %q: %w", targetPath, err)
	}

	emptyDirBasePath := emptyReplacementRegexp.ReplaceAllString(targetPath, fmt.Sprintf("kubernetes.io~empty-dir/%v/.volumes/$1", webhook.SidecarContainerTmpVolumeName))

	if createEmptyDir {
		if err := os.MkdirAll(emptyDirBasePath, 0o750); err != nil {
			return "", fmt.Errorf("mkdir failed for path %q: %w", emptyDirBasePath, err)
		}
	}

	return emptyDirBasePath, nil
}
