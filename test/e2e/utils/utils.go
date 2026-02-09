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
	"os"
	"os/exec"
	"strings"

	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/klog/v2"
)

const MinGCSFuseKernelParamsVersion = "v3.7.0-gke.0"

var (
	MasterBranchName = "master"

	// Use release branch for corresponding gcsfuse version. This ensures we
	// can pick up test fixes without requiring a new gcsfuse release.
	gcsfuseReleaseBranchFormat = "v%v.%v.%v_release"
)

func EnsureVariable(v *string, set bool, msgOnError string) {
	if set && len(*v) == 0 {
		klog.Fatal(msgOnError)
	} else if !set && len(*v) != 0 {
		klog.Fatal(msgOnError)
	}
}

func isVariableSet(v string) bool {
	return len(v) != 0
}

func runCommand(action string, cmd *exec.Cmd) error {
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	klog.Infof("%s", action)
	klog.Infof("cmd env=%v", cmd.Env)
	klog.Infof("cmd path=%v", cmd.Path)
	klog.Infof("cmd args=%s", cmd.Args)

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Wait()
}

func GCSFuseBranch(gcsfuseVersionStr string) (*version.Version, string) {
	v, err := version.ParseSemantic(gcsfuseVersionStr)
	// When the gcsfuse binary is built using the head commit or has a pre-release tag that does not contain "-gke",
	// (e.g. v3.3.0-gke.1) it is a development build. Always use the master branch for these builds.
	if err != nil || (v.PreRelease() != "" && !strings.Contains(gcsfuseVersionStr, "-gke")) {
		return nil, MasterBranchName
	}

	return v, fmt.Sprintf(gcsfuseReleaseBranchFormat, v.Major(), v.Minor(), v.Patch())
}
