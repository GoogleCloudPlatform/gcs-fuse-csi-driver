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
	"time"

	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
	corev1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"

	iam "google.golang.org/api/iam/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	klog "k8s.io/klog/v2"
)

const (
	pollInterval     = 1 * time.Second
	pollTimeout      = 1 * time.Minute
	pollIntervalSlow = 10 * time.Second
	pollTimeoutSlow  = 20 * time.Minute

	backoffDuration = 5 * time.Second
	backoffFactor   = 2.0
	backoffCap      = 2 * time.Minute
	backoffSteps    = 8
	backoffJitter   = 0.1

	driverNamespace      = "gcs-fuse-csi-driver"
	driverContainer      = "gcs-fuse-csi-driver"
	driverDaemonsetLabel = "k8s-app=gcs-fuse-csi-driver"

	K8sServiceAccountName = "gcsfuse-csi-sa"
)

type TestKubernetesServiceAccount struct {
	client         clientset.Interface
	serviceAccount *corev1.ServiceAccount
	namespace      *corev1.Namespace
}

func NewTestKubernetesServiceAccount(c clientset.Interface, ns *corev1.Namespace, name, gcpSAEmail string) *TestKubernetesServiceAccount {
	sa := &TestKubernetesServiceAccount{
		client:    c,
		namespace: ns,
		serviceAccount: &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
	if gcpSAEmail != "" {
		sa.serviceAccount.Annotations = map[string]string{
			"iam.gke.io/gcp-service-account": gcpSAEmail,
		}
	}

	return sa
}

func (t *TestKubernetesServiceAccount) Create(ctx context.Context) {
	klog.Infof("Creating Kubernetes Service Account %s", t.serviceAccount.Name)
	var err error
	t.serviceAccount, err = t.client.CoreV1().ServiceAccounts(t.namespace.Name).Create(ctx, t.serviceAccount, metav1.CreateOptions{})
	ExpectNoError(err)
}

func (t *TestKubernetesServiceAccount) Cleanup(ctx context.Context) {
	klog.Infof("Deleting Kubernetes Service Account %s", t.serviceAccount.Name)
	err := t.client.CoreV1().ServiceAccounts(t.namespace.Name).Delete(ctx, t.serviceAccount.Name, metav1.DeleteOptions{})
	ExpectNoError(err)
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

func (t *TestGCPServiceAccount) Create(ctx context.Context) {
	klog.Infof("Creating GCP IAM Service Account %s", t.serviceAccount.Name)
	iamService, err := iam.NewService(ctx)
	ExpectNoError(err)

	request := &iam.CreateServiceAccountRequest{
		AccountId: t.serviceAccount.Name,
		ServiceAccount: &iam.ServiceAccount{
			DisplayName: "Cloud Storage FUSE CSI Driver E2E Test SA",
		},
	}
	t.serviceAccount, err = iamService.Projects.ServiceAccounts.Create("projects/"+t.serviceAccount.ProjectId, request).Do()
	ExpectNoError(err)

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(context.Context) (bool, error) {
		if _, e := iamService.Projects.ServiceAccounts.Get(t.serviceAccount.Name).Do(); e != nil {
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
	ExpectNoError(err)
}

func (t *TestGCPServiceAccount) AddIAMPolicyBinding(ctx context.Context, ns *corev1.Namespace) {
	klog.Infof("Binding the GCP IAM Service Account %s with Role roles/iam.workloadIdentityUser", t.serviceAccount.Name)
	iamService, err := iam.NewService(ctx)
	ExpectNoError(err)

	policy, err := iamService.Projects.ServiceAccounts.GetIamPolicy(t.serviceAccount.Name).Do()
	ExpectNoError(err)

	policy.Bindings = append(policy.Bindings,
		&iam.Binding{
			Role: "roles/iam.workloadIdentityUser",
			Members: []string{
				fmt.Sprintf("serviceAccount:%v.svc.id.goog[%v/%v]", t.serviceAccount.ProjectId, ns.Name, K8sServiceAccountName),
			},
		})

	iamPolicyRequest := &iam.SetIamPolicyRequest{Policy: policy}
	_, err = iamService.Projects.ServiceAccounts.SetIamPolicy(t.serviceAccount.Name, iamPolicyRequest).Do()
	ExpectNoError(err)
}

func (t *TestGCPServiceAccount) GetEmail() string {
	if t.serviceAccount != nil {
		return t.serviceAccount.Email
	}

	return ""
}

func (t *TestGCPServiceAccount) Cleanup(ctx context.Context) {
	klog.Infof("Deleting GCP IAM Service Account %s", t.serviceAccount.Name)
	service, err := iam.NewService(ctx)
	ExpectNoError(err)
	_, err = service.Projects.ServiceAccounts.Delete(t.serviceAccount.Name).Do()
	ExpectNoError(err)
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

func (t *TestGCPProjectIAMPolicyBinding) Create(ctx context.Context) {
	klog.Infof("Binding member %s with role %s, condition %q to project %s", t.member, t.role, t.condition, t.projectID)
	crmService, err := cloudresourcemanager.NewService(ctx)
	ExpectNoError(err)

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeoutSlow, true, func(context.Context) (bool, error) {
		if addBinding(crmService, t.projectID, t.member, t.role) != nil {
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
	ExpectNoError(err)

}

func (t *TestGCPProjectIAMPolicyBinding) Cleanup(ctx context.Context) {
	klog.Infof("Removing member %q from project %v", t.member, t.projectID)
	crmService, err := cloudresourcemanager.NewService(ctx)
	ExpectNoError(err)

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeoutSlow, true, func(context.Context) (bool, error) {
		if removeMember(crmService, t.projectID, t.member, t.role) != nil {
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
	ExpectNoError(err)
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
	klog.Infof("Removing member %q with role %q from project %q", member, role, projectID)
	policy, err := getPolicy(crmService, projectID)
	if err != nil {
		return err
	}
	klog.Infof("Current policy: %+v", policy)

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
	if binding == nil {
		klog.Warning("Attempted to delete a member from a binding that does not exist %s in project %s", role, projectID)
		return nil
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

// addComputeBindingForAC binds the Workload Identity principal to the monitoring.viewer role on the target project, this is needed to perform ac requests.
// projectID: The project receiving the binding (${GCS_PROJECT}), we are assuming its the same as the ID of the project hosting the identity pool.
// identityProjectNumber: The number of the project hosting the identity pool (${PROJECT_NUMBER})
func addComputeBindingForAC() (func(), error) {
	fmt.Println("Adding monitoring.viewer role to the Workload Identity principal for AC requests")
	projectID := os.Getenv(ProjectEnvVar)
	if projectID == "" {
		return nil, fmt.Errorf("environment variable %s is not set", ProjectEnvVar)
	}
	projectNumber := os.Getenv(ProjectNumberEnvVar)
	if projectNumber == "" {
		return nil, fmt.Errorf("environment variable %s is not set", ProjectNumberEnvVar)
	}

	member := fmt.Sprintf(
		"principal://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s.svc.id.goog/subject/ns/gcs-fuse-csi-driver/sa/gcs-fuse-csi-controller-sa",
		projectNumber,
		projectID,
	)
	bindingHelper := NewTestGCPProjectIAMPolicyBinding(projectID, member, "roles/compute.viewer", "")
	bindingHelper.Create(context.Background())

	// Define the cleanup logic inside a closure
	cleanupFunc := func() {
		// Use a fresh context for cleanup to ensure it isn't cancelled prematurely
		ctx := context.Background()
		bindingHelper.Cleanup(ctx)
	}

	return cleanupFunc, nil
}

// ExpectNoError fails immediately if err != nil, this is being used instead of framework because we want
// to avoid framework setting the global 'v'.
func ExpectNoError(err error, msgAndArgs ...interface{}) {
	if err != nil {
		var msg string
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprintf(msgAndArgs[0].(string), msgAndArgs[1:]...)
		} else {
			msg = "unexpected error"
		}
		panic(fmt.Sprintf("%s: %v", msg, err))
	}
}
