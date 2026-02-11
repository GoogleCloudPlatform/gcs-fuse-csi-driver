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
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	DefaultNamespace              = "default"
	MinGCSFuseKernelParamsVersion = "v3.7.0-gke.0"
)

var (
	MasterBranchName = "master"

	// Use release branch for corresponding gcsfuse version. This ensures we
	// can pick up test fixes without requiring a new gcsfuse release.
	gcsfuseReleaseBranchFormat = "v%v.%v.%v_release"
	configMapBackoff           = wait.Backoff{
		Duration: 200 * time.Millisecond,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    5,
	}
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

func ReadConfigMap(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, name string,
) (map[string]string, error) {

	cm, err := client.CoreV1().
		ConfigMaps(namespace).
		Get(ctx, name, metav1.GetOptions{})

	if err != nil {
		return nil, err
	}

	return cm.Data, nil
}

func UpsertConfigMap(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, name string,
	data map[string]string,
) error {

	cmClient := client.CoreV1().ConfigMaps(namespace)

	// First try create
	_, err := cmClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: data,
	}, metav1.CreateOptions{})

	if err == nil {
		return nil
	}

	if !apierrors.IsAlreadyExists(err) {
		return err
	}

	// Exists → update with retry on conflict
	return wait.ExponentialBackoffWithContext(ctx, configMapBackoff, func(ctx context.Context) (bool, error) {
		existing, err := cmClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if existing.Data == nil {
			existing.Data = make(map[string]string)
		}
		for k, v := range data {
			existing.Data[k] = v
		}

		_, err = cmClient.Update(ctx, existing, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return false, nil
		}

		return true, err
	})
}
