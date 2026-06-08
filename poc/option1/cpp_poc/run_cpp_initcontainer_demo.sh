#!/bin/bash
# Exit on error
set -e

# Color codes for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Get the directory of this script
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

echo -e "${GREEN}=== Starting C++ InitContainer Demo Setup ===${NC}"

# 1. Create Source ConfigMap
echo -e "${GREEN}1. Creating cpp-source-config ConfigMap...${NC}"
kubectl apply -f "$DIR/cpp-source-config.yaml"

# 2. Create ConfigMap
echo -e "${GREEN}2. Creating cpp-auth-config ConfigMap...${NC}"
kubectl create configmap cpp-auth-config --from-file=credential-configuration.json="$DIR/cpp-credential-configuration.json" -n default --dry-run=client -o yaml | kubectl apply -f -

# 3. Deploy Workload
echo -e "${GREEN}3. Deploying InitContainer Workload...${NC}"
kubectl apply -f "$DIR/demo-cpp-initcontainer.yaml"

echo "Waiting for workload pod to be created and sidecar to start (sleeping 60s for compilation)..."
sleep 60

# 4. Verify
echo -e "${GREEN}=== Verification ===${NC}"

echo -e "${GREEN}Checking Init Container logs (compilation status):${NC}"
kubectl logs -l app=gke-workload-with-gcsfuse-cpp-init -c copy-token-binary --tail=20 || true

echo ""
echo -e "${GREEN}Checking GCS Fuse sidecar logs:${NC}"
echo -e "${YELLOW}(Expect to see execution output from mock-client-static, proving it worked in distroless!)${NC}"
kubectl logs -l app=gke-workload-with-gcsfuse-cpp-init -c gke-gcsfuse-sidecar --tail=20 || true

echo ""
echo -e "${GREEN}=== Demo is running ===${NC}"
echo -e "Press ${YELLOW}[ENTER]${NC} to begin cleanup..."
read -r

# Cleanup
echo -e "${GREEN}=== Starting Cleanup ===${NC}"

echo -e "${GREEN}Deleting InitContainer Workload...${NC}"
kubectl delete deployment gke-workload-with-gcsfuse-cpp-init --ignore-not-found

echo -e "${GREEN}Deleting ConfigMaps...${NC}"
kubectl delete configmap cpp-auth-config --ignore-not-found
kubectl delete configmap cpp-source-config --ignore-not-found

echo -e "${GREEN}=== Cleanup Complete ===${NC}"
