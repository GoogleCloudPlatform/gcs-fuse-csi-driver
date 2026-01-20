#!/bin/bash
# Shortcuts for viewing GCS FUSE CSI Driver logs
# Usage: source shortcuts.sh or add to ~/.bashrc

# Set default namespace
alias csi='kubectl config set-context --current --namespace=gcs-fuse-csi-driver'
alias test='kubectl config set-context --current --namespace=gcs-csi-test'

# Get current namespace
alias ns='kubectl config view --minify --output "jsonpath={..namespace}" && echo'

# Pod listings
alias pods='kubectl get pods'
alias podw='kubectl get pods -w'

# CSI Driver logs
alias csi-logs='kubectl logs daemonset/gcsfusecsi-node -c gcs-fuse-csi-driver --tail=100'
alias csi-logs-f='kubectl logs daemonset/gcsfusecsi-node -c gcs-fuse-csi-driver -f'
alias csi-custom='kubectl logs daemonset/gcsfusecsi-node -c gcs-fuse-csi-driver --tail=200 | grep "CUSTOM BUILD"'

# Webhook logs
alias webhook-logs='kubectl logs deployment/gcs-fuse-csi-driver-webhook --tail=100'
alias webhook-logs-f='kubectl logs deployment/gcs-fuse-csi-driver-webhook -f'
alias webhook-custom='kubectl logs deployment/gcs-fuse-csi-driver-webhook --tail=200 | grep "CUSTOM BUILD"'

# Sidecar logs (assumes pod name gcs-csi-test-pod)
alias sidecar-logs='kubectl logs gcs-csi-test-pod -c gke-gcsfuse-sidecar --tail=100'
alias sidecar-logs-f='kubectl logs gcs-csi-test-pod -c gke-gcsfuse-sidecar -f'
alias sidecar-custom='kubectl logs gcs-csi-test-pod -c gke-gcsfuse-sidecar --tail=200 | grep "CUSTOM BUILD"'

# Test pod logs
alias test-logs='kubectl logs gcs-csi-test-pod -c test-container'
alias test-logs-f='kubectl logs gcs-csi-test-pod -c test-container -f'

# Describe resources
alias desc-pod='kubectl describe pod'
alias desc-webhook='kubectl describe deployment gcs-fuse-csi-driver-webhook'
alias desc-csi='kubectl describe daemonset gcsfusecsi-node'

# Check all custom builds
alias check-custom='echo "=== CSI Driver ===" && kubectl logs daemonset/gcsfusecsi-node -c gcs-fuse-csi-driver -n gcs-fuse-csi-driver --tail=100 | grep "CUSTOM BUILD" | head -2 && echo -e "\n=== Webhook ===" && kubectl logs deployment/gcs-fuse-csi-driver-webhook -n gcs-fuse-csi-driver --tail=100 | grep "CUSTOM BUILD" | head -2 && echo -e "\n=== Sidecar ===" && kubectl logs gcs-csi-test-pod -c gke-gcsfuse-sidecar -n gcs-csi-test --tail=100 2>/dev/null | grep "CUSTOM BUILD" | head -2 || echo "No sidecar pod found"'

# Quick commands
alias rebuild='cd /home/princer_google_com/dev/gcs-fuse-csi-driver && export PROJECT_ID=gcs-tess REGISTRY=gcr.io/gcs-tess/princer STAGINGVERSION=v999.999.999 BUILD_GCSFUSE_FROM_SOURCE=true && make build-image-and-push-multi-arch REGISTRY=$REGISTRY STAGINGVERSION=$STAGINGVERSION'
alias reinstall='cd /home/princer_google_com/dev/gcs-fuse-csi-driver && export REGISTRY=gcr.io/gcs-tess/princer STAGINGVERSION=v999.999.999 && make uninstall && make install'
alias update-csi='cd /home/princer_google_com/dev/gcs-fuse-csi-driver && export REGISTRY=gcr.io/gcs-tess/princer STAGINGVERSION=v999.999.999 && kubectl set image daemonset/gcsfusecsi-node gcs-fuse-csi-driver=$REGISTRY/gcs-fuse-csi-driver:$STAGINGVERSION -n gcs-fuse-csi-driver && kubectl set image deployment/gcs-fuse-csi-driver-webhook gcs-fuse-csi-driver-webhook=$REGISTRY/gcs-fuse-csi-driver-webhook:$STAGINGVERSION -n gcs-fuse-csi-driver'
alias recreate-pod='cd /home/princer_google_com/dev/gcs-fuse-csi-driver && ./setup-test-pod.sh --skip-setup'

echo "✅ GCS FUSE CSI shortcuts loaded!"
echo ""
echo "Namespace shortcuts:"
echo "  csi       - Switch to gcs-fuse-csi-driver namespace"
echo "  test      - Switch to gcs-csi-test namespace"
echo "  ns        - Show current namespace"
echo ""
echo "Log shortcuts:"
echo "  csi-logs, csi-logs-f, csi-custom"
echo "  webhook-logs, webhook-logs-f, webhook-custom"
echo "  sidecar-logs, sidecar-logs-f, sidecar-custom"
echo "  test-logs, test-logs-f"
echo "  check-custom - Check all components for custom build"
echo ""
echo "Quick commands:"
echo "  rebuild      - Rebuild and push all images"
echo "  reinstall    - Uninstall and reinstall CSI driver"
echo "  update-csi   - Update existing deployment with new images"
echo "  recreate-pod - Recreate test pod"
echo ""
echo "Other:"
echo "  pods, podw, desc-pod, desc-webhook, desc-csi"
