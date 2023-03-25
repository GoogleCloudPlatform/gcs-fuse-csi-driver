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

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
)

// GetJob uses c to get the Job in namespace ns named name. If the returned error is nil, the returned Job is valid.
func GetJob(c clientset.Interface, ns, name string) (*batchv1.Job, error) {
	return c.BatchV1().Jobs(ns).Get(context.TODO(), name, metav1.GetOptions{})
}

// GetAllRunningJobPods returns a list of all running Pods belonging to a Job.
func GetAllRunningJobPods(c clientset.Interface, ns, jobName string) ([]v1.Pod, error) {
	if podList, err := GetJobPods(c, ns, jobName); err != nil {
		return nil, err
	} else {
		pods := []v1.Pod{}
		for _, pod := range podList.Items {
			if pod.Status.Phase == v1.PodRunning {
				pods = append(pods, pod)
			}
		}
		return pods, nil
	}
}

// GetJobPods returns a list of Pods belonging to a Job.
func GetJobPods(c clientset.Interface, ns, jobName string) (*v1.PodList, error) {
	label := labels.SelectorFromSet(labels.Set(map[string]string{JobSelectorKey: jobName}))
	options := metav1.ListOptions{LabelSelector: label.String()}
	return c.CoreV1().Pods(ns).List(context.TODO(), options)
}

// CreateJob uses c to create job in namespace ns. If the returned error is nil, the returned Job is valid and has
// been created.
func CreateJob(c clientset.Interface, ns string, job *batchv1.Job) (*batchv1.Job, error) {
	return c.BatchV1().Jobs(ns).Create(context.TODO(), job, metav1.CreateOptions{})
}

// CreateJob uses c to update a job in namespace ns. If the returned error is
// nil, the returned Job is valid and has been updated.
func UpdateJob(c clientset.Interface, ns string, job *batchv1.Job) (*batchv1.Job, error) {
	return c.BatchV1().Jobs(ns).Update(context.TODO(), job, metav1.UpdateOptions{})
}
