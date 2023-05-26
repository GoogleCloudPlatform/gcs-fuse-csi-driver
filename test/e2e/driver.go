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

package main

import (
	"path/filepath"
)

func getOverlayDir(pkgDir, deployOverlayName string) string {
	return filepath.Join(pkgDir, "deploy", "kubernetes", "overlays", deployOverlayName)
}

// TODO(amacaskill): Implement this function. This is used when useManagedDriver is false, but doDriverBuild is true.
func installDriver(testParams *testParameters, stagingImage, deployOverlayName string, doDriverBuild bool) error {
	return nil
}

// TODO(amacaskill): Implement this function. This is used when useManagedDriver is false, but doDriverBuild is true.
func deleteDriver(testParams *testParameters, deployOverlayName string) error {
	return nil
}

// TODO(amacaskill): Implement this function. This is used when useManagedDriver is false, but doDriverBuild is true.
func pushImage(pkgDir, stagingImage, stagingVersion string) error {
	return nil
}

// TODO(amacaskill): Implement this function. This is used when useManagedDriver is false, but doDriverBuild is true.
func deleteImage(stagingImage, stagingVersion string) error {
	return nil
}
