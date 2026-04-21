# Option 2 POC: Token Server Sidecar

This directory contains the resources to run a Proof of Concept for using a customer-managed token server sidecar with GCS Fuse, communicating over a Unix Domain Socket.

## Files

- `token_server.py`: Python script for the mock token server.
- `option2_poc.yaml`: Kubernetes manifest containing a ConfigMap for the script and a Pod that runs both the token server and the GCS Fuse sidecar.

## How to Run

1. Apply the manifest to your cluster:
   ```bash
   kubectl apply -f option2_poc.yaml
   ```

2. Verify the pod is running:
   ```bash
   kubectl get pod gcsfuse-poc-option2
   ```

3. Check the logs of the `gcsfuse-sidecar` container to see if it connects to the socket and fetches the token:
   ```bash
   kubectl logs gcsfuse-poc-option2 -c gcsfuse-sidecar
   ```

4. Check the logs of the `token-server` container to see if it receives requests:
   ```bash
   kubectl logs gcsfuse-poc-option2 -c token-server
   ```
