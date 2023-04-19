/*
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

package specs

import (
	"context"
	"fmt"
	"time"

	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
	iam "google.golang.org/api/iam/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/pkg/kubelet/events"
	"k8s.io/kubernetes/test/e2e/framework"
	e2edeployment "k8s.io/kubernetes/test/e2e/framework/deployment"
	e2eevents "k8s.io/kubernetes/test/e2e/framework/events"
	e2ejob "k8s.io/kubernetes/test/e2e/framework/job"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"
	"k8s.io/utils/pointer"
)

const (
	TesterContainerName   = "volume-tester"
	K8sServiceAccountName = "gcsfuse-csi-sa"
	//nolint:gosec
	K8sSecretName                   = "gcsfuse-csi-test-secret"
	FakeVolumePrefix                = "gcsfuse-csi-fake-volume"
	NonRootVolumePrefix             = "gcsfuse-csi-non-root-volume"
	InvalidMountOptionsVolumePrefix = "gcsfuse-csi-invalid-mount-options-volume"
	ImplicitDirsVolumePrefix        = "gcsfuse-csi-implicit-dirs-volume"
	ForceNewBucketPrefix            = "gcsfuse-csi-force-new-bucket"
	SameBucketDifferentDirPrefix    = "gcsfuse-csi-same-bucket-different-dir"
	MultipleBucketsPrefix           = "gcsfuse-csi-multiple-buckets"
	ImplicitDirsPath                = "implicit-dir"

	pollInterval    = 1 * time.Second
	pollTimeout     = 1 * time.Minute
	pollTimeoutSlow = 10 * time.Minute
)

type TestPod struct {
	client    clientset.Interface
	pod       *v1.Pod
	namespace *v1.Namespace
}

func NewTestPod(c clientset.Interface, ns *v1.Namespace) *TestPod {
	cpu, _ := resource.ParseQuantity("100m")
	mem, _ := resource.ParseQuantity("20Mi")

	return &TestPod{
		client:    c,
		namespace: ns,
		pod: &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "gcsfuse-volume-tester-",
				Annotations: map[string]string{
					"gke-gcsfuse/volumes": "true",
				},
			},
			Spec: v1.PodSpec{
				TerminationGracePeriodSeconds: pointer.Int64(5),
				NodeSelector:                  map[string]string{"kubernetes.io/os": "linux"},
				ServiceAccountName:            K8sServiceAccountName,
				Containers: []v1.Container{
					{
						Name:         TesterContainerName,
						Image:        imageutils.GetE2EImage(imageutils.BusyBox),
						Command:      []string{"/bin/sh"},
						Args:         []string{"-c", "tail -f /dev/null"},
						VolumeMounts: make([]v1.VolumeMount, 0),
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								v1.ResourceCPU:    cpu,
								v1.ResourceMemory: mem,
							},
							Requests: v1.ResourceList{
								v1.ResourceCPU:    cpu,
								v1.ResourceMemory: mem,
							},
						},
					},
				},
				RestartPolicy:                v1.RestartPolicyNever,
				Volumes:                      make([]v1.Volume, 0),
				AutomountServiceAccountToken: pointer.Bool(false),
				Tolerations: []v1.Toleration{
					{Operator: v1.TolerationOpExists},
				},
			},
		},
	}
}

func (t *TestPod) Create() {
	framework.Logf("Creating Pod %s", t.pod.Name)
	var err error
	t.pod, err = t.client.CoreV1().Pods(t.namespace.Name).Create(context.TODO(), t.pod, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

// VerifyExecInPodSucceed verifies shell cmd in target pod succeed.
func (t *TestPod) VerifyExecInPodSucceed(f *framework.Framework, containerName, shExec string) {
	stdout, stderr, err := e2epod.ExecCommandInContainerWithFullOutput(f, t.pod.Name, containerName, "/bin/sh", "-c", shExec)
	framework.ExpectNoError(err,
		"%q should succeed, but failed with error message %q\nstdout: %s\nstderr: %s",
		shExec, err, stdout, stderr)
}

// VerifyExecInPodFail verifies shell cmd in target pod fail with certain exit code.
func (t *TestPod) VerifyExecInPodFail(f *framework.Framework, containerName, shExec string, exitCode int) {
	stdout, stderr, err := e2epod.ExecCommandInContainerWithFullOutput(f, t.pod.Name, containerName, "/bin/sh", "-c", shExec)
	framework.ExpectError(err,
		"%q should fail with exit code %d, but exit without error\nstdout: %s\nstderr: %s",
		shExec, exitCode, stdout, stderr)
}

func (t *TestPod) WaitForRunning() {
	err := e2epod.WaitForPodRunningInNamespace(t.client, t.pod)
	framework.ExpectNoError(err)

	t.pod, err = t.client.CoreV1().Pods(t.namespace.Name).Get(context.TODO(), t.pod.Name, metav1.GetOptions{})
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForUnschedulable() {
	err := e2epod.WaitForPodNameUnschedulableInNamespace(t.client, t.pod.Name, t.namespace.Name)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForFailedMountError(msg string) {
	err := e2eevents.WaitTimeoutForEvent(
		t.client,
		t.namespace.Name,
		fields.Set{"reason": events.FailedMountVolume}.AsSelector().String(),
		msg,
		pollTimeoutSlow)
	framework.ExpectNoError(err)
}

func (t *TestPod) SetupVolume(volumeResource *storageframework.VolumeResource, name, mountPath string, readOnly bool) {
	volumeMount := v1.VolumeMount{
		Name:      name,
		MountPath: mountPath,
		ReadOnly:  readOnly,
	}
	t.pod.Spec.Containers[0].VolumeMounts = append(t.pod.Spec.Containers[0].VolumeMounts, volumeMount)

	volume := v1.Volume{
		Name: name,
	}
	if volumeResource.Pvc != nil {
		volume.VolumeSource = v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				ClaimName: volumeResource.Pvc.Name,
			},
		}
	} else if volumeResource.VolSource != nil {
		volume.VolumeSource = *volumeResource.VolSource
	}

	t.pod.Spec.Volumes = append(t.pod.Spec.Volumes, volume)
}

func (t *TestPod) SetName(name string) {
	t.pod.Name = name
}

func (t *TestPod) GetNode() string {
	return t.pod.Spec.NodeName
}

func (t *TestPod) SetNodeSelector(nodeName string, sameNode bool) {
	framework.ExpectNotEqual(nodeName, "")

	ns := &e2epod.NodeSelection{}
	if sameNode {
		e2epod.SetAffinity(ns, nodeName)
	} else {
		e2epod.SetAntiAffinity(ns, nodeName)
	}
	t.pod.Spec.Affinity = ns.Affinity
}

func (t *TestPod) SetAnnotations(annotations map[string]string) {
	t.pod.Annotations = annotations
}

func (t *TestPod) SetServiceAccount(sa string) {
	t.pod.Spec.ServiceAccountName = sa
}

func (t *TestPod) SetNonRootSecurityContext() {
	t.pod.Spec.SecurityContext = &v1.PodSecurityContext{
		RunAsUser: pointer.Int64(1001),
		FSGroup:   pointer.Int64(3003),
	}
}

func (t *TestPod) SetCommand(cmd string) {
	t.pod.Spec.Containers[0].Args = []string{"-c", cmd}
}

func (t *TestPod) SetPod(pod *v1.Pod) {
	t.pod = pod
}

func (t *TestPod) Cleanup() {
	e2epod.DeletePodOrFail(t.client, t.namespace.Name, t.pod.Name)
}

type TestSecret struct {
	client    clientset.Interface
	secret    *v1.Secret
	namespace *v1.Namespace
}

func NewTestSecret(c clientset.Interface, ns *v1.Namespace, name string, data map[string]string) *TestSecret {
	return &TestSecret{
		client:    c,
		namespace: ns,
		secret: &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			StringData: data,
			Type:       v1.SecretTypeOpaque,
		},
	}
}

func (t *TestSecret) Create() {
	framework.Logf("Creating Secret %s", t.secret.Name)
	var err error
	t.secret, err = t.client.CoreV1().Secrets(t.namespace.Name).Create(context.TODO(), t.secret, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestSecret) Cleanup() {
	framework.Logf("Deleting Secret %s", t.secret.Name)
	err := t.client.CoreV1().Secrets(t.namespace.Name).Delete(context.TODO(), t.secret.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

type TestKubernetesServiceAccount struct {
	client         clientset.Interface
	serviceAccount *v1.ServiceAccount
	namespace      *v1.Namespace
}

func NewTestKubernetesServiceAccount(c clientset.Interface, ns *v1.Namespace, name, gcpSAName string) *TestKubernetesServiceAccount {
	sa := &TestKubernetesServiceAccount{
		client:    c,
		namespace: ns,
		serviceAccount: &v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
	if gcpSAName != "" {
		sa.serviceAccount.Annotations = map[string]string{
			"iam.gke.io/gcp-service-account": gcpSAName,
		}
	}

	return sa
}

func (t *TestKubernetesServiceAccount) Create() {
	framework.Logf("Creating Kubernetes Service Account %s", t.serviceAccount.Name)
	var err error
	t.serviceAccount, err = t.client.CoreV1().ServiceAccounts(t.namespace.Name).Create(context.TODO(), t.serviceAccount, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestKubernetesServiceAccount) Cleanup() {
	framework.Logf("Deleting Kubernetes Service Account %s", t.serviceAccount.Name)
	err := t.client.CoreV1().ServiceAccounts(t.namespace.Name).Delete(context.TODO(), t.serviceAccount.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

type TestGCPServiceAccount struct {
	serviceAccount *iam.ServiceAccount
}

func NewTestGCPServiceAccount(name, projectID string) *TestGCPServiceAccount {
	return &TestGCPServiceAccount{
		serviceAccount: &iam.ServiceAccount{
			Name:      name,
			ProjectId: projectID,
		},
	}
}

func (t *TestGCPServiceAccount) Create() {
	framework.Logf("Creating GCP IAM Service Account %s", t.serviceAccount.Name)
	iamService, err := iam.NewService(context.TODO())
	framework.ExpectNoError(err)

	request := &iam.CreateServiceAccountRequest{
		AccountId: t.serviceAccount.Name,
		ServiceAccount: &iam.ServiceAccount{
			DisplayName: "Cloud Storage FUSE CSI Driver E2E Test SA " + t.serviceAccount.Name,
		},
	}
	t.serviceAccount, err = iamService.Projects.ServiceAccounts.Create("projects/"+t.serviceAccount.ProjectId, request).Do()
	framework.ExpectNoError(err)

	err = wait.PollImmediate(pollInterval, pollTimeout, func() (bool, error) {
		if _, e := iamService.Projects.ServiceAccounts.Get(t.serviceAccount.Name).Do(); e != nil {
			return false, e
		}

		return true, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestGCPServiceAccount) Cleanup() {
	framework.Logf("Deleting GCP IAM Service Account %s", t.serviceAccount.Name)
	service, err := iam.NewService(context.TODO())
	framework.ExpectNoError(err)
	_, err = service.Projects.ServiceAccounts.Delete(t.serviceAccount.Name).Do()
	framework.ExpectNoError(err)
}

type TestGCPServiceAccountIAMPolicyBinding struct {
	serviceAccount *iam.ServiceAccount
	namespace      *v1.Namespace
}

func NewTestGCPServiceAccountIAMPolicyBinding(ns *v1.Namespace, serviceAccount *iam.ServiceAccount) *TestGCPServiceAccountIAMPolicyBinding {
	return &TestGCPServiceAccountIAMPolicyBinding{
		namespace:      ns,
		serviceAccount: serviceAccount,
	}
}

func (t *TestGCPServiceAccountIAMPolicyBinding) Create() {
	framework.Logf("Binding the GCP IAM Service Account %s with Role roles/iam.workloadIdentityUser", t.serviceAccount.Name)
	iamService, err := iam.NewService(context.TODO())
	framework.ExpectNoError(err)

	iamPolicyRequest := &iam.SetIamPolicyRequest{
		Policy: &iam.Policy{
			Bindings: []*iam.Binding{
				{
					Role: "roles/iam.workloadIdentityUser",
					Members: []string{
						fmt.Sprintf("serviceAccount:%v.svc.id.goog[%v/%v]", t.serviceAccount.ProjectId, t.namespace.Name, K8sServiceAccountName),
					},
				},
			},
		},
	}
	_, err = iamService.Projects.ServiceAccounts.SetIamPolicy(t.serviceAccount.Name, iamPolicyRequest).Do()
	framework.ExpectNoError(err)
}

func (t *TestGCPServiceAccountIAMPolicyBinding) Cleanup() {
	framework.Logf("Removing Workload Identity User role from GCP IAM Service Account %s", t.serviceAccount.Name)
	iamService, err := iam.NewService(context.TODO())
	framework.ExpectNoError(err)
	_, err = iamService.Projects.ServiceAccounts.SetIamPolicy(t.serviceAccount.Name, &iam.SetIamPolicyRequest{}).Do()
	framework.ExpectNoError(err)
}

type TestGCPProjectIAMPolicyBinding struct {
	projectID string
	member    string
	role      string
	condition string
}

func NewTestGCPProjectIAMPolicyBinding(projectID, member, role, condition string) *TestGCPProjectIAMPolicyBinding {
	return &TestGCPProjectIAMPolicyBinding{
		projectID: projectID,
		member:    member,
		role:      role,
		condition: condition,
	}
}

func (t *TestGCPProjectIAMPolicyBinding) Create() {
	framework.Logf("Binding member %s with role %s, condition %q to project %s", t.member, t.role, t.condition, t.projectID)
	crmService, err := cloudresourcemanager.NewService(context.TODO())
	framework.ExpectNoError(err)

	err = wait.PollImmediate(pollInterval, pollTimeoutSlow, func() (bool, error) {
		if addBinding(crmService, t.projectID, t.member, t.role) != nil {
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestGCPProjectIAMPolicyBinding) Cleanup() {
	framework.Logf("Removing member %q from project %v", t.member, t.projectID)
	crmService, err := cloudresourcemanager.NewService(context.TODO())
	framework.ExpectNoError(err)

	err = wait.PollImmediate(pollInterval, pollTimeoutSlow, func() (bool, error) {
		if removeMember(crmService, t.projectID, t.member, t.role) != nil {
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
	framework.ExpectNoError(err)
}

// addBinding adds the member to the project's IAM policy.
func addBinding(crmService *cloudresourcemanager.Service, projectID, member, role string) error {
	policy, err := getPolicy(crmService, projectID)
	if err != nil {
		return err
	}

	// Find the policy binding for role. Only one binding can have the role.
	var binding *cloudresourcemanager.Binding
	for _, b := range policy.Bindings {
		if b.Role == role {
			binding = b

			break
		}
	}

	if binding != nil {
		// If the binding exists, adds the member to the binding
		binding.Members = append(binding.Members, member)
	} else {
		// If the binding does not exist, adds a new binding to the policy
		binding = &cloudresourcemanager.Binding{
			Role:    role,
			Members: []string{member},
		}
		policy.Bindings = append(policy.Bindings, binding)
	}

	return setPolicy(crmService, projectID, policy)
}

// removeMember removes the member from the project's IAM policy.
func removeMember(crmService *cloudresourcemanager.Service, projectID, member, role string) error {
	policy, err := getPolicy(crmService, projectID)
	if err != nil {
		return err
	}

	// Find the policy binding for role. Only one binding can have the role.
	var binding *cloudresourcemanager.Binding
	var bindingIndex int
	for i, b := range policy.Bindings {
		if b.Role == role {
			binding = b
			bindingIndex = i

			break
		}
	}

	// Order doesn't matter for bindings or members, so to remove, move the last item
	// into the removed spot and shrink the slice.
	if len(binding.Members) == 1 {
		// If the member is the only member in the binding, removes the binding
		last := len(policy.Bindings) - 1
		policy.Bindings[bindingIndex] = policy.Bindings[last]
		policy.Bindings = policy.Bindings[:last]
	} else {
		// If there is more than one member in the binding, removes the member
		var memberIndex int
		for i, mm := range binding.Members {
			if mm == member {
				memberIndex = i
			}
		}
		last := len(policy.Bindings[bindingIndex].Members) - 1
		binding.Members[memberIndex] = binding.Members[last]
		binding.Members = binding.Members[:last]
	}

	return setPolicy(crmService, projectID, policy)
}

// getPolicy gets the project's IAM policy.
func getPolicy(crmService *cloudresourcemanager.Service, projectID string) (*cloudresourcemanager.Policy, error) {
	request := new(cloudresourcemanager.GetIamPolicyRequest)

	return crmService.Projects.GetIamPolicy(projectID, request).Do()
}

// setPolicy sets the project's IAM policy.
func setPolicy(crmService *cloudresourcemanager.Service, projectID string, policy *cloudresourcemanager.Policy) error {
	request := new(cloudresourcemanager.SetIamPolicyRequest)
	request.Policy = policy
	_, err := crmService.Projects.SetIamPolicy(projectID, request).Do()

	return err
}

type TestDeployment struct {
	client     clientset.Interface
	deployment *appsv1.Deployment
	namespace  *v1.Namespace
}

func NewTestDeployment(c clientset.Interface, ns *v1.Namespace, tPod *TestPod) *TestDeployment {
	tPod.pod.Spec.RestartPolicy = v1.RestartPolicyAlways
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "gcsfuse-volume-deployment-tester-",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "gcsfuse-volume-deployment-tester",
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: tPod.pod.ObjectMeta,
				Spec:       tPod.pod.Spec,
			},
		},
	}
	deployment.Spec.Template.ObjectMeta.Labels = deployment.Spec.Selector.MatchLabels

	return &TestDeployment{
		client:     c,
		namespace:  ns,
		deployment: deployment,
	}
}

func (t *TestDeployment) Create() {
	framework.Logf("Creating Deployment %s", t.deployment.Name)
	var err error
	t.deployment, err = t.client.AppsV1().Deployments(t.namespace.Name).Create(context.TODO(), t.deployment, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestDeployment) WaitForComplete() {
	framework.Logf("Waiting Deployment %s to complete", t.deployment.Name)
	err := e2edeployment.WaitForDeploymentComplete(t.client, t.deployment)
	framework.ExpectNoError(err)
}

func (t *TestDeployment) GetPod() *v1.Pod {
	podList, err := e2edeployment.GetPodsForDeployment(t.client, t.deployment)
	framework.ExpectNoError(err)
	framework.ExpectEqual(len(podList.Items), 1)

	pod := podList.Items[0]

	return &pod
}

func (t *TestDeployment) Cleanup() {
	framework.Logf("Deleting Deployment %s", t.deployment.Name)
	err := t.client.AppsV1().Deployments(t.namespace.Name).Delete(context.TODO(), t.deployment.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

type TestJob struct {
	client    clientset.Interface
	job       *batchv1.Job
	namespace *v1.Namespace
}

func NewTestJob(c clientset.Interface, ns *v1.Namespace, tPod *TestPod) *TestJob {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gcsfuse-volume-job-tester",
		},
		Spec: batchv1.JobSpec{
			ManualSelector: pointer.Bool(true),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					e2ejob.JobSelectorKey: "gcsfuse-volume-job-tester",
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: tPod.pod.ObjectMeta,
				Spec:       tPod.pod.Spec,
			},
			BackoffLimit: pointer.Int32(1),
		},
	}
	job.Spec.Template.ObjectMeta.Labels = job.Spec.Selector.MatchLabels

	return &TestJob{
		client:    c,
		namespace: ns,
		job:       job,
	}
}

func (t *TestJob) Create() {
	framework.Logf("Creating Job %s", t.job.Name)
	var err error
	t.job, err = t.client.BatchV1().Jobs(t.namespace.Name).Create(context.TODO(), t.job, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestJob) WaitForJobPodsSucceeded() {
	framework.Logf("Waiting Job %s to have 1 succeeded pod", t.job.Name)
	err := e2ejob.WaitForJobPodsSucceeded(t.client, t.namespace.Name, t.job.Name, 1)
	framework.ExpectNoError(err)
}

func (t *TestJob) Cleanup() {
	framework.Logf("Deleting Job %s", t.job.Name)
	d := metav1.DeletePropagationBackground
	err := t.client.BatchV1().Jobs(t.namespace.Name).Delete(context.TODO(), t.job.Name, metav1.DeleteOptions{PropagationPolicy: &d})
	framework.ExpectNoError(err)
}
