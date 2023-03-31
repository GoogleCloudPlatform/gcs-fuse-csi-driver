/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package job

import (
	"context"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

// WaitForJobPodsRunning wait for all pods for the Job named JobName in namespace ns to become Running.  Only use
// when pods will run for a long time, or it will be racy.
func WaitForJobPodsRunning(c clientset.Interface, ns, jobName string, expectedCount int32) error {
	return waitForJobPodsInPhase(c, ns, jobName, expectedCount, v1.PodRunning)
}

// WaitForJobPodsSucceeded wait for all pods for the Job named JobName in namespace ns to become Succeeded.
func WaitForJobPodsSucceeded(c clientset.Interface, ns, jobName string, expectedCount int32) error {
	return waitForJobPodsInPhase(c, ns, jobName, expectedCount, v1.PodSucceeded)
}

// waitForJobPodsInPhase wait for all pods for the Job named JobName in namespace ns to be in a given phase.
func waitForJobPodsInPhase(c clientset.Interface, ns, jobName string, expectedCount int32, phase v1.PodPhase) error {
	return wait.Poll(framework.Poll, JobTimeout, func() (bool, error) {
		pods, err := GetJobPods(c, ns, jobName)
		if err != nil {
			return false, err
		}
		count := int32(0)
		for _, p := range pods.Items {
			if p.Status.Phase == phase {
				count++
			}
		}
		return count == expectedCount, nil
	})
}

// WaitForJobComplete uses c to wait for completions to complete for the Job jobName in namespace ns.
func WaitForJobComplete(c clientset.Interface, ns, jobName string, completions int32) error {
	return wait.Poll(framework.Poll, JobTimeout, func() (bool, error) {
		curr, err := c.BatchV1().Jobs(ns).Get(context.TODO(), jobName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return curr.Status.Succeeded == completions, nil
	})
}

// WaitForJobFailed uses c to wait for the Job jobName in namespace ns to fail
func WaitForJobFailed(c clientset.Interface, ns, jobName string) error {
	return wait.PollImmediate(framework.Poll, JobTimeout, func() (bool, error) {
		curr, err := c.BatchV1().Jobs(ns).Get(context.TODO(), jobName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return isJobFailed(curr), nil
	})
}

func isJobFailed(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobFailed) && c.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// WaitForJobFinish uses c to wait for the Job jobName in namespace ns to finish (either Failed or Complete).
func WaitForJobFinish(c clientset.Interface, ns, jobName string) error {
	return wait.PollImmediate(framework.Poll, JobTimeout, func() (bool, error) {
		curr, err := c.BatchV1().Jobs(ns).Get(context.TODO(), jobName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return isJobFinished(curr), nil
	})
}

func isJobFinished(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == v1.ConditionTrue {
			return true
		}
	}

	return false
}

// WaitForJobGone uses c to wait for up to timeout for the Job named jobName in namespace ns to be removed.
func WaitForJobGone(c clientset.Interface, ns, jobName string, timeout time.Duration) error {
	return wait.Poll(framework.Poll, timeout, func() (bool, error) {
		_, err := c.BatchV1().Jobs(ns).Get(context.TODO(), jobName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

// WaitForAllJobPodsGone waits for all pods for the Job named jobName in namespace ns
// to be deleted.
func WaitForAllJobPodsGone(c clientset.Interface, ns, jobName string) error {
	return wait.PollImmediate(framework.Poll, JobTimeout, func() (bool, error) {
		pods, err := GetJobPods(c, ns, jobName)
		if err != nil {
			return false, err
		}
		return len(pods.Items) == 0, nil
	})
}
