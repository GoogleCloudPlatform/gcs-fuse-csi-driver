/*
Copyright 2018 The Kubernetes Authors.
Copyright 2025 Google LLC

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

package testsuites

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"local/test/e2e/specs"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	oidcTestPrefix                 = "gcsfuse-csi-oidc"
	oidcWorkloadIdentityPoolID     = "gcs-fuse-oidc-pool"
	oidcWorkloadIdentityProviderID = "gcs-fuse-oidc-provider"
	oidcConfigMapName              = "workload-identity-credentials"
	oidcCredentialConfigFileName   = "credential-configuration.json"
	oidcServiceAccountName         = "gcs-fuse-oidc-ksa"
	oidcVolumeName                 = "gcs-volume"
	oidcMountPath                  = "/mnt/gcs"
)

type gcsFuseCSIOIDCTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIOIDCTestSuite returns gcsFuseCSIOIDCTestSuite that implements TestSuite interface.
func InitGcsFuseCSIOIDCTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIOIDCTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "oidc",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIOIDCTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIOIDCTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIOIDCTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("oidc", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	// setupOIDCInfrastructure sets up the GCP OIDC infrastructure (workload identity pool and provider).
	// Returns projectNumber and credentialConfig.
	setupOIDCInfrastructure := func() (string, string) {
		projectID := os.Getenv("PROJECT_ID")
		gomega.Expect(projectID).NotTo(gomega.BeEmpty(), "PROJECT_ID environment variable must be set")

		ginkgo.By("Getting GCP project number")
		projectNumber := getProjectNumber(projectID)
		gomega.Expect(projectNumber).NotTo(gomega.BeEmpty(), "Failed to get project number")

		ginkgo.By("Getting cluster information")
		clusterName := os.Getenv("CLUSTER_NAME")
		clusterLocation := os.Getenv("CLUSTER_LOCATION")
		gomega.Expect(clusterName).NotTo(gomega.BeEmpty(), "CLUSTER_NAME environment variable must be set")
		gomega.Expect(clusterLocation).NotTo(gomega.BeEmpty(), "CLUSTER_LOCATION environment variable must be set")

		ginkgo.By(fmt.Sprintf("Creating workload identity pool: %s", oidcWorkloadIdentityPoolID))
		createWorkloadIdentityPool(projectID, oidcWorkloadIdentityPoolID)

		ginkgo.By("Getting cluster OIDC issuer URL")
		clusterIssuer := getClusterOIDCIssuer(clusterName, clusterLocation, projectID)
		gomega.Expect(clusterIssuer).NotTo(gomega.BeEmpty(), "Failed to get cluster OIDC issuer")

		ginkgo.By(fmt.Sprintf("Creating workload identity provider: %s", oidcWorkloadIdentityProviderID))
		createWorkloadIdentityProvider(projectID, oidcWorkloadIdentityPoolID, oidcWorkloadIdentityProviderID, clusterIssuer)

		ginkgo.By("Generating credential configuration file")
		credentialConfig := generateCredentialConfig(projectNumber, oidcWorkloadIdentityPoolID, oidcWorkloadIdentityProviderID)

		return projectNumber, credentialConfig
	}

	// setupOIDCKubernetesResources creates the K8s service account and configmap with credential configuration.
	setupOIDCKubernetesResources := func(credentialConfig string) {
		ginkgo.By(fmt.Sprintf("Creating Kubernetes service account: %s", oidcServiceAccountName))
		createServiceAccount(ctx, f, oidcServiceAccountName)

		ginkgo.By(fmt.Sprintf("Creating ConfigMap: %s", oidcConfigMapName))
		createCredentialConfigMap(ctx, f, oidcConfigMapName, credentialConfig)
	}

	// cleanupOIDCKubernetesResources deletes the K8s service account and configmap.
	cleanupOIDCKubernetesResources := func() {
		deleteServiceAccount(ctx, f, oidcServiceAccountName)
		deleteConfigMap(ctx, f, oidcConfigMapName)
	}

	// grantOIDCBucketAccess grants bucket access to the OIDC workload identity.
	grantOIDCBucketAccess := func(bucketName, projectNumber string) {
		ginkgo.By("Granting bucket access to workload identity")
		principal := fmt.Sprintf("principal://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/subject/system:serviceaccount:%s:%s",
			projectNumber, oidcWorkloadIdentityPoolID, f.Namespace.Name, oidcServiceAccountName)
		grantBucketAccess(bucketName, principal, "roles/storage.objectUser")

		ginkgo.By("Waiting for IAM policy propagation")
		time.Sleep(5 * time.Second)
	}

	// revokeOIDCBucketAccess revokes bucket access from the OIDC workload identity.
	revokeOIDCBucketAccess := func(bucketName, projectNumber string) {
		principal := fmt.Sprintf("principal://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/subject/system:serviceaccount:%s:%s",
			projectNumber, oidcWorkloadIdentityPoolID, f.Namespace.Name, oidcServiceAccountName)
		revokeBucketAccess(bucketName, principal, "roles/storage.objectUser")
	}

	testCaseOIDCMount := func() {
		init(specs.SkipCSIBucketAccessCheckPrefix)
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		projectNumber, credentialConfig := setupOIDCInfrastructure()
		setupOIDCKubernetesResources(credentialConfig)
		defer cleanupOIDCKubernetesResources()

		grantOIDCBucketAccess(bucketName, projectNumber)
		defer revokeOIDCBucketAccess(bucketName, projectNumber)

		ginkgo.By("Configuring test pod with OIDC authentication")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetServiceAccount(oidcServiceAccountName)
		tPod.SetupVolume(l.volumeResource, oidcVolumeName, oidcMountPath, false)
		tPod.SetAnnotations(map[string]string{
			webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: oidcConfigMapName,
		})

		ginkgo.By("Deploying test pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Verifying the volume is mounted")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("mount | grep %v | grep rw,", oidcMountPath))
	}

	testCaseOIDCStoreData := func() {
		init(specs.SkipCSIBucketAccessCheckPrefix)
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		projectNumber, credentialConfig := setupOIDCInfrastructure()
		setupOIDCKubernetesResources(credentialConfig)
		defer cleanupOIDCKubernetesResources()

		grantOIDCBucketAccess(bucketName, projectNumber)
		defer revokeOIDCBucketAccess(bucketName, projectNumber)

		ginkgo.By("Configuring test pod with OIDC authentication")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetServiceAccount(oidcServiceAccountName)
		tPod.SetupVolume(l.volumeResource, oidcVolumeName, oidcMountPath, false)
		tPod.SetAnnotations(map[string]string{
			webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: oidcConfigMapName,
		})

		ginkgo.By("Deploying test pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Writing a test file to the bucket")
		testFileName := "oidc-test-file.txt"
		testContent := "Hello from OIDC authentication test!"
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo '%s' > %s/%s", testContent, oidcMountPath, testFileName))

		ginkgo.By("Reading the test file from the bucket")
		readOutput := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName,
			fmt.Sprintf("cat %s/%s", oidcMountPath, testFileName))
		gomega.Expect(strings.TrimSpace(readOutput)).To(gomega.Equal(testContent))

		ginkgo.By("Verifying file exists in bucket using gsutil")
		verifyFileInBucket(bucketName, testFileName)

		ginkgo.By("Cleaning up test file")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("rm %s/%s", oidcMountPath, testFileName))
	}

	testCaseOIDCStoreDataInImplicitDir := func() {
		init(specs.SkipCSIBucketAccessCheckPrefix)
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		projectNumber, credentialConfig := setupOIDCInfrastructure()
		setupOIDCKubernetesResources(credentialConfig)
		defer cleanupOIDCKubernetesResources()

		grantOIDCBucketAccess(bucketName, projectNumber)
		defer revokeOIDCBucketAccess(bucketName, projectNumber)

		ginkgo.By("Configuring test pod with OIDC authentication")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetServiceAccount(oidcServiceAccountName)
		tPod.SetupVolume(l.volumeResource, oidcVolumeName, oidcMountPath, false)
		tPod.SetAnnotations(map[string]string{
			webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: oidcConfigMapName,
		})

		ginkgo.By("Deploying test pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Creating an implicit directory and writing a test file")
		testDirPath := "oidc-test-dir"
		testFileName := "test-file.txt"
		testContent := "Hello from implicit directory!"
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("mkdir -p %s/%s", oidcMountPath, testDirPath))
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("echo '%s' > %s/%s/%s", testContent, oidcMountPath, testDirPath, testFileName))

		ginkgo.By("Reading the test file from the implicit directory")
		readOutput := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName,
			fmt.Sprintf("cat %s/%s/%s", oidcMountPath, testDirPath, testFileName))
		gomega.Expect(strings.TrimSpace(readOutput)).To(gomega.Equal(testContent))

		ginkgo.By("Verifying file exists in bucket using gsutil")
		verifyFileInBucket(bucketName, fmt.Sprintf("%s/%s", testDirPath, testFileName))

		ginkgo.By("Cleaning up test files and directory")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("rm -rf %s/%s", oidcMountPath, testDirPath))
	}

	testCaseOIDCMissingConfigMap := func() {
		init(specs.SkipCSIBucketAccessCheckPrefix)
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		projectNumber, _ := setupOIDCInfrastructure()
		
		// Only create service account, but NOT the ConfigMap
		ginkgo.By(fmt.Sprintf("Creating Kubernetes service account: %s", oidcServiceAccountName))
		createServiceAccount(ctx, f, oidcServiceAccountName)
		defer deleteServiceAccount(ctx, f, oidcServiceAccountName)

		grantOIDCBucketAccess(bucketName, projectNumber)
		defer revokeOIDCBucketAccess(bucketName, projectNumber)

		ginkgo.By("Configuring test pod with OIDC annotation but missing ConfigMap")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetServiceAccount(oidcServiceAccountName)
		tPod.SetupVolume(l.volumeResource, oidcVolumeName, oidcMountPath, false)
		// Reference a ConfigMap that doesn't exist
		tPod.SetAnnotations(map[string]string{
			webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: oidcConfigMapName,
		})

		ginkgo.By("Deploying test pod and expecting error")
		tPod.CreateExpectError(ctx)
	}

	testCaseOIDCWithCSIBucketAccessCheck := func() {
		// Note: NOT using SkipCSIBucketAccessCheckPrefix
		init()
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		projectNumber, credentialConfig := setupOIDCInfrastructure()
		setupOIDCKubernetesResources(credentialConfig)
		defer cleanupOIDCKubernetesResources()

		grantOIDCBucketAccess(bucketName, projectNumber)
		defer revokeOIDCBucketAccess(bucketName, projectNumber)

		ginkgo.By("Configuring test pod with OIDC authentication")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetServiceAccount(oidcServiceAccountName)
		tPod.SetupVolume(l.volumeResource, oidcVolumeName, oidcMountPath, false)
		tPod.SetAnnotations(map[string]string{
			webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: oidcConfigMapName,
		})

		ginkgo.By("Deploying test pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the pod has failed mount error due to CSI bucket access check")
		// The CSI driver's bucket access check will fail because it doesn't use OIDC credentials
		// during the mount validation phase. It likely uses the node's service account which
		// doesn't have access to the bucket.
		tPod.WaitForFailedMountError(ctx, "PermissionDenied")
	}

	ginkgo.It("should successfully mount with OIDC authentication", func() {
		testCaseOIDCMount()
	})

	ginkgo.It("should store and retain data with OIDC authentication", func() {
		testCaseOIDCStoreData()
	})

	ginkgo.It("should store data in implicit directory with OIDC authentication", func() {
		testCaseOIDCStoreDataInImplicitDir()
	})

	ginkgo.It("should fail when OIDC ConfigMap is missing", func() {
		testCaseOIDCMissingConfigMap()
	})

	ginkgo.It("should fail when CSI bucket access check is enabled with OIDC authentication", func() {
		testCaseOIDCWithCSIBucketAccessCheck()
	})
}

// Helper functions for GCP operations

func getProjectNumber(projectID string) string {
	cmd := exec.Command("gcloud", "projects", "describe", projectID, "--format=value(projectNumber)")
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Errorf("Failed to get project number: %v, output: %s", err, string(output))
		return ""
	}
	return strings.TrimSpace(string(output))
}

func createWorkloadIdentityPool(projectID, poolID string) {
	cmd := exec.Command("gcloud", "iam", "workload-identity-pools", "create", poolID,
		"--project="+projectID,
		"--location=global",
		"--display-name=GCS FUSE OIDC Test Pool")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Ignore if pool already exists
		if !strings.Contains(string(output), "already exists") {
			klog.Warningf("Failed to create workload identity pool (may already exist): %v, output: %s", err, string(output))
		}
	} else {
		klog.Infof("Created workload identity pool: %s", poolID)
	}
}

func getClusterOIDCIssuer(clusterName, clusterLocation, projectID string) string {
	// The issuer URL for a GKE cluster is constructed as follows.
	return fmt.Sprintf("https://container.googleapis.com/v1/projects/%s/locations/%s/clusters/%s",
		projectID, clusterLocation, clusterName)
}

func createWorkloadIdentityProvider(projectID, poolID, providerID, issuerURI string) {
	cmd := exec.Command("gcloud", "iam", "workload-identity-pools", "providers", "create-oidc", providerID,
		"--project="+projectID,
		"--location=global",
		"--workload-identity-pool="+poolID,
		"--display-name=GCS FUSE OIDC Test Provider",
		"--attribute-mapping=google.subject=assertion.sub",
		"--issuer-uri="+issuerURI)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Ignore if provider already exists
		if !strings.Contains(string(output), "already exists") {
			klog.Warningf("Failed to create workload identity provider (may already exist): %v, output: %s", err, string(output))
		}
	} else {
		klog.Infof("Created workload identity provider: %s", providerID)
	}
}

func generateCredentialConfig(projectNumber, poolID, providerID string) string {
	config := map[string]interface{}{
		"type": "external_account",
		"audience": fmt.Sprintf("//iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
			projectNumber, poolID, providerID),
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"token_url":          "https://sts.googleapis.com/v1/token",
		"credential_source": map[string]string{
			"file": "/var/run/service-account/token",
		},
	}

	configJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		klog.Fatalf("Failed to marshal credential config: %v", err)
	}

	return string(configJSON)
}

func createServiceAccount(ctx context.Context, f *framework.Framework, name string) {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace.Name,
		},
	}
	_, err := f.ClientSet.CoreV1().ServiceAccounts(f.Namespace.Name).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil {
		framework.Failf("Failed to create service account: %v", err)
	}
	klog.Infof("Created service account: %s", name)
}

func deleteServiceAccount(ctx context.Context, f *framework.Framework, name string) {
	err := f.ClientSet.CoreV1().ServiceAccounts(f.Namespace.Name).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		klog.Warningf("Failed to delete service account: %v", err)
	}
}

func createCredentialConfigMap(ctx context.Context, f *framework.Framework, name, credentialConfig string) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace.Name,
		},
		Data: map[string]string{
			oidcCredentialConfigFileName: credentialConfig,
		},
	}
	_, err := f.ClientSet.CoreV1().ConfigMaps(f.Namespace.Name).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		framework.Failf("Failed to create ConfigMap: %v", err)
	}
	klog.Infof("Created ConfigMap: %s", name)
}

func deleteConfigMap(ctx context.Context, f *framework.Framework, name string) {
	err := f.ClientSet.CoreV1().ConfigMaps(f.Namespace.Name).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		klog.Warningf("Failed to delete ConfigMap: %v", err)
	}
}

func grantBucketAccess(bucketName, principal, role string) {
	cmd := exec.Command("gcloud", "storage", "buckets", "add-iam-policy-binding",
		"gs://"+bucketName,
		"--member="+principal,
		"--role="+role)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Errorf("Failed to grant bucket access: %v, output: %s", err, string(output))
		framework.Failf("Failed to grant bucket access: %v", err)
	}
	klog.Infof("Granted %s access to bucket %s for principal %s", role, bucketName, principal)
}

func revokeBucketAccess(bucketName, principal, role string) {
	cmd := exec.Command("gcloud", "storage", "buckets", "remove-iam-policy-binding",
		"gs://"+bucketName,
		"--member="+principal,
		"--role="+role)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Warningf("Failed to revoke bucket access: %v, output: %s", err, string(output))
	}
}

func verifyFileInBucket(bucketName, fileName string) {
	cmd := exec.Command("gsutil", "ls", fmt.Sprintf("gs://%s/%s", bucketName, fileName))
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Errorf("Failed to verify file in bucket: %v, output: %s", err, string(output))
		framework.Failf("File %s not found in bucket %s", fileName, bucketName)
	}
	klog.Infof("Verified file %s exists in bucket %s", fileName, bucketName)
}
