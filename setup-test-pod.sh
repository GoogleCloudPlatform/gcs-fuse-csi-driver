#!/bin/bash
# Setup script for testing custom GCS FUSE CSI Driver

set -e

# Parse flags
SKIP_SETUP=false
if [[ "$1" == "--skip-setup" ]] || [[ "$1" == "-s" ]]; then
  SKIP_SETUP=true
fi

PROJECT_ID="gcs-tess"
PROJECT_NUMBER="222564316065"
NAMESPACE="gcs-csi-test"
# BUCKET_NAME="gcs-fuse-warp-test-bucket"
BUCKET_NAME="princer-zonal-us-west4-a"
KSA_NAME="gcs-csi-test-sa"

echo "=== Setting up GCS FUSE CSI Driver Test ==="
echo "Project: $PROJECT_ID"
echo "Project Number: $PROJECT_NUMBER"
echo "Namespace: $NAMESPACE"
echo "Bucket: $BUCKET_NAME"

if [[ "$SKIP_SETUP" == "false" ]]; then
  # Create GCS bucket
  echo -e "\n1. Creating GCS bucket..."
  gsutil mb -p $PROJECT_ID -l us-central1 gs://$BUCKET_NAME || echo "Bucket may already exist"

  # Create namespace
  echo -e "\n2. Creating Kubernetes namespace..."
  kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

  # Create Kubernetes Service Account
  echo -e "\n3. Creating Kubernetes Service Account..."
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: $KSA_NAME
  namespace: $NAMESPACE
EOF

  # Grant direct bucket access via Workload Identity principal
  echo -e "\n4. Granting bucket access to Kubernetes service account..."
  PRINCIPAL="principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject/ns/${NAMESPACE}/sa/${KSA_NAME}"
  echo "   Principal: $PRINCIPAL"
  gcloud storage buckets add-iam-policy-binding gs://$BUCKET_NAME \
    --member="$PRINCIPAL" \
    --role="roles/storage.objectAdmin"
else
  echo -e "\n⏭️  Skipping setup (--skip-setup flag detected)"
fi

# Create test pod
echo -e "\n5. Creating test pod..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: gcs-csi-test-pod
  namespace: $NAMESPACE
  annotations:
    gke-gcsfuse/volumes: "true"
spec:
  containers:
  - name: test-container
    image: busybox
    command:
      - "/bin/sh"
      - "-c"
      - |
        echo "=== Testing GCS FUSE CSI Driver with Custom Build ==="
        echo "Bucket: $BUCKET_NAME"
        echo "Time: \$(date)"
        echo ""
        echo "Listing /data contents:"
        ls -la /data/
        echo ""
        echo "Reading back the file:"
        cat /data/1m/test.0.0 > /dev/null
        echo ""
        echo "✓ Test successful! Pod will sleep now..."
        sleep 3600
    volumeMounts:
    - name: gcs-volume
      mountPath: /data
    resources:
      limits:
        cpu: 100m
        memory: 128Mi
      requests:
        cpu: 50m
        memory: 64Mi
  serviceAccountName: $KSA_NAME
  tolerations:
  - key: sandbox.gke.io/runtime
    operator: Equal
    value: gvisor
    effect: NoSchedule
  volumes:
  - name: gcs-volume
    csi:
      driver: gcsfuse.csi.storage.gke.io
      volumeAttributes:
        bucketName: $BUCKET_NAME
EOF

echo -e "\n=== Setup Complete! ==="
echo ""
echo "Monitor pod creation:"
echo "  kubectl get pods -n $NAMESPACE -w"
echo ""
echo "View pod logs:"
echo "  kubectl logs -n $NAMESPACE gcs-csi-test-pod -f"
echo ""
echo "View sidecar logs (to see CUSTOM BUILD message):"
echo "  kubectl logs -n $NAMESPACE gcs-csi-test-pod -c gke-gcsfuse-sidecar"
echo ""
echo "Check CSI driver logs:"
echo "  kubectl logs -n gcs-fuse-csi-driver daemonset/gcsfusecsi-node -c gcs-fuse-csi-driver | grep -i 'CUSTOM BUILD\\|$NAMESPACE\\|$BUCKET_NAME'"
echo ""
echo "Cleanup when done:"
echo "  kubectl delete namespace $NAMESPACE"
echo "  gsutil rm -r gs://$BUCKET_NAME"
