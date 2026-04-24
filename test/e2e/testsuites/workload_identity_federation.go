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
	klog "k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	wifWorkloadIdentityPoolID     = "gcs-fuse-oidc-pool"
	wifWorkloadIdentityProviderID = "gcs-fuse-oidc-provider"
	// wifFakeProviderID is a WIF provider configured with a deliberately wrong
	// issuer URI. Any STS token exchange against it will always fail with an
	// "invalid_token" error, giving a guaranteed authentication failure that
	// does not depend on the node service account's GCS permissions.
	wifFakeProviderID = "wif-fake-provider"
	wifFakeIssuerURI  = "https://fake-oidc-issuer.example.com"
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

	initWithCSIBucketAccessCheckSkipped := func() {
		// Mount should succeed; authz failures surface on I/O, matching the OIDC test flow.
		init(specs.SkipCSIBucketAccessCheckPrefix)

		if l.volumeResource == nil || l.volumeResource.VolSource == nil || l.volumeResource.VolSource.CSI == nil {
			framework.Failf("volume resource not initialized properly")
		}
		if l.volumeResource.VolSource.CSI.VolumeAttributes == nil {
			l.volumeResource.VolSource.CSI.VolumeAttributes = map[string]string{}
		}
		l.volumeResource.VolSource.CSI.VolumeAttributes["skipCSIBucketAccessCheck"] = "true"
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

		// Fix corrupted gcloud output
		lines := strings.Split(strings.TrimSpace(rawProjectID), "\n")
		projectID = lines[len(lines)-1]

		// Safety check
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

		ginkgo.By("Waiting for Workload Identity binding to propagate globally (~60s)")
		time.Sleep(2 * time.Minute)

		return "serviceAccount:" + testGcpSA.GetEmail()
	}

	ginkgo.It("should fail GCS access after workload identity federation principal permissions are removed while pod is running", func() {
		initWithCSIBucketAccessCheckSkipped()
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		const (
			wifKSA     = "wif-revoke-ksa"
			volumeName = "gcs-volume"
			mountPath  = "/mnt/gcs"
		)

		isOSS := os.Getenv(utils.IsOSSEnvVar) == "true"

		var (
			principal               string
			credentialConfigMapName string
			permissionRevoked       bool
		)

		if isOSS {
			credentialConfigMapName = "wif-revoke-credentials"
			principal = setupOSSWIFPrincipal(wifKSA, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, credentialConfigMapName)
		} else {
			principal = setupGKEWIPrincipal(wifKSA)
		}

		ginkgo.By("Granting bucket access to workload identity principal")
		grantBucketAccess(bucketName, principal, "roles/storage.objectUser")
		defer func() {
			if !permissionRevoked {
				revokeBucketAccess(bucketName, principal, "roles/storage.objectUser")
			}
		}()

		ginkgo.By("Waiting for IAM policy and WIF infrastructure propagation")
		time.Sleep(2 * time.Minute)

		// The pod continuously writes 10 MB chunks as separate GCS objects. Each chunk close
		// triggers a GCS upload which is IAM-checked. When permission is revoked the next
		// upload returns 403 and dd exits non-zero, stopping the loop.
		ginkgo.By("Creating and deploying test pod with continuous write loop")
		tPod := specs.NewTestPodModifiedSpec(f.ClientSet, f.Namespace, true)
		tPod.SetServiceAccount(wifKSA)
		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		if credentialConfigMapName != "" {
			tPod.SetAnnotations(map[string]string{
				webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: credentialConfigMapName,
			})
		}
		tPod.SetCommand(fmt.Sprintf(
			"i=0; while true; do "+
				"i=$((i+1)); "+
				"dd if=/dev/urandom bs=1M count=10 of=%s/chunk-$i.bin 2>&1; "+
				"if [ $? -ne 0 ]; then "+
				"echo WRITE_FAILED; "+
				"sleep 5; "+
				"fi; "+
				"done",
			mountPath,
		))
		tPod.SetRestartPolicy(corev1.RestartPolicyNever)
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Waiting for pod to reach Running state")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Polling until at least one chunk is written to GCS (confirms active writes before revocation)")
		preRevokeCtx, cancelPreRevoke := context.WithTimeout(ctx, 2*time.Minute)
		defer cancelPreRevoke()
		chunkWritten := false
		_ = wait.PollUntilContextCancel(preRevokeCtx, 5*time.Second, true, func(_ context.Context) (bool, error) {
			output := tPod.VerifyExecInPodSucceedWithOutput(f, specs.TesterContainerName,
				fmt.Sprintf("test -f %s/chunk-1.bin && echo WRITTEN || echo PENDING", mountPath))
			if strings.Contains(output, "WRITTEN") {
				chunkWritten = true
				return true, nil
			}
			return false, nil
		})
		gomega.Expect(chunkWritten).To(gomega.BeTrue(),
			"expected pod to write at least one chunk to GCS before permission revocation")

		ginkgo.By("Revoking bucket access while pod is actively writing")
		revokeBucketAccess(bucketName, principal, "roles/storage.objectUser")
		permissionRevoked = true

		ginkgo.By("Waiting for IAM revocation to propagate")
		time.Sleep(20 * time.Second)

		ginkgo.By("Waiting until writes stop progressing")

		var countStable bool

		_ = wait.PollUntilContextTimeout(ctx, 2*time.Minute, 10*time.Second, true,
			func(ctx context.Context) (bool, error) {
				out1 := tPod.VerifyExecInPodSucceedWithOutput(
					f, specs.TesterContainerName,
					fmt.Sprintf("ls %s/chunk-* 2>/dev/null | wc -l", mountPath),
				)

				time.Sleep(10 * time.Second)

				out2 := tPod.VerifyExecInPodSucceedWithOutput(
					f, specs.TesterContainerName,
					fmt.Sprintf("ls %s/chunk-* 2>/dev/null | wc -l", mountPath),
				)

				if strings.TrimSpace(out1) == strings.TrimSpace(out2) {
					countStable = true
					return true, nil
				}
				return false, nil
			},
		)

		gomega.Expect(countStable).To(gomega.BeTrue(),
			"expected writes to stop after permission revocation")

		ginkgo.By("Verifying GCS FUSE sidecar logs contain a 403 Forbidden error")
		sidecarLogReq := f.ClientSet.CoreV1().Pods(f.Namespace.Name).GetLogs(tPod.GetPodName(), &corev1.PodLogOptions{
			Container: webhook.GcsFuseSidecarName,
		})
		sidecarLogBytes, err := sidecarLogReq.DoRaw(ctx)
		framework.ExpectNoError(err, "fetching GCS FUSE sidecar logs")
		sidecarLogs := strings.ToLower(string(sidecarLogBytes))
		gomega.Expect(
			strings.Contains(sidecarLogs, "403") || strings.Contains(sidecarLogs, "forbidden"),
		).To(gomega.BeTrue(),
			"expected GCS FUSE sidecar logs to contain '403' or 'forbidden' after permission revocation;\nsidecar logs: %s", string(sidecarLogBytes))
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
