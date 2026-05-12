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
	// WIF principal string and credential config. Cleanup is registered via ginkgo.DeferCleanup.
	setupOSSWIFPrincipal := func(ksaName, poolID, providerID, configMapName string) (string, string) {
		projectID := os.Getenv(utils.ProjectEnvVar)
		gomega.Expect(projectID).NotTo(gomega.BeEmpty(), fmt.Sprintf("%s environment variable must be set", utils.ProjectEnvVar))

		ginkgo.By("Getting GCP project number")
		projectNumber := getProjectNumber(projectID)
		gomega.Expect(projectNumber).NotTo(gomega.BeEmpty(), "failed to get project number")

		ginkgo.By(fmt.Sprintf("Creating workload identity pool: %s", poolID))
		createWorkloadIdentityPool(projectID, poolID)

		clusterName := os.Getenv(utils.ClusterNameEnvVar)
		clusterLocation := os.Getenv(utils.ClusterLocationEnvVar)
		clusterIssuer := getClusterOIDCIssuer(clusterName, clusterLocation, projectID)
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

		principal := fmt.Sprintf(
			"principal://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/subject/system:serviceaccount:%s:%s",
			projectNumber, poolID, f.Namespace.Name, ksaName,
		)
		return principal, credentialConfig
	}

	// setupGKEWIPrincipal creates a GCP service account, binds it to ksaName via
	// roles/iam.workloadIdentityUser, and creates the annotated KSA. Returns the
	// GSA principal string. Cleanup is registered via ginkgo.DeferCleanup.
	setupGKEWIPrincipal := func(ksaName string) string {
		rawProjectID := os.Getenv(utils.ProjectEnvVar)
		gomega.Expect(rawProjectID).NotTo(gomega.BeEmpty(), "PROJECT must be set")

		// Strip any Cloud Shell "Your active configuration is: [...]" warning prefix.
		lines := strings.Split(strings.TrimSpace(rawProjectID), "\n")
		projectID := lines[len(lines)-1]

		gomega.Expect(strings.Contains(projectID, "Your active configuration")).To(gomega.BeFalse(),
			fmt.Sprintf("invalid projectID detected: %q", projectID))

		// Append the namespace numeric suffix to make the GCP SA name unique per test run,
		// preventing 409 conflicts when a previous run's SA was not cleaned up.
		nsIdx := strings.LastIndex(f.Namespace.Name, "-")
		nsSuffix := f.Namespace.Name[nsIdx+1:]
		saName := fmt.Sprintf("%s-%s", ksaName, nsSuffix)
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
			var credentialConfig string
			ns1Principal, credentialConfig = setupOSSWIFPrincipal(sharedKSAName, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, credentialConfigMapName)

			ginkgo.By(fmt.Sprintf("Creating Kubernetes service account %s in ns-2 (%s)", sharedKSAName, ns2.Name))
			createServiceAccount(ctx, f, sharedKSAName, ns2.Name)
			ginkgo.DeferCleanup(func() { deleteServiceAccount(ctx, f, sharedKSAName, ns2.Name) })

			ginkgo.By(fmt.Sprintf("Creating credential ConfigMap %s in ns-2 (%s) — same WIF pool/provider, distinct subject", credentialConfigMapName, ns2.Name))
			createCredentialConfigMap(ctx, f, credentialConfigMapName, credentialConfig, ns2.Name)
			ginkgo.DeferCleanup(func() { deleteConfigMap(ctx, f, credentialConfigMapName, ns2.Name) })
		} else {
			// GKE: both KSAs are annotated with dedicated GSAs and have roles/iam.workloadIdentityUser
			// bindings, preventing any fallback to the node's default service account.
			// Only ns-1's GSA receives a GCS bucket IAM binding.
			// ns-2's GSA is created first so both WI bindings propagate during the
			// same 2-minute window inside setupGKEWIPrincipal.
			projectID := os.Getenv(utils.ProjectEnvVar)
			gomega.Expect(projectID).NotTo(gomega.BeEmpty(), fmt.Sprintf("%s environment variable must be set", utils.ProjectEnvVar))

			ns2SAName := ns2.Name
			if len(ns2SAName) > 30 {
				ns2SAName = ns2SAName[:30]
			}
			testGcpSA2 := utils.NewTestGCPServiceAccount(ns2SAName, projectID)
			ginkgo.By(fmt.Sprintf("Creating GCP service account for ns-2 (no bucket access): %s", ns2SAName))
			testGcpSA2.Create(ctx)
			ginkgo.DeferCleanup(func() { testGcpSA2.Cleanup(ctx) })

			ginkgo.By(fmt.Sprintf("Binding KSA %s in ns-2 to GCP service account %s with roles/iam.workloadIdentityUser", sharedKSAName, testGcpSA2.GetEmail()))
			addWorkloadIdentityBinding(ctx, testGcpSA2.GetEmail(), projectID, ns2.Name, sharedKSAName)

			ginkgo.By(fmt.Sprintf("Creating Kubernetes service account %s in ns-2 annotated with GCP service account %s", sharedKSAName, testGcpSA2.GetEmail()))
			testK8sSA2 := utils.NewTestKubernetesServiceAccount(f.ClientSet, ns2, sharedKSAName, testGcpSA2.GetEmail())
			testK8sSA2.Create(ctx)
			ginkgo.DeferCleanup(func() { testK8sSA2.Cleanup(ctx) })

			// setupGKEWIPrincipal creates ns-1's GSA + WI binding + KSA, then waits 2 minutes
			// for both ns-1 and ns-2 WI bindings to propagate globally.
			ns1Principal = setupGKEWIPrincipal(sharedKSAName)
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

		// Deploy unauthorized pod in ns-2. It runs a continuous write loop so that GCS FUSE
		// makes repeated upload attempts, ensuring the 403 error surfaces in sidecar logs
		// even under IAM propagation delays.
		ginkgo.By("Deploying unauthorized pod in ns-2 with same KSA name but no GCS access")
		tPodNs2 := specs.NewTestPodModifiedSpec(f.ClientSet, ns2, true)
		tPodNs2.SetServiceAccount(sharedKSAName)
		tPodNs2.SetupVolume(l.volumeResource, volumeName, mountPath, false)
		if !isOSS {
			// GKE: the CSI node driver's pre-mount check uses the pod's WI credentials.
			// ns-2 KSA has no bucket access, so without this skip the check raises a
			// FailedMount event before the sidecar ever starts, making logs unavailable.
			// We skip only the CSI check here; the sidecar's own bucket access check still
			// runs and surfaces the 403, which is what the assertion below validates.
			// Deep-copy the CSI spec to avoid modifying ns-1's shared volume source.
			vols := tPodNs2.GetPodVols()
			for i, vol := range vols {
				if vol.Name == volumeName && vol.VolumeSource.CSI != nil {
					csiCopy := *vol.VolumeSource.CSI
					attrsCopy := make(map[string]string, len(csiCopy.VolumeAttributes)+1)
					for k, v := range csiCopy.VolumeAttributes {
						attrsCopy[k] = v
					}
					attrsCopy["skipCSIBucketAccessCheck"] = "true"
					csiCopy.VolumeAttributes = attrsCopy
					vols[i].VolumeSource.CSI = &csiCopy
					break
				}
			}
		}
		if isOSS {
			tPodNs2.SetAnnotations(map[string]string{
				webhook.GCPWorkloadIdentityCredentialConfigMapAnnotation: credentialConfigMapName,
			})
		}
		tPodNs2.SetCommand(fmt.Sprintf(
			"i=0; while true; do "+
				"i=$((i+1)); "+
				"dd if=/dev/urandom bs=1M count=1 of=%s/ns2-chunk-$i.bin 2>&1; "+
				"sleep 3; "+
				"done",
			mountPath,
		))
		tPodNs2.SetRestartPolicy(corev1.RestartPolicyNever)
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

		// In GKE, the sidecar's own bucket access check surfaces the 403 before the main
		// container ever starts, so the ns-2 pod may never reach Running. Logs are still
		// available from the sidecar container as soon as it starts; the polling loop
		// retries on fetch errors until the container is ready or the timeout expires.
		ginkgo.By("Polling ns-2 GCS FUSE sidecar logs for 403 Forbidden (confirms IAM denial, not a mount issue)")
		var ns2AccessDenied bool
		err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 3*time.Minute, true,
			func(pollCtx context.Context) (bool, error) {
				sidecarLogReq := f.ClientSet.CoreV1().Pods(ns2.Name).GetLogs(tPodNs2.GetPodName(), &corev1.PodLogOptions{
					Container: webhook.GcsFuseSidecarName,
				})
				logBytes, fetchErr := sidecarLogReq.DoRaw(pollCtx)
				if fetchErr != nil {
					klog.Warningf("failed to fetch ns-2 sidecar logs: %v — retrying", fetchErr)
					return false, nil
				}
				logLower := strings.ToLower(string(logBytes))
				if strings.Contains(logLower, "403") || strings.Contains(logLower, "forbidden") ||
					strings.Contains(logLower, "permission denied") || strings.Contains(logLower, "permissiondenied") {
					ns2AccessDenied = true
					return true, nil
				}
				return false, nil
			},
		)
		framework.ExpectNoError(err, "polling ns-2 sidecar logs for access denial indicator")
		gomega.Expect(ns2AccessDenied).To(gomega.BeTrue(),
			"expected ns-2 GCS FUSE sidecar logs to contain an access-denial indicator "+
				"('403', 'forbidden', 'permission denied', or 'permissiondenied'), "+
				"confirming the unauthorized identity was correctly denied GCS access;\n"+
				"ns-2 pod: %s/%s", ns2.Name, tPodNs2.GetPodName())
	})

	ginkgo.It("should enforce different GCS bucket permissions for different Kubernetes service accounts", func() {
		init(specs.SkipCSIBucketAccessCheckPrefix)
		defer cleanup()

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]
		gomega.Expect(bucketName).NotTo(gomega.BeEmpty(), "bucketName must be set in volume attributes")

		const (
			ksaReader     = "wif-reader-ksa"
			ksaReadWriter = "wif-readwriter-ksa"
			ksaNoAccess   = "wif-noaccess-ksa"
			mountPath     = "/mnt/gcs"
			testFileName  = "readwriter-write-test.txt"
		)

		isOSS := os.Getenv(utils.IsOSSEnvVar) == "true"

		var (
			readerPrincipal     string
			readWriterPrincipal string
			readerCredMap       string
			readWriterCredMap   string
			noAccessCredMap     string
		)

		if isOSS {
			readerCredMap = "wif-reader-credentials"
			readWriterCredMap = "wif-readwriter-credentials"
			noAccessCredMap = "wif-noaccess-credentials"

			readerPrincipal, _ = setupOSSWIFPrincipal(ksaReader, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, readerCredMap)
			readWriterPrincipal, _ = setupOSSWIFPrincipal(ksaReadWriter, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, readWriterCredMap)
			// noAccess KSA gets a valid WIF identity but intentionally no IAM binding on the bucket.
			_, _ = setupOSSWIFPrincipal(ksaNoAccess, wifWorkloadIdentityPoolID, wifWorkloadIdentityProviderID, noAccessCredMap)
		} else {
			readerPrincipal = setupGKEWIPrincipal(ksaReader)
			readWriterPrincipal = setupGKEWIPrincipal(ksaReadWriter)
			setupGKEWIPrincipal(ksaNoAccess)
		}

		ginkgo.By("Granting objectViewer to reader KSA and objectAdmin to read-writer KSA; no binding for no-access KSA")
		grantBucketAccess(bucketName, readerPrincipal, "roles/storage.objectViewer")
		defer revokeBucketAccess(bucketName, readerPrincipal, "roles/storage.objectViewer")
		grantBucketAccess(bucketName, readWriterPrincipal, "roles/storage.objectAdmin")
		defer revokeBucketAccess(bucketName, readWriterPrincipal, "roles/storage.objectAdmin")

		ginkgo.By("Waiting for IAM policy propagation")
		time.Sleep(2 * time.Minute)

		// --- Reader pod: objectViewer — read must pass, write must be denied by GCS ---
		ginkgo.By("Deploying reader pod (objectViewer)")
		readerPod := deployWIFPod(ksaReader, readerCredMap, "gcs-volume-reader", mountPath)
		readerPod.WaitForRunning(ctx)

		ginkgo.By("Verifying reader KSA can list objects in the bucket")
		readerPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("ls %s", mountPath))

		ginkgo.By("Verifying reader KSA cannot write to the bucket (objectViewer denies object creation)")
		readerPod.VerifyExecInPodFail(f, specs.TesterContainerName,
			fmt.Sprintf("touch %s/%s", mountPath, testFileName), 1)
		readerPod.Cleanup(ctx)

		// --- ReadWriter pod: objectAdmin — both read and write must pass ---
		ginkgo.By("Deploying read-writer pod (objectAdmin)")
		readWriterPod := deployWIFPod(ksaReadWriter, readWriterCredMap, "gcs-volume-readwriter", mountPath)
		readWriterPod.WaitForRunning(ctx)

		ginkgo.By("Verifying read-writer KSA can list objects in the bucket")
		readWriterPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("ls %s", mountPath))

		ginkgo.By("Verifying read-writer KSA can write a file to the bucket")
		readWriterPod.VerifyExecInPodSucceed(f, specs.TesterContainerName,
			fmt.Sprintf("touch %s/%s", mountPath, testFileName))
		readWriterPod.Cleanup(ctx)

		// --- NoAccess pod: no IAM binding — GCS FUSE mount fails with gRPC PermissionDenied ---
		ginkgo.By("Deploying no-access pod (no bucket IAM binding)")
		noAccessPod := deployWIFPod(ksaNoAccess, noAccessCredMap, "gcs-volume-noaccess", mountPath)
		defer noAccessPod.Cleanup(ctx)

		ginkgo.By("Verifying no-access KSA cannot mount the bucket (expects FailedMount event with PermissionDenied)")
		noAccessPod.WaitForFailedMountError(ctx, "PermissionDenied")
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
