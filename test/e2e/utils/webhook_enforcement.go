/*
Copyright 2018 The Kubernetes Authors.
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

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	DriverNamespace        = "gcs-fuse-csi-driver"
	webhookDeploymentName  = "gcs-fuse-csi-driver-webhook"
	requireWIFArgTrue      = "--require-wif-credential-configmap=true"
	requireWIFArgFalse     = "--require-wif-credential-configmap=false"
	requireAppCredsArgTrue = "--require-application-credentials=true"
	requireAppCredsArgFalse = "--require-application-credentials=false"
	rolloutPollInterval    = 5 * time.Second
	rolloutTimeout         = 5 * time.Minute
)

// GetWebhookWIFEnforcement reads both enforcement flags from the live webhook deployment.
// Returns true only when both --require-wif-credential-configmap and --require-application-credentials are true.
func GetWebhookWIFEnforcement(ctx context.Context, client kubernetes.Interface) bool {
	deployment, err := client.AppsV1().Deployments(DriverNamespace).Get(ctx, webhookDeploymentName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("failed to get webhook deployment %s/%s: %v", DriverNamespace, webhookDeploymentName, err)
		return false
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		hasWIF := false
		hasAppCreds := false
		for _, arg := range container.Args {
			if arg == requireWIFArgTrue {
				hasWIF = true
			}
			if arg == requireAppCredsArgTrue {
				hasAppCreds = true
			}
		}
		if hasWIF && hasAppCreds {
			return true
		}
	}
	return false
}

// SetWebhookWIFEnforcement sets both enforcement flags to the same value.
// Short-circuits if the deployment is already at the desired value to avoid a no-op patch
// that would not increment the deployment generation and cause waitForWebhookRollout to time out.
func SetWebhookWIFEnforcement(ctx context.Context, client kubernetes.Interface, enabled bool) error {
	current := GetWebhookWIFEnforcement(ctx, client)
	if current == enabled {
		klog.Infof("webhook WIF enforcement already at desired value %v, skipping patch", enabled)
		return nil
	}

	deployment, err := client.AppsV1().Deployments(DriverNamespace).Get(ctx, webhookDeploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get webhook deployment: %w", err)
	}

	for i, container := range deployment.Spec.Template.Spec.Containers {
		deployment.Spec.Template.Spec.Containers[i].Args = setOrReplaceEnforcementArgs(container.Args, enabled)
	}

	patch, err := buildArgPatch(deployment)
	if err != nil {
		return fmt.Errorf("failed to build arg patch: %w", err)
	}

	if _, err = client.AppsV1().Deployments(DriverNamespace).Patch(ctx, webhookDeploymentName, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("failed to patch webhook deployment: %w", err)
	}

	return waitForWebhookRollout(ctx, client)
}

// setOrReplaceEnforcementArgs strips existing values for both enforcement flags and appends new ones.
func setOrReplaceEnforcementArgs(args []string, enabled bool) []string {
	result := make([]string, 0, len(args)+2)
	for _, arg := range args {
		if strings.HasPrefix(arg, "--require-wif-credential-configmap=") ||
			strings.HasPrefix(arg, "--require-application-credentials=") {
			continue
		}
		result = append(result, arg)
	}
	if enabled {
		result = append(result, requireWIFArgTrue, requireAppCredsArgTrue)
	} else {
		result = append(result, requireWIFArgFalse, requireAppCredsArgFalse)
	}
	return result
}

// buildArgPatch builds a strategic merge patch JSON for the deployment's container args.
func buildArgPatch(deployment *appsv1.Deployment) ([]byte, error) {
	type containerPatch struct {
		Name string   `json:"name"`
		Args []string `json:"args"`
	}
	type specPatch struct {
		Containers []containerPatch `json:"containers"`
	}
	type templatePatch struct {
		Spec specPatch `json:"spec"`
	}
	type patchSpec struct {
		Template templatePatch `json:"template"`
	}
	type patchBody struct {
		Spec patchSpec `json:"spec"`
	}

	containers := make([]containerPatch, 0, len(deployment.Spec.Template.Spec.Containers))
	for _, c := range deployment.Spec.Template.Spec.Containers {
		containers = append(containers, containerPatch{Name: c.Name, Args: c.Args})
	}

	p := patchBody{
		Spec: patchSpec{
			Template: templatePatch{
				Spec: specPatch{Containers: containers},
			},
		},
	}
	return json.Marshal(p)
}

// waitForWebhookRollout polls every 5s with a 5-minute timeout until the webhook deployment
// has fully rolled out the new pod spec.
func waitForWebhookRollout(ctx context.Context, client kubernetes.Interface) error {
	return wait.PollUntilContextTimeout(ctx, rolloutPollInterval, rolloutTimeout, true,
		func(ctx context.Context) (bool, error) {
			deployment, err := client.AppsV1().Deployments(DriverNamespace).Get(ctx, webhookDeploymentName, metav1.GetOptions{})
			if err != nil {
				klog.Warningf("failed to get webhook deployment during rollout wait: %v — retrying", err)
				return false, nil
			}
			return isDeploymentRolledOut(deployment), nil
		},
	)
}

// isDeploymentRolledOut returns true when the deployment has finished rolling out its latest spec.
func isDeploymentRolledOut(d *appsv1.Deployment) bool {
	if d.Spec.Replicas == nil {
		return false
	}
	return d.Status.ObservedGeneration >= d.Generation &&
		d.Status.UpdatedReplicas == *d.Spec.Replicas &&
		d.Status.ReadyReplicas == *d.Spec.Replicas &&
		d.Status.AvailableReplicas == *d.Spec.Replicas
}
