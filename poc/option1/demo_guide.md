# Demo Guide: GCS Fuse Integration with Custom Token Fetcher (Option 1)

This guide outlines the steps, walkthrough, and talking script for demonstrating the Executable-Sourced Credentials flow for the GCS Fuse integration.

## Goal
Demonstrate how GCS Fuse can use a custom binary to fetch authentication tokens, avoiding the need for a sidecar container at this stage.

---

## Part 1: Walkthrough of Prepared Assets

In this part, you will show the audience the files we have prepared.

### 1. The Token Fetcher Go Source
Show the file [fetch_token.go](file:///usr/local/google/home/yangspirit/Work/gcs-fuse-csi-driver/poc/option1/fetch_token.go).
*   **What to highlight**: Point out that it uses mTLS to connect to the internal token service (`https://10.128.0.2:5000/mint-cat`) and returns a SAML token in the format expected by Google Workload Identity Federation.

### 2. The Compiled Binary
Show that we have compiled this file into a statically linked binary named `fetch-token` in `poc/option1/`.
*   **What to highlight**: Explain that it must be statically linked because the GCS Fuse sidecar image is distroless and lacks shared libraries or a package manager.

### 3. The Credential Configuration
Show the file [credential-configuration.json](file:///usr/local/google/home/yangspirit/Work/gcs-fuse-csi-driver/poc/option1/credential-configuration.json).
*   **What to highlight**: Show the `credential_source` section where it specifies `executable` and points to `/scripts/fetch-token`. This tells the Google Auth library to run our binary to get the token.

### 4. The Deployment Manifest
Show the file [deployment.yaml](file:///usr/local/google/home/yangspirit/Work/gcs-fuse-csi-driver/poc/option1/deployment.yaml).
*   **What to highlight**:
    *   The annotations that trigger the CSI driver webhook to inject the GCS Fuse sidecar and mount additional volumes.
    *   The `initContainers` section that copies the binary from the custom image to a shared volume.
    *   The `volumes` section defining the shared `emptyDir` volume.

---

## Part 2: Execution Steps for the Demo

*Note: These steps assume you are in a connected environment with access to a GKE cluster and the container registry. If this is a dry-run or simulated demo, you will just explain these steps.*

### Step 1: Create the ConfigMap
We need to store the `credential-configuration.json` in a ConfigMap so the CSI driver can mount it.

```bash
kubectl create configmap custom-auth-config --from-file=credential-configuration.json=poc/option1/credential-configuration.json
```

### Step 2: Package and Push the Binary
The user needs to create a container image containing the `fetch-token` binary at `/usr/bin/fetch-token` and push it to their registry.

### Step 3: Deploy the Workload
Apply the deployment manifest.

```bash
kubectl apply -f poc/option1/deployment.yaml
```

### Step 4: Verify Sidecar Injection
Check if the GCS Fuse sidecar was successfully injected into the Pod.

```bash
kubectl get pods -l app=gke-workload-with-gcsfuse -o jsonpath='{.items[0].spec.containers[*].name}'
```
*Expected output should include `gke-gcsfuse-sidecar`.*

### Step 5: Verify Mount Success
Check the logs of the GCS Fuse sidecar to ensure it started and mounted the bucket successfully.

```bash
kubectl logs -l app=gke-workload-with-gcsfuse -c gke-gcsfuse-sidecar
```
*Look for successful mount messages.*

---

## Part 3: Talking Script

**[Slide/Screen: Architecture Overview or showing `fetch_token.go`]**

"Hello everyone. Today I'm going to demonstrate the solution we've developed to unblock the V0 deployment of GCS Fuse.

The user has a unique security requirement: they use custom authentication mechanisms and encryption proxies. To support this without introducing a complex sidecar container right now, we are using **Option 1: Executable-Sourced Credentials**."

**[Action: Show `fetch_token.go`]**

"Here is the Go source code for the token fetcher. This code will be provided to the user. It connects to the internal token service via mTLS and returns a SAML response.

Because our GCS Fuse sidecar uses a secure, distroless image, this code must be compiled into a fully statically linked binary. We've done that here."

**[Action: Show `credential-configuration.json`]**

"Now, how do we tell GCS Fuse to use this binary? We use this `credential-configuration.json` file. Notice the `credential_source` block. Instead of a standard service account key or metadata server, it specifies an `executable` command: `/scripts/fetch-token`.

When GCS Fuse needs to authenticate, the Google Auth library inside it will execute this binary to get the token."

**[Action: Show `deployment.yaml`]**

"Finally, let's look at how this is deployed. We are using the **InitContainer pattern** to share the binary securely.

1.  The user packages their binary into a custom image.
2.  An `initContainer` runs first, mounting a shared `emptyDir` volume, and copies the binary into it.
3.  We use these specific annotations to tell our CSI driver webhook to inject the GCS Fuse sidecar AND mount that same shared volume into it.

This allows the distroless sidecar to access and execute the binary without insecure host mounts.

This solution preserves native audit lineage and unblocks the V0 deployment while we work on longer-term protocol compatibility initiatives."

---

## Part 4: Mock Demo (Self-Contained)

If you want to run a self-contained demo without access to the actual services, use the following assets we have prepared:

1.  **Mock Service**: [mock_service.go](file:///usr/local/google/home/yangspirit/Work/gcs-fuse-csi-driver/poc/option1/mock_service.go) (and compiled `mock-service`). Now includes `/sts-token` endpoint to simulate Google STS.
2.  **Mock Service Manifest**: [mock-service-deployment.yaml](file:///usr/local/google/home/yangspirit/Work/gcs-fuse-csi-driver/poc/option1/mock-service-deployment.yaml) runs the mock service as a separate Deployment and Service.
3.  **Credential Configuration**: [mock-credential-configuration.json](file:///usr/local/google/home/yangspirit/Work/gcs-fuse-csi-driver/poc/option1/mock-credential-configuration.json) points directly to the binary `/scripts/fetch-token`.
4.  **Demo Deployment**: [demo-deployment.yaml](file:///usr/local/google/home/yangspirit/Work/gcs-fuse-csi-driver/poc/option1/demo-deployment.yaml) copies the binary in the `initContainer` and uses `gke-gcsfuse/inject-after: "copy-token-binary"` annotation to ensure correct execution order.

### Steps for Mock Demo:
1.  **Deploy the Mock Service**:
    ```bash
    kubectl apply -f poc/option1/mock-service-deployment.yaml
    ```
2.  **Create the ConfigMap**:
    ```bash
    kubectl create configmap mock-auth-config --from-file=credential-configuration.json=poc/option1/mock-credential-configuration.json -n default --dry-run=client -o yaml | kubectl apply -f -
    ```
3.  **Apply the Demo Deployment**:
    ```bash
    kubectl apply -f poc/option1/demo-deployment.yaml
    ```
4.  **Verify**:
    *   Check mock service logs to see token requests: `kubectl logs -l app=mock-token-service`
    *   Check sidecar logs to see GCS authentication attempt: `kubectl logs -l app=gke-workload-with-gcsfuse-demo -c gke-gcsfuse-sidecar`
    *   Expect to see `ACCESS_TOKEN_TYPE_UNSUPPORTED` error from GCS, proving that the sidecar successfully executed the binary and used the mock token!

---

## Part 5: HostPath Alternative

If the user prefers not to package their binary in a container image and instead use a binary dropped on the host by their DaemonSet, you can demonstrate this alternative.

### Key Differences:
- No `initContainers` needed.
- Uses `hostPath` volume instead of `emptyDir`.
- Requires the binary to be statically linked and present at the specified path on all nodes.

### Manifest:
Show [hostpath-deployment.yaml](file:///usr/local/google/home/yangspirit/Work/gcs-fuse-csi-driver/poc/option1/hostpath-deployment.yaml).

### Steps for HostPath Demo:
1.  **Install the binary on host nodes** (Simulation):
    Apply the helper DaemonSet to copy the binary from our image to the host path:
    ```bash
    kubectl apply -f poc/option1/install-hostpath-binary.yaml
    ```
2.  **Apply the HostPath Demo Deployment**:
    ```bash
    kubectl apply -f poc/option1/hostpath-deployment.yaml
    ```
3.  **Verify**:
    Check sidecar logs to see GCS authentication attempt:
    ```bash
    kubectl logs -l app=gke-workload-with-gcsfuse-hostpath -c gke-gcsfuse-sidecar
    ```
    Expect to see the same `ACCESS_TOKEN_TYPE_UNSUPPORTED` error, proving it executed the binary from the host!

### Talking Script Addition:
"We also have an alternative for environments where packaging the binary in a container is not preferred. If the user uses a DaemonSet to distribute the binary to all nodes, we can mount it directly using a `hostPath` volume. This avoids the initContainer step but requires the binary to be present on the host. We have a helper DaemonSet to simulate this for the demo."
