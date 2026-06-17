//go:build linux

/*
Copyright 2026 Google LLC

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

package driver

import (
	"errors"
	"fmt"
	"syscall"

	"k8s.io/klog/v2"
)

func lazyUnmount(targetPath string) error {
	klog.V(2).Infof("Attempting lazy unmount for target %q", targetPath)
	err := syscall.Unmount(targetPath, syscall.MNT_DETACH)
	if err != nil {
		// Treat the operation as successful for errors of type
		// EINVAL (target is already unmounted) or ENOENT (target does not exist).
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOENT) {
			klog.V(4).Infof("lazy unmount ignored error for target %q: %v", targetPath, err)
			return nil
		}
		return fmt.Errorf("syscall.Unmount failed: %w", err)
	}
	return nil
}
