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

	iamadmin "cloud.google.com/go/iam/admin/apiv1"
	adminpb "cloud.google.com/go/iam/admin/apiv1/adminpb"
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

	K8sServiceAccountName                 = "gcsfuse-csi-sa"
	GCSFuseCSIProfilesStaticBucketProject = "gke-scalability-images"
)

var (
	ProjectContainingMonitoringRoles = []string{GCSFuseCSIProfilesStaticBucketProject}

	// EnvRobots maps enviroments to the Kubernetes Service Agents.
	EnvRobots = map[string]string{
		"prod":     "container-engine-robot",
		"staging":  "container-engine-robot-staging",
		"staging2": "container-engine-robot-stag2",
		"test":     "container-engine-robot-test",
	}
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
		bindErr := addBinding(crmService, t.projectID, t.member, t.role)
		if bindErr != nil {
			klog.Warningf("error occured while binding member %s with role %s, condition %q to project %s: %v", t.member, t.role, t.condition, t.projectID, bindErr)
			//nolint:nilerr
			return false, nil
		}

		return true, nil
	})
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

// addComputeBindingForAC binds the Workload Identity principal to the compute.viewer role on the target project, this is needed to perform ac requests.
// projectID: The project receiving the binding (${GCS_PROJECT}), we are assuming its the same as the ID of the project hosting the identity pool.
// identityProjectNumber: The number of the project hosting the identity pool (${PROJECT_NUMBER})
func AddComputeBindingForAC(ctx context.Context) error {
	if os.Getenv(IsOSSEnvVar) != "true" {
		// If not using OSS, no need to add the binding as the p4sa should have the access needed.
		return nil
	}
	projectID := os.Getenv(ProjectEnvVar)
	if projectID == "" {
		return fmt.Errorf("environment variable %s is not set", ProjectEnvVar)
	}
	projectNumber := os.Getenv(ProjectNumberEnvVar)
	if projectNumber == "" {
		return fmt.Errorf("environment variable %s is not set", ProjectNumberEnvVar)
	}

	member, err := determineDriverSA(projectID, projectNumber)
	if err != nil {
		return fmt.Errorf("failed to determine driver service account: %v", err)
	}
	bindingHelper := NewTestGCPProjectIAMPolicyBinding(projectID, member, "roles/compute.viewer", "")
	bindingHelper.Create(ctx)
	return nil
}

// AddMonitoringBindingForBucketProject binds the Workload Identity principal to the monitoring.viewer role on the target project hosting an external bucket.
// If the tests are running on the managed driver environment, it binds the p4sa instead.
func AddMonitoringBindingForBucketProject(projIDForExternalBucket string, ctx context.Context) error {
	projectID := os.Getenv(ProjectEnvVar)
	if projectID == "" {
		return fmt.Errorf("environment variable %s is not set", ProjectEnvVar)
	}
	projectNumber := os.Getenv(ProjectNumberEnvVar)
	if projectNumber == "" {
		return fmt.Errorf("environment variable %s is not set", ProjectNumberEnvVar)
	}
	member, err := determineDriverSA(projectID, projectNumber)
	if err != nil {
		return fmt.Errorf("failed to determine controller service account: %v", err)
	}

	bindingHelper := NewTestGCPProjectIAMPolicyBinding(projIDForExternalBucket, member, "roles/monitoring.viewer", "")
	bindingHelper.Create(ctx)
	return nil
}

func determineDriverSA(projectID string, projectNumber string) (string, error) {
	member := fmt.Sprintf(
		"principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s.svc.id.goog/namespace/gcs-fuse-csi-driver",
		projectNumber,
		projectID,
	)
	if os.Getenv(IsOSSEnvVar) != "true" {
		testEnv := os.Getenv(TestEnvEnvVar)
		if testEnv == "" {
			return member, fmt.Errorf("failed to read test environment variable")
		}
		// Retrieve the Kubernetes service account that maps to the current env.
		robotAccount := EnvRobots[testEnv]
		member = fmt.Sprintf("serviceAccount:service-%s@%s.iam.gserviceaccount.com", projectNumber, robotAccount)
	}

	return member, nil
}

// RemoveMonitoringBindingForAllProjects removes the monitoring.viewer role binding from all projects that might have it.
// This includes the current project and the projects associated with buckets existing outside the scope of a single test run.
// Should be used for managed tests cleanup only as the p4sa is project wide and ephemeral unlike the sa created during non managed runs.
func RemoveMonitoringBindingForAllProjects(ctx context.Context) error {
	projectID := os.Getenv(ProjectEnvVar)
	if projectID == "" {
		return fmt.Errorf("environment variable %s is not set", ProjectEnvVar)
	}
	projectNumber := os.Getenv(ProjectNumberEnvVar)
	if projectNumber == "" {
		return fmt.Errorf("environment variable %s is not set", ProjectNumberEnvVar)
	}

	member, err := determineDriverSA(projectID, projectNumber)
	if err != nil {
		return fmt.Errorf("failed to determine driver service account: %v", err)
	}

	for _, proj := range ProjectContainingMonitoringRoles {
		bindingHelper := NewTestGCPProjectIAMPolicyBinding(proj, member, "roles/monitoring.viewer", "")
		bindingHelper.Cleanup(ctx)
	}
	return nil
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

func ValidateIAMRoleExists(ctx context.Context, projectID string, roleID string) error {
	klog.Info("Validating role exists: %s", roleID)
	iamClient, err := iamadmin.NewIamClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create IAM client: %v", err)
	}
	defer iamClient.Close()

	// The Name field must follow the pattern: projects/{project_id}/roles/{role_id}
	roleName := fmt.Sprintf("projects/%s/roles/%s", projectID, roleID)

	getRoleReq := &adminpb.GetRoleRequest{
		Name: roleName,
	}

	_, err = iamClient.GetRole(ctx, getRoleReq)
	if err != nil {
		return fmt.Errorf("error retrieving role: %v", err)
	}
	return nil
}

func deleteIAMRoleForProfilesTests(ctx context.Context, projectID string, roleID string) {
	klog.Info("Deleting role: %s", roleID)
	iamClient, err := iamadmin.NewIamClient(ctx)
	if err != nil {
		klog.Errorf("failed to create IAM client: %v", err)
	}
	defer iamClient.Close()

	roleName := fmt.Sprintf("projects/%s/roles/%s", projectID, roleID)

	deleteRoleReq := &adminpb.DeleteRoleRequest{
		Name: roleName,
	}

	// Capture both the deleted role object and the error
	deletedRole, err := iamClient.DeleteRole(ctx, deleteRoleReq)
	if err != nil {
		klog.Errorf("failed to delete IAM role %s: %v", roleName, err)
	}

	klog.Infof("Successfully deleted role: %s", deletedRole.Name)
}

func getDriverWIDPrincipal(projectID string, projectNumber string) string {
	return fmt.Sprintf(
		"principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s.svc.id.goog/namespace/gcs-fuse-csi-driver",
		projectNumber,
		projectID,
	)
}
