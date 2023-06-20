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

package utils

import (
	"fmt"
	"os/exec"
	"strconv"
)

func buildAndPushImage(pkgDir, registry string, buildGcsFuseFromSource bool) error {
	cmd := exec.Command("make", "-C", pkgDir, "build-image-and-push-multi-arch", fmt.Sprintf("REGISTRY=%s", registry), "BUILD_GCSFUSE_FROM_SOURCE="+strconv.FormatBool(buildGcsFuseFromSource))
	if err := runCommand("Pushing image to REGISTRY "+registry, cmd); err != nil {
		return fmt.Errorf("failed to run push image: %w", err)
	}

	return nil
}

// TODO(songjiaxun): Implement this function. This is used when useManagedDriver is true, and inProw is true.
func deleteImage() error {
	return nil
}
