#!/bin/bash
# Exit on error
set -e

# Color codes for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Get the directory of this script
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

IMAGE="gcr.io/yangspirit-joonix/cpp-poc-installer:latest"

echo -e "${GREEN}=== Starting Full C++ HostPath Demo Setup ===${NC}"

# 1. Build artifacts via Docker
echo -e "${GREEN}1. Building static artifacts via Docker (rust:alpine)...${NC}"
docker run --rm -v "$DIR":/src -w /src rust:alpine sh -c "apk add --no-cache g++ && g++ -c -fPIC mock_crypto.cpp -o /tmp/mock_crypto.o && ar rcs /tmp/libmockcrypto.a /tmp/mock_crypto.o && rustc -L /tmp -l static=mockcrypto -l stdc++ main.rs -o mock-client-static"

# 2. Build Docker Image
echo -e "${GREEN}2. Building Docker image $IMAGE...${NC}"
docker build -t "$IMAGE" "$DIR"

# 3. Push Docker Image
echo -e "${GREEN}3. Pushing Docker image $IMAGE...${NC}"
echo -e "${YELLOW}Assumes you are authenticated to the registry (e.g., gcloud auth configure-docker)${NC}"
docker push "$IMAGE"

# 4. Create ConfigMap
echo -e "${GREEN}4. Creating cpp-auth-config ConfigMap...${NC}"
kubectl create configmap cpp-auth-config --from-file=credential-configuration.json="$DIR/cpp-credential-configuration.json" -n default --dry-run=client -o yaml | kubectl apply -f -

# 5. Deploy DaemonSet
echo -e "${GREEN}5. Deploying DaemonSet to install binary on host...${NC}"
kubectl apply -f "$DIR/install-cpp-hostpath-binary.yaml"

echo "Waiting for DaemonSet to copy binary (sleeping 15s)..."
sleep 15

# 6. Deploy Workload
echo -e "${GREEN}6. Deploying HostPath Workload...${NC}"
kubectl apply -f "$DIR/demo-cpp-hostpath.yaml"

echo "Waiting for workload pod to be created and sidecar to start (sleeping 15s)..."
sleep 15

# 7. Verify
echo -e "${GREEN}=== Verification ===${NC}"
echo -e "${GREEN}Checking GCS Fuse sidecar logs:${NC}"
echo -e "${YELLOW}(Expect to see execution output from mock-client-static, proving it worked in distroless!)${NC}"
kubectl logs -l app=gke-workload-with-gcsfuse-cpp-hostpath -c gke-gcsfuse-sidecar --tail=20 || true

echo ""
echo -e "${GREEN}=== Demo is running ===${NC}"
echo -e "Press ${YELLOW}[ENTER]${NC} to begin cleanup..."
read -r

# Cleanup
echo -e "${GREEN}=== Starting Cleanup ===${NC}"

echo -e "${GREEN}Deleting HostPath Workload...${NC}"
kubectl delete deployment gke-workload-with-gcsfuse-cpp-hostpath --ignore-not-found

echo -e "${GREEN}Deleting DaemonSet...${NC}"
kubectl delete daemonset install-cpp-client-on-host --ignore-not-found

echo -e "${GREEN}Deleting ConfigMap...${NC}"
kubectl delete configmap cpp-auth-config --ignore-not-found

echo -e "${GREEN}=== Cleanup Complete ===${NC}"
