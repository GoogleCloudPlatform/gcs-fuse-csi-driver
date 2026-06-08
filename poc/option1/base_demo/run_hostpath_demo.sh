#!/bin/bash
# Exit on error
set -e

# Color codes for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Get the directory of this script
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

echo -e "${GREEN}=== Starting HostPath Demo Setup ===${NC}"

# 1. Deploy Mock Token Service
echo -e "${GREEN}1. Deploying Mock Token Service...${NC}"
kubectl apply -f "$DIR/mock-service-deployment.yaml"

# 2. Create ConfigMap
echo -e "${GREEN}2. Creating mock-auth-config ConfigMap...${NC}"
kubectl create configmap mock-auth-config --from-file=credential-configuration.json="$DIR/mock-credential-configuration.json" -n default --dry-run=client -o yaml | kubectl apply -f -

# 3. Install binary on host via DaemonSet
echo -e "${GREEN}3. Deploying DaemonSet to install binary on host...${NC}"
kubectl apply -f "$DIR/install-hostpath-binary.yaml"

echo "Waiting for DaemonSet to copy binary (sleeping 10s)..."
sleep 10

# 4. Deploy Workload
echo -e "${GREEN}4. Deploying HostPath Workload...${NC}"
kubectl apply -f "$DIR/hostpath-deployment.yaml"

echo "Waiting for workload pod to be created and sidecar to start (sleeping 15s)..."
sleep 15

# 5. Verify
echo -e "${GREEN}=== Verification ===${NC}"
echo -e "${GREEN}Checking mock service logs:${NC}"
kubectl logs -l app=mock-token-service --tail=10 || true

echo ""
echo -e "${GREEN}Checking GCS Fuse sidecar logs:${NC}"
echo -e "${YELLOW}(Expect to see 'ACCESS_TOKEN_TYPE_UNSUPPORTED' error, which proves it executed the binary and used the mock token!)${NC}"
kubectl logs -l app=gke-workload-with-gcsfuse-hostpath -c gke-gcsfuse-sidecar --tail=20 || true

echo ""
echo -e "${GREEN}=== Demo is running ===${NC}"
echo -e "Press ${YELLOW}[ENTER]${NC} to begin cleanup..."
read -r

# Cleanup
echo -e "${GREEN}=== Starting Cleanup ===${NC}"

echo -e "${GREEN}Deleting HostPath Workload...${NC}"
kubectl delete deployment gke-workload-with-gcsfuse-hostpath --ignore-not-found

echo -e "${GREEN}Deleting DaemonSet...${NC}"
kubectl delete daemonset install-fetch-token-on-host --ignore-not-found

echo -e "${GREEN}Deleting ConfigMap...${NC}"
kubectl delete configmap mock-auth-config --ignore-not-found

echo -e "${GREEN}Deleting Mock Token Service...${NC}"
kubectl delete deployment mock-token-service --ignore-not-found
kubectl delete service mock-token-service --ignore-not-found

echo -e "${GREEN}=== Cleanup Complete ===${NC}"
