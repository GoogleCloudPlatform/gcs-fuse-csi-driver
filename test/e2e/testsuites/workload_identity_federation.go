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

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/pkg/webhook"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	iam "google.golang.org/api/iam/v1"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
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

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
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

	// setupOSSWIFPrincipal creates OSS Workload Identity Federation infrastructure for the
	// given namespace: WIF pool and provider (idempotent — safe to call for multiple namespaces),
	// a KSA, and a credential ConfigMap. Returns the WIF principal and credential config JSON.
	// Cleanup is registered via ginkgo.DeferCleanup.
	// If namespace is empty, f.Namespace.Name is used.
	setupOSSWIFPrincipal := func(ksaName, poolID, providerID, configMapName, namespace string, c clientset.Interface) (string, string) {
		if namespace == "" {
			namespace = f.Namespace.Name
		}
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

		ginkgo.By(fmt.Sprintf("Creating Kubernetes service account %s in namespace %s", ksaName, namespace))
		createServiceAccountInNamespace(ctx, c, namespace, ksaName)
		ginkgo.DeferCleanup(func() { deleteServiceAccountInNamespace(ctx, c, namespace, ksaName) })

		ginkgo.By(fmt.Sprintf("Creating credential ConfigMap %s in namespace %s", configMapName, namespace))
		createCredentialConfigMapInNamespace(ctx, c, namespace, configMapName, credentialConfig)
		ginkgo.DeferCleanup(func() { deleteCredentialConfigMapInNamespace(ctx, c, namespace, configMapName) })

		principal := fmt.Sprintf(
			"principal://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/subject/system:serviceaccount:%s:%s",
			projectNumber, poolID, namespace, ksaName,
		)
		return principal, credentialConfig
	}

	// setupGKEWIPrincipal creates a GCP service account, binds ksaName in ns via
	// roles/iam.workloadIdentityUser, and creates the annotated KSA. Returns the
	// GSA member string. Cleanup is registered via ginkgo.DeferCleanup.
	// The caller is responsible for waiting for the WI binding to propagate.
	// If ns is nil, f.Namespace is used.
	setupGKEWIPrincipal := func(ksaName, projectID string, ns *corev1.Namespace) string {
		if ns == nil {
			ns = f.Namespace
		}
		saName := ns.Name
		if len(saName) > 30 {
			saName = saName[:30]
		}
		testGcpSA := utils.NewTestGCPServiceAccount(saName, projectID)
		ginkgo.By(fmt.Sprintf("Creating GCP service account: %s", saName))
		testGcpSA.Create(ctx)
		ginkgo.DeferCleanup(func() { testGcpSA.Cleanup(ctx) })

		ginkgo.By(fmt.Sprintf("Binding KSA %s in namespace %s to GCP service account %s", ksaName, ns.Name, testGcpSA.GetEmail()))
		addWorkloadIdentityBinding(ctx, testGcpSA.GetEmail(), projectID, ns.Name, ksaName)

		ginkgo.By(fmt.Sprintf("Creating Kubernetes service account %s in namespace %s annotated with %s", ksaName, ns.Name, testGcpSA.GetEmail()))
		testK8sSA := utils.NewTestKubernetesServiceAccount(f.ClientSet, ns, ksaName, testGcpSA.GetEmail())
		testK8sSA.Create(ctx)
		ginkgo.DeferCleanup(func() { testK8sSA.Cleanup(ctx) })

		return "serviceAccount:" + testGcpSA.GetEmail()
	}

	ginkgo.It("should isolate workload identity federation access for Kubernetes service accounts with the same name across different namespaces", func() {
		isOSS := os.Getenv(utils.IsOSSEnvVar) == "true"

		// OSS: credential ConfigMap doesn't exist at mount time, so the CSI pre-mount
		// bucket access check would fail — skip it and let authz errors surface on I/O.
		// GKE: WI binding and bucket access are both ready before the pod starts, so the
		// pre-mount check can run normally.
		if isOSS {
			init(specs.SkipCSIBucketAccessCheckPrefix)
		} else {
			init()
		}
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		const (
			sharedKSAName           = "gcsfuse-test-sa"
			volumeName              = "gcs-volume"
			mountPath               = "/mnt/gcs"
			credentialConfigMapName = "wif-isolation-credentials"
		)

		// ns-1: f.Namespace (framework-managed, will receive IAM access)
		// ns-2: manually created with the same KSA name but no IAM access
		ginkgo.By("Creating second namespace for identity isolation test")
		ns2, err := f.ClientSet.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "wif-isolation-",
				Labels: map[string]string{
					admissionapi.EnforceLevelLabel: string(admissionapi.LevelPrivileged),
				},
			},
		}, metav1.CreateOptions{})
		framework.ExpectNoError(err, "creating second namespace for isolation test")
		ginkgo.DeferCleanup(func() {
			if delErr := f.ClientSet.CoreV1().Namespaces().Delete(ctx, ns2.Name, metav1.DeleteOptions{}); delErr != nil {
				klog.Warningf("failed to delete namespace %s: %v", ns2.Name, delErr)
			}
		})

		var ns1Principal string

		if isOSS {
			// OSS: both namespaces receive the same WIF pool/provider credential config so
			// both KSAs exchange their tokens via WIF rather than falling back to the node's
			// default identity. The WIF subject is derived from the JWT "sub" claim, which
			// encodes the namespace, making the two principals distinct:
			//   ns-1: system:serviceaccount:<f.Namespace>:<ksaName>  → granted bucket access
			//   ns-2: system:serviceaccount:<ns-2>:<ksaName>         → no bucket access
			ns1Principal, _ = setupOSSWIFPrincipal(sharedKSAName, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, credentialConfigMapName, f.Namespace.Name, f.ClientSet)
			// Pool and provider are idempotent; only the KSA and ConfigMap differ by namespace.
			setupOSSWIFPrincipal(sharedKSAName, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, credentialConfigMapName, ns2.Name, f.ClientSet)
		} else {
			// GKE: both KSAs are annotated with dedicated GSAs that have roles/iam.workloadIdentityUser
			// bindings, preventing any fallback to the node's default service account.
			// Only ns-1's GSA receives a GCS bucket IAM binding.
			// Both WI bindings are created before the IAM grant so they propagate during
			// the shared 2-minute sleep below.
			rawProjectID := os.Getenv(utils.ProjectEnvVar)
			lines := strings.Split(strings.TrimSpace(rawProjectID), "\n")
			projectID := lines[len(lines)-1]
			gomega.Expect(strings.Contains(projectID, "Your active configuration")).To(gomega.BeFalse(),
				fmt.Sprintf("invalid projectID detected: %q", projectID))

			setupGKEWIPrincipal(sharedKSAName, projectID, ns2)
			ns1Principal = setupGKEWIPrincipal(sharedKSAName, projectID, f.Namespace)
		}

		ginkgo.By("Granting GCS bucket access to ns-1 principal only")
		grantBucketAccess(bucketName, ns1Principal, "roles/storage.objectAdmin")
		defer revokeBucketAccess(bucketName, ns1Principal, "roles/storage.objectAdmin")

		ginkgo.By("Waiting for IAM policy and WIF infrastructure propagation")
		time.Sleep(2 * time.Minute)

		// Deploy authorized pod in ns-1. Kept long-running (default "tail -f /dev/null") so
		// that mount success and GCS write access can be validated explicitly via exec.
		ginkgo.By("Deploying authorized pod in ns-1 with GCS write access")
		tPodNs1 := specs.NewTestPodModifiedSpec(f.ClientSet, f.Namespace, true)
		tPodNs1.SetServiceAccount(sharedKSAName)
		tPodNs1.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		if isOSS {
			tPodNs1.SetAnnotations(map[string]string{
				webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: credentialConfigMapName,
			})
		}

		tPodNs1.Create(ctx)
		defer tPodNs1.Cleanup(ctx)

		// Deploy unauthorized pod in ns-2. No custom command needed — the mount itself
		// is expected to fail with PermissionDenied before the main container starts.
		// GKE: CSI pre-mount check uses gcp-sa-ns2 (no bucket access) → FailedMount.
		// OSS: CSI check is skipped but gcsfuse fails with PermissionDenied → FailedMount.
		ginkgo.By("Deploying unauthorized pod in ns-2 with same KSA name but no GCS access")
		tPodNs2 := specs.NewTestPodModifiedSpec(f.ClientSet, ns2, true)
		tPodNs2.SetServiceAccount(sharedKSAName)
		tPodNs2.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		if isOSS {
			tPodNs2.SetAnnotations(map[string]string{
				webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: credentialConfigMapName,
			})
		}
		tPodNs2.Create(ctx)
		defer tPodNs2.Cleanup(ctx)

		ginkgo.By("Waiting for ns-1 pod (authorized identity) to reach Running state — confirms GCS mount succeeded")
		tPodNs1.WaitForRunning(ctx)

		ginkgo.By("Verifying the GCS volume is mounted read-write in ns-1 pod")
		tPodNs1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("mount | grep %s | grep rw,", mountPath))

		ginkgo.By("Verifying ns-1 pod (authorized identity) can write to the GCS bucket")
		tPodNs1.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("dd if=/dev/urandom bs=1M count=1 of=%s/ns1-test.bin 2>&1", mountPath))

		ginkgo.By("Verifying ns-2 pod (unauthorized identity) fails to mount — confirms WIF identity isolation")
		tPodNs2.WaitForFailedMountError(ctx, "PermissionDenied")
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
		if alreadyBound {
			return true, nil
		}
		policy.Bindings = append(policy.Bindings, &iam.Binding{
			Role:    "roles/iam.workloadIdentityUser",
			Members: []string{member},
		})
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

// createServiceAccountInNamespace creates a Kubernetes ServiceAccount in the given namespace.
func createServiceAccountInNamespace(ctx context.Context, c clientset.Interface, namespace, name string) {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	_, err := c.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil {
		framework.Failf("failed to create service account %s in namespace %s: %v", name, namespace, err)
	}
	klog.Infof("Created service account %s in namespace %s", name, namespace)
}

// deleteServiceAccountInNamespace deletes a Kubernetes ServiceAccount from the given namespace.
func deleteServiceAccountInNamespace(ctx context.Context, c clientset.Interface, namespace, name string) {
	if err := c.CoreV1().ServiceAccounts(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		klog.Warningf("failed to delete service account %s in namespace %s: %v", name, namespace, err)
	}
}

// createCredentialConfigMapInNamespace creates a credential ConfigMap in the given namespace.
func createCredentialConfigMapInNamespace(ctx context.Context, c clientset.Interface, namespace, name, credentialConfig string) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string]string{
			oidcCredentialConfigFileName: credentialConfig,
		},
	}
	_, err := c.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		framework.Failf("failed to create ConfigMap %s in namespace %s: %v", name, namespace, err)
	}
	klog.Infof("Created ConfigMap %s in namespace %s", name, namespace)
}

// deleteCredentialConfigMapInNamespace deletes a credential ConfigMap from the given namespace.
func deleteCredentialConfigMapInNamespace(ctx context.Context, c clientset.Interface, namespace, name string) {
	if err := c.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		klog.Warningf("failed to delete ConfigMap %s in namespace %s: %v", name, namespace, err)
	}
}
