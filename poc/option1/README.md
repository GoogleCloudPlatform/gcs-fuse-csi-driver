# Option 1 POC: Meta CLI in Sidecar

This directory contains the resources to run a Proof of Concept for integrating a custom token-fetching CLI into the GCS Fuse sidecar using Google Cloud Workload Identity Federation's executable-sourced credentials.

## Files

- `mock_cli.go`: Source code for the dummy CLI that returns a mock token.
- `option1_poc.yaml`: Kubernetes manifest containing ConfigMaps for the code and credential config, and a Pod that builds and runs it.

## How to Run

1. Apply the manifest to your cluster:
   ```bash
   kubectl apply -f option1_poc.yaml
   ```

2. Verify the pod is running:
   ```bash
   kubectl get pod gcsfuse-poc-option1
   ```

3. Check the logs of the `gcsfuse-sidecar` container to see the authentication attempt:
   ```bash
   kubectl logs gcsfuse-poc-option1 -c gcsfuse-sidecar
   ```

Note: The authentication will fail with `invalid_target` because it uses dummy values for the project and workload identity pool. This is expected and proves the mechanism works.
