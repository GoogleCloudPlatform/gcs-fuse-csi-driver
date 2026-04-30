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

package testsuites

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"local/test/e2e/specs"
	"local/test/e2e/utils"

	gostorage "cloud.google.com/go/storage"
	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	iam "google.golang.org/api/iam/v1"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	klog "k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	wifWorkloadIdentityPoolID     = "gcs-fuse-oidc-pool"
	wifWorkloadIdentityProviderID = "gcs-fuse-oidc-provider"
)

type gcsFuseCSIWorkloadIdentityFederationTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIWorkloadIdentityFederationTestSuite returns a suite with WIF-focused tests.
func InitGcsFuseCSIWorkloadIdentityFederationTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIWorkloadIdentityFederationTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "workload-identity-federation",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIWorkloadIdentityFederationTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIWorkloadIdentityFederationTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIWorkloadIdentityFederationTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	f := framework.NewFrameworkWithCustomTimeouts("workload-identity-federation", storageframework.GetDriverTimeouts(driver))
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

	// setupOSSWIFPrincipal creates all OSS Workload Identity Federation infrastructure
	// (WIF pool, provider, KSA, credential ConfigMap) for ksaName and returns the
	// WIF principal string. Cleanup is registered via ginkgo.DeferCleanup.
	setupOSSWIFPrincipal := func(ksaName, poolID, providerID, configMapName string) string {
		projectID := os.Getenv(utils.ProjectEnvVar)
		gomega.Expect(projectID).NotTo(gomega.BeEmpty(), fmt.Sprintf("%s environment variable must be set", utils.ProjectEnvVar))

		ginkgo.By("Getting GCP project number")
		projectNumber := getProjectNumber(projectID)
		gomega.Expect(projectNumber).NotTo(gomega.BeEmpty(), "failed to get project number")

		ginkgo.By(fmt.Sprintf("Creating workload identity pool: %s", poolID))
		createWorkloadIdentityPool(projectID, poolID)

		ginkgo.By("Discovering cluster OIDC issuer from cluster service account token")
		clusterIssuer := getOSSClusterOIDCIssuer(ctx, f)
		gomega.Expect(clusterIssuer).NotTo(gomega.BeEmpty(), "failed to discover cluster OIDC issuer")

		ginkgo.By(fmt.Sprintf("Creating workload identity provider: %s", providerID))
		createWorkloadIdentityProvider(projectID, poolID, providerID, clusterIssuer)

		ginkgo.By("Generating credential configuration")
		credentialConfig := generateCredentialConfig(projectNumber, poolID, providerID)

		ginkgo.By(fmt.Sprintf("Creating Kubernetes service account: %s", ksaName))
		createServiceAccount(ctx, f, ksaName)
		ginkgo.DeferCleanup(func() { deleteServiceAccount(ctx, f, ksaName) })

		ginkgo.By(fmt.Sprintf("Creating credential ConfigMap: %s", configMapName))
		createCredentialConfigMap(ctx, f, configMapName, credentialConfig)
		ginkgo.DeferCleanup(func() { deleteConfigMap(ctx, f, configMapName) })

		return fmt.Sprintf(
			"principal://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/subject/system:serviceaccount:%s:%s",
			projectNumber, poolID, f.Namespace.Name, ksaName,
		)
	}

	// setupGKEWIPrincipal creates a GCP service account, binds it to ksaName via
	// roles/iam.workloadIdentityUser, and creates the annotated KSA. Returns the
	// GSA principal string. Cleanup is registered via ginkgo.DeferCleanup.
	setupGKEWIPrincipal := func(ksaName string) string {
		rawProjectID := os.Getenv(utils.ProjectEnvVar)
		gomega.Expect(rawProjectID).NotTo(gomega.BeEmpty(), "PROJECT must be set")
		projectID := sanitizeProjectID(rawProjectID)
		gomega.Expect(strings.Contains(projectID, "Your active configuration")).To(gomega.BeFalse(),
			fmt.Sprintf("invalid projectID detected: %q", projectID))

		saName := f.Namespace.Name
		if len(saName) > 30 {
			saName = saName[:30]
		}
		testGcpSA := utils.NewTestGCPServiceAccount(saName, projectID)
		ginkgo.By(fmt.Sprintf("Creating GCP service account: %s", saName))
		testGcpSA.Create(ctx)
		ginkgo.DeferCleanup(func() { testGcpSA.Cleanup(ctx) })

		ginkgo.By(fmt.Sprintf("Binding KSA %s to GCP service account %s with roles/iam.workloadIdentityUser", ksaName, testGcpSA.GetEmail()))
		addWorkloadIdentityBinding(ctx, testGcpSA.GetEmail(), projectID, f.Namespace.Name, ksaName)

		ginkgo.By(fmt.Sprintf("Creating Kubernetes service account %s annotated with GCP service account %s", ksaName, testGcpSA.GetEmail()))
		testK8sSA := utils.NewTestKubernetesServiceAccount(f.ClientSet, f.Namespace, ksaName, testGcpSA.GetEmail())
		testK8sSA.Create(ctx)
		ginkgo.DeferCleanup(func() { testK8sSA.Cleanup(ctx) })

		ginkgo.By("Waiting for Workload Identity binding to propagate globally (~2 minutes)")
		time.Sleep(2 * time.Minute)

		return "serviceAccount:" + testGcpSA.GetEmail()
	}


	// deployWIFPod creates a pod with the WIF KSA, mounts the volume, and applies the
	// credential ConfigMap annotation when running on an OSS cluster.
	deployWIFPod := func(ksaName, credentialConfigMapName, volumeName, mountPath string) *specs.TestPod {
		tPod := specs.NewTestPodModifiedSpec(f.ClientSet, f.Namespace, true)
		tPod.SetServiceAccount(ksaName)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		if credentialConfigMapName != "" {
			tPod.SetAnnotations(map[string]string{
				webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: credentialConfigMapName,
			})
		}
		tPod.Create(ctx)
		return tPod
	}

	ginkgo.It("should fail GCS access when WI principal has no storage role", func() {
		isOSS := os.Getenv(utils.IsOSSEnvVar) == "true"
		if isOSS {
			init(specs.SkipCSIBucketAccessCheckPrefix)
		} else {
			init()
		}		
		defer cleanup()

		const (
			wifKSA        = "wif-no-role-ksa"
			configMapName = "wif-credentials-no-role"
			volumeName    = "gcs-wif-volume"
			mountPath     = "/mnt/gcs"
		)

		var principal string
		if isOSS {
			principal = setupOSSWIFPrincipal(wifKSA, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, configMapName)
		} else {
			principal = setupGKEWIPrincipal(wifKSA)
		}
		_ = principal // intentionally no bucket IAM role granted

		credMap := ""
		if isOSS {
			credMap = configMapName
		}
		tPod := deployWIFPod(wifKSA, credMap, volumeName, mountPath)
		defer tPod.Cleanup(ctx)

		if os.Getenv(utils.TestWithSidecarBucketAccessCheckEnvVar) == "true" || os.Getenv(utils.IsOSSEnvVar) == "true" {
			ginkgo.By("Checking that the sidecar bucket access check returns PermissionDenied")
			tPod.WaitForFailedMountError(ctx, "PermissionDenied")
		} else {
			tPod.WaitForRunning(ctx)
			ginkgo.By("Checking that gcsfuse logs a permission denied error from GCS")
			tPod.WaitForLog(ctx, webhook.GcsFuseSidecarName, "PermissionDenied")
		}
	})

	ginkgo.It("should fail write operations when WI principal has read-only storage role", func() {
		isOSS := os.Getenv(utils.IsOSSEnvVar) == "true"
		if isOSS {
			init(specs.SkipCSIBucketAccessCheckPrefix)
		} else {
			init()
		}
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		const (
			wifKSA        = "wif-readonly-ksa"
			configMapName = "wif-credentials-readonly"
			volumeName    = "gcs-wif-volume"
			mountPath     = "/mnt/gcs"
		)

		var principal string
		if isOSS {
			principal = setupOSSWIFPrincipal(wifKSA, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, configMapName)
		} else {
			principal = setupGKEWIPrincipal(wifKSA)
		}

		ginkgo.By("Granting read-only (objectViewer) access to bucket")
		grantBucketAccess(bucketName, principal, "roles/storage.objectViewer")
		defer revokeBucketAccess(bucketName, principal, "roles/storage.objectViewer")

		ginkgo.By("Waiting for IAM policy propagation")
		time.Sleep(5 * time.Second)

		credMap := ""
		if isOSS {
			credMap = configMapName
		}
		tPod := deployWIFPod(wifKSA, credMap, volumeName, mountPath)
		defer tPod.Cleanup(ctx)

		tPod.WaitForRunning(ctx)

		ginkgo.By("Verifying read operations succeed with objectViewer role")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("ls %v", mountPath))

		ginkgo.By("Verifying write operations fail with objectViewer role")
		tPod.VerifyExecInPodFail(f, specs.TesterContainerName,
			fmt.Sprintf("echo 'write-test' > %v/wif-write-test.txt", mountPath), 1)
	})

	ginkgo.It("should fail GCS access when WI principal role is on a different bucket", func() {
		isOSS := os.Getenv(utils.IsOSSEnvVar) == "true"
		if isOSS {
			init(specs.SkipCSIBucketAccessCheckPrefix)
		} else {
			init()
		}		
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		const (
			wifKSA        = "wif-wrong-bucket-ksa"
			configMapName = "wif-credentials-wrong-bucket"
			volumeName    = "gcs-wif-volume"
			mountPath     = "/mnt/gcs"
		)

		projectID := sanitizeProjectID(os.Getenv(utils.ProjectEnvVar))

		var principal string
		if isOSS {
			principal = setupOSSWIFPrincipal(wifKSA, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, configMapName)
		} else {
			principal = setupGKEWIPrincipal(wifKSA)
		}

		altBucket := fmt.Sprintf("gcs-fuse-wif-alt-%s", f.Namespace.Name)
		ginkgo.By(fmt.Sprintf("Creating alternate bucket: %s", altBucket))
		storageClient, err := gostorage.NewClient(ctx)
		framework.ExpectNoError(err, "creating GCS storage client for alternate bucket")
		defer storageClient.Close()
		if err := storageClient.Bucket(altBucket).Create(ctx, projectID, nil); err != nil {
			framework.Failf("Failed to create alternate bucket %s: %v", altBucket, err)
		}
		defer func() {
			if err := storageClient.Bucket(altBucket).Delete(ctx); err != nil {
				klog.Warningf("Failed to delete alternate bucket %s: %v", altBucket, err)
			}
		}()

		ginkgo.By(fmt.Sprintf("Granting objectUser on alternate bucket %s (not on test bucket %s)", altBucket, bucketName))
		grantBucketAccess(altBucket, principal, "roles/storage.objectUser")
		defer revokeBucketAccess(altBucket, principal, "roles/storage.objectUser")

		ginkgo.By("Waiting for IAM policy propagation")
		time.Sleep(5 * time.Second)

		credMap := ""
		if isOSS {
			credMap = configMapName
		}
		tPod := deployWIFPod(wifKSA, credMap, volumeName, mountPath)
		defer tPod.Cleanup(ctx)

		if os.Getenv(utils.TestWithSidecarBucketAccessCheckEnvVar) == "true" || os.Getenv(utils.IsOSSEnvVar) == "true" {
			ginkgo.By("Checking that the sidecar bucket access check returns PermissionDenied")
			tPod.WaitForFailedMountError(ctx, "PermissionDenied")
		} else {
			tPod.WaitForRunning(ctx)
			ginkgo.By("Checking that gcsfuse logs a permission denied error for the test bucket")
			tPod.WaitForLog(ctx, webhook.GcsFuseSidecarName, "PermissionDenied")
		}
	})
}

// addWorkloadIdentityBinding grants roles/iam.workloadIdentityUser on the given GCP service
// account to the Workload Identity principal for ksaName, enabling GKE token exchange.
// Retries with backoff to handle IAM eventual consistency after SA creation.
func addWorkloadIdentityBinding(ctx context.Context, gcpSAEmail, projectID, namespace, ksaName string) {
	iamService, err := iam.NewService(ctx)
	framework.ExpectNoError(err, "creating IAM service")
	saResourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", projectID, gcpSAEmail)
	member := fmt.Sprintf("serviceAccount:%s.svc.id.goog[%s/%s]", projectID, namespace, ksaName)

	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		policy, e := iamService.Projects.ServiceAccounts.GetIamPolicy(saResourceName).Do()
		if e != nil {
			klog.Warningf("GetIamPolicy for %s not ready yet: %v — retrying", gcpSAEmail, e)
			return false, nil
		}
		alreadyBound := false
		for _, b := range policy.Bindings {
			if b.Role == "roles/iam.workloadIdentityUser" {
				for _, m := range b.Members {
					if m == member {
						alreadyBound = true
						break
					}
				}
			}
			if alreadyBound {
				break
			}
		}
		if !alreadyBound {
			policy.Bindings = append(policy.Bindings, &iam.Binding{
				Role:    "roles/iam.workloadIdentityUser",
				Members: []string{member},
			})
		}
		if _, e = iamService.Projects.ServiceAccounts.SetIamPolicy(saResourceName, &iam.SetIamPolicyRequest{Policy: policy}).Do(); e != nil {
			klog.Warningf("SetIamPolicy for %s failed: %v — retrying", gcpSAEmail, e)
			return false, nil
		}
		return true, nil
	})
	framework.ExpectNoError(err, "setting workload identity binding for %s", gcpSAEmail)
}

// getOSSClusterOIDCIssuer discovers the cluster OIDC issuer URL by decoding a live
// ServiceAccount token issued by the cluster. Unlike getClusterOIDCIssuer, this works
// on any Kubernetes cluster (GKE or self-managed) without requiring cluster-name or
// location environment variables.
func getOSSClusterOIDCIssuer(ctx context.Context, f *framework.Framework) string {
	expirationSecs := int64(600)
	tok, err := f.ClientSet.CoreV1().ServiceAccounts(f.Namespace.Name).CreateToken(
		ctx,
		"default",
		&authv1.TokenRequest{
			Spec: authv1.TokenRequestSpec{ExpirationSeconds: &expirationSecs},
		},
		metav1.CreateOptions{},
	)
	framework.ExpectNoError(err, "creating service account token to discover cluster OIDC issuer")

	parts := strings.Split(tok.Status.Token, ".")
	if len(parts) != 3 {
		framework.Failf("unexpected JWT format: want 3 parts, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	framework.ExpectNoError(err, "base64-decoding JWT payload")

	var claims struct {
		Issuer string `json:"iss"`
	}
	framework.ExpectNoError(json.Unmarshal(payload, &claims), "unmarshalling JWT claims")
	gomega.Expect(claims.Issuer).NotTo(gomega.BeEmpty(), "cluster OIDC issuer must not be empty")
	return claims.Issuer
}

// sanitizeProjectID strips any Cloud Shell "Your active configuration is: [...]"
// warning that gcloud can prepend to stdout, returning only the actual project ID.
func sanitizeProjectID(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	return lines[len(lines)-1]
}