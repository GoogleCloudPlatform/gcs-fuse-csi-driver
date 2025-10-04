<!--
Copyright 2025 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Cloud Storage FUSE CSI Driver Installation Guide for Self-Built K8s

The following guide is an e2e working setup for installing the Cloud Storage FUSE CSI Driver on a self-built k8s cluster. Since self-built k8s setup can vary, there is no guarantee the following guide will work for other self-built k8s clusters, with different setup steps.

## VM Creation

### Create VPC and Subnet

```bash
# Replace the env variables with your own values. Region and Zone are the locations for the VMs.
export PROJECT_ID=amacaskill-joonix
export REGION="us-central1" 
export ZONE="$REGION-b"

gcloud config set project $PROJECT_ID

gcloud compute networks create k8s-vpc \
    --subnet-mode=custom \
    --bgp-routing-mode=global \
    --mtu=1500

gcloud compute networks subnets create $REGION-sub-1 \
    --network=k8s-vpc \
    --range=10.170.0.0/16 \
    --enable-private-ip-google-access \
    --region=$REGION
```

## Create Master

```bash
export PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")

gcloud compute instances create kub-m \
  --project=$PROJECT_ID \
  --zone=$ZONE \
  --machine-type=n2-standard-2 \
  --network-interface=network-tier=PREMIUM,subnet=us-central1-sub-1 \
  --metadata=enable-oslogin=true \
  --can-ip-forward \
  --maintenance-policy=MIGRATE \
  --create-disk=auto-delete=yes,boot=yes,device-name=kub-m,image=projects/rocky-linux-cloud/global/images/rocky-linux-9-v20250611,mode=rw,size=100,type=pd-balanced \
  --no-shielded-secure-boot \
  --shielded-vtpm \
  --shielded-integrity-monitoring \
  --reservation-affinity=any \
  --tags=k8s-vpc \
  --service-account=$PROJECT_NUMBER-compute@developer.gserviceaccount.com \
  --scopes=https://www.googleapis.com/auth/cloud-platform
```

## Create Node-1

```bash 
gcloud compute instances create kub-n-1 \
  --project=$PROJECT_ID \
  --zone=$ZONE \
  --machine-type=n2-standard-4 \
  --network-interface=network-tier=PREMIUM,subnet=us-central1-sub-1 \
  --metadata=enable-oslogin=true \
  --can-ip-forward \
  --create-disk=auto-delete=yes,boot=yes,device-name=kub-n-1,image=projects/rocky-linux-cloud/global/images/rocky-linux-9-v20250611,mode=rw,size=100,type=pd-balanced \
  --no-shielded-secure-boot \
  --maintenance-policy=MIGRATE \ 
  --shielded-vtpm \
  --shielded-integrity-monitoring \
  --tags=k8s-vpc \
  --reservation-affinity=any \
  --service-account=$PROJECT_NUMBER-compute@developer.gserviceaccount.com \
  --scopes=https://www.googleapis.com/auth/cloud-platform
```

## Create Node-2

```bash
gcloud compute instances create kub-n-2 \
  --project=$PROJECT_ID \
  --zone=$ZONE \
  --machine-type=n2-standard-4 \
  --network-interface=network-tier=PREMIUM,subnet=us-central1-sub-1 \
  --metadata=enable-oslogin=true \
  --can-ip-forward \
  --create-disk=auto-delete=yes,boot=yes,device-name=kub-n-2,image=projects/rocky-linux-cloud/global/images/rocky-linux-9-v20250611,mode=rw,size=100,type=pd-balanced \
  --no-shielded-secure-boot \
  --maintenance-policy=MIGRATE \
  --shielded-vtpm \
  --shielded-integrity-monitoring \
  --reservation-affinity=any \
  --tags=k8s-vpc \
  --service-account=$PROJECT_NUMBER-compute@developer.gserviceaccount.com \
  --scopes=https://www.googleapis.com/auth/cloud-platform
```

## Configure Easy VM Access

Define a function to set env variables on dev machine and within master VM. Copy and paste the following function directly on your dev machine.

```bash
# ==============================================================================
# --- Helper Function to Sync Environment Variables between dev machine and Master VM ---
# ==============================================================================
# This function sets a variable locally and syncs it to the master VM (kub-m).
# Usage: set_and_sync_env VARIABLE_NAME "VALUE"
set_and_sync_env() {
  local VAR_NAME="$1"
  local VAR_VALUE="$2"
  local MASTER_VM="kub-m"

  # Export the variable in the local script session
  export "$VAR_NAME"="$VAR_VALUE"
  echo "INFO: Set $VAR_NAME=$VAR_VALUE locally."

  # Form the command to be added to the remote .bashrc
  local EXPORT_CMD="export $VAR_NAME=\"$VAR_VALUE\""

  # Check if the master VM exists before trying to SSH
  if gcloud compute instances describe "$MASTER_VM" --zone="$ZONE" &>/dev/null; then
    echo "INFO: Syncing/overwriting variable $VAR_NAME on master VM ($MASTER_VM)..."
    gcloud compute ssh "$MASTER_VM" --zone="$ZONE" --command="
      # Use 'sed' to delete any existing line that defines this specific variable
      sudo sed -i '/^export '"$VAR_NAME"'=/d' /root/.bashrc

      # Append the new, correct export command to the end of the file
      echo '$EXPORT_CMD' | sudo tee -a /root/.bashrc
      echo 'SUCCESS: Set/updated $VAR_NAME in /root/.bashrc on $MASTER_VM.'
    "
  fi
}

set_and_sync_env PROJECT_ID $PROJECT_ID
set_and_sync_env REGION $REGION
set_and_sync_env ZONE $ZONE
set_and_sync_env PROJECT_NUMBER $PROJECT_NUMBER
```

# Network Configuration

## Create Firewall Rules

This creates a permissive internal rule and a basic external rule that we will update later.

```bash 
# Allow all traffic within the VPC 
gcloud compute firewall-rules create k8s-vpc-allow-all --project=$PROJECT_ID --network=k8s-vpc --direction=INGRESS --priority=1000 --allow=all --source-ranges=10.170.0.0/16

# Allow external SSH traffic
gcloud compute --project=$PROJECT_ID firewall-rules create k8s-pub-rule --direction=INGRESS --priority=1000 --network=k8s-vpc --action=ALLOW --rules=tcp:22,tcp:80,tcp:443 --source-ranges=0.0.0.0/0

# Allow VM to send traffic to the internet (this allows VM to download tools like kubeadm and others from internet)
gcloud compute firewall-rules create k8s-vpc-allow-egress \
    --network=k8s-vpc \
    --action=ALLOW \
    --direction=EGRESS \
    --rules=all \
    --destination-ranges=0.0.0.0/0 \
    --priority=1000
```


## Create and Assign the Master's Public IP

This reserves a static IP and assigns it to your master VM (kub-m), which is required for Workload Identity verification.

```bash
# Create the regional static IP address
gcloud compute addresses create k8s-master-ip \
    --project=$PROJECT_ID \
    --region=$REGION

# Get the IP address value
export MASTER_PUBLIC_IP=$(gcloud compute addresses describe k8s-master-ip --project=$PROJECT_ID --region=$REGION --format='value(address)')

# Delete the VM's old temporary IP config (if any).
gcloud compute instances delete-access-config kub-m \
    --project=$PROJECT_ID \
    --zone=$ZONE \
    --quiet

# Add the new static IP config to the VM
gcloud compute instances add-access-config kub-m \
    --project=$PROJECT_ID \
    --zone=$ZONE \
    --address=$MASTER_PUBLIC_IP

# Verify the IP is now on the instance and save it to the variable again
export MASTER_PUBLIC_IP=$(gcloud compute instances describe kub-m --project=$PROJECT_ID --zone=$ZONE --format='get(networkInterfaces[0].accessConfigs[0].natIP)')
set_and_sync_env MASTER_PUBLIC_IP $MASTER_PUBLIC_IP
echo "Your Master's Public IP is now correctly assigned: $MASTER_PUBLIC_IP"
```

## Create private pool for cloud build

The following steps create a pool of Cloud Build workers that run inside your network, allowing them to connect to your cluster's private IP address. This process involves two major parts: setting up a private network connection and then creating the worker pool itself. These steps are documented in [cloud build driver install instructions](./installation.md#creating-a-private-pool), but duplicated here. 

### Set up the Private Connection

1. **Enable the Service Networking API.** This is a one-time setup step that grants your project permission to create private connections to Google services, including Cloud Build.

```bash
# Enable the Service Networking API. 
gcloud services enable servicenetworking.googleapis.com \
  --project=$PROJECT_ID
```

2. **Reserve an IP Range for the Peering Connection.** The private pool workers need their own IP addresses inside your network. This command explicitly reserves a block of addresses for Google's services to use, preventing future conflicts with your own resources. Replace `VPC_NAME` with the name of your VPC network you created during VPC configuration of your K8s on GCE cluster. To get the permissions that you need to set up a private connection, ask your administrator to grant you the Compute Engine Network Admin (roles/compute.networkAdmin) IAM role on the Google Cloud project in which the VPC network resides. A /24 prefix provides 256 addresses, which is sufficient for most pools, but make sure to confirm the prefix-length is compatible with your VPC network.

```bash
export VPC_NAME=k8s-vpc
gcloud compute addresses create reserved-range-$VPC_NAME \
    --global \
    --purpose=VPC_PEERING \
    --prefix-length=24 \
    --network=$VPC_NAME \
    --project=$PROJECT_ID
```

3. **Create the VPC Peering Connection.** This command establishes the actual private bridge. It uses the IP range you reserved in the previous step as the on-ramp for the private pool workers. The IP range you specify here will be subject to firewall rules that are defined in the VPC network.

```bash
gcloud services vpc-peerings connect \
    --service=servicenetworking.googleapis.com \
    --network=$VPC_NAME \
    --ranges=reserved-range-$VPC_NAME \
    --project=$PROJECT_ID
```



### Create the private pool

This command builds the pool and connects it to your VPC via the peering you just created. We recommend creating the pool in the same region as your Kubernetes cluster to ensure low latency and avoid network data transfer costs. Replace WORKER_POOL_NAME with a name for your pool, REGION with the region of your K8s cluster, and VPC_NAME with the name of your VPC network. To have permissions to create the private pool, ask your administrator to grant you the Cloud Build WorkerPool Owner role (`roles/cloudbuild.workerPoolOwner`).

```bash
# Set WORKER_POOL_NAME to your own name.
export WORKER_POOL_NAME=amacaskill-worker
gcloud builds worker-pools create $WORKER_POOL_NAME \
  --project=$PROJECT_ID \
  --region=$REGION \
  --peered-network=projects/$PROJECT_ID/global/networks/$VPC_NAME

# Save the Worker Pool Name for use later in gcloud builds submit.
export WORKER_POOL=projects/$PROJECT_ID/locations/$REGION/workerPools/$WORKER_POOL_NAME
```


## Finalize Firewall Rules

This updates your public rule to allow access to the Kubernetes API server and adds a new rule for the Cloud Build private pool.

```bash
# Update your public firewall rule to include the K8s API server port
gcloud compute firewall-rules update k8s-pub-rule \
    --project=$PROJECT_ID \
    --allow=tcp:22,tcp:6443

# Get the reserved IP range for the Cloud Build private pool connection
export RESERVED_RANGE=$(gcloud compute addresses describe reserved-range-k8s-vpc --global --project=$PROJECT_ID --format="value(address, prefixLength)" | awk '{print $1 "/" $2}')

# Create the firewall rule to allow the private pool to access the API server
gcloud compute firewall-rules create k8s-allow-cloudbuild-api \
    --project=$PROJECT_ID \
    --network=k8s-vpc \
    --direction=INGRESS \
    --priority=1000 \
    --action=ALLOW \
    --rules=tcp:6443 \
    --source-ranges="$RESERVED_RANGE"
```

# VM Configuration

## Install kubeadm and related tools

**IMPORTANT**: Run each one of these commands separately. Do not copy and paste entire block; otherwise, steps will be missed. This VM configuration uses and was only tested with v1.28 Kubernetes. If you want to use a different version of Kubernetes, replace all mentions of v1.28 below, with the version of Kubernetes you would like to use.

### Master configuration


```bash
gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID

sudo su -

yum install git make -y

systemctl stop firewalld
systemctl disable firewalld

sudo sed -i 's/^SELINUX=enforcing$/SELINUX=disabled/' /etc/selinux/config

cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
nf_conntrack
EOF

cat <<EOF | sudo tee /etc/modules-load.d/ipvs.conf
ip_vs
ip_vs_rr
ip_vs_wrr
ip_vs_sh
EOF

cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
vm.swappiness=0
net.ipv4.ip_forward = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-arptables=1
EOF

yum install -y epel-release

yum install -y dnf-utils yum-utils wget lrzsz ipvsadm telnet wget net-tools conntrack ipset jq iptables curl sysstat libseccomp socat nfs-utils fuse

yum-config-manager --add-repo http://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo

yum install -y containerd.io

# generate configuration
containerd config default > /etc/containerd/config.toml

# find SystemdCgroup in /etc/containerd/config.toml and update to true
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/g' /etc/containerd/config.toml

systemctl daemon-reload
systemctl enable containerd.service --now
systemctl status containerd.service

cat <<EOF | tee /etc/crictl.yaml
runtime-endpoint: "unix:///var/run/containerd/containerd.sock"
image-endpoint: "unix:///var/run/containerd/containerd.sock"
timeout: 10
debug: false
pull-image-on-create: true
disable-pull-on-run: false
EOF


cat <<EOF | sudo tee /etc/yum.repos.d/kubernetes.repo
[kubernetes]
name=Kubernetes
baseurl=https://pkgs.k8s.io/core:/stable:/v1.28/rpm/
enabled=1
gpgcheck=1
gpgkey=https://pkgs.k8s.io/core:/stable:/v1.28/rpm/repodata/repomd.xml.key
exclude=kubelet kubeadm kubectl cri-tools kubernetes-cni
EOF

sudo yum install -y kubelet kubeadm kubectl --disableexcludes=kubernetes

reboot

# ssh to master again
gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID
sudo su -
systemctl enable --now kubelet

# IMPORTANT: Replace <MASTER_PUBLIC_IP> below with the value of MASTER_PUBLIC_IP you exported above.
# For example, if your Master IP is 34.67.73.212, you would have: controlPlaneEndpoint: "34.67.73.212:6443"
cat <<EOF > kubeadm-config.yaml
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
nodeRegistration:
  criSocket: "unix:///run/containerd/containerd.sock"
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
kubernetesVersion: v1.28.0
controlPlaneEndpoint: "<MASTER_PUBLIC_IP>:6443"
networking:
  podSubnet: "10.244.0.0/16"
apiServer:
  extraArgs:
    service-account-issuer: https://kubernetes.default.svc.cluster.local
    service-account-key-file: /etc/kubernetes/pki/sa.pub
    service-account-signing-key-file: /etc/kubernetes/pki/sa.key
EOF

# IMPORTANT: record the final line of kubeadm init command you are about to run. It is required, for joining node to cluster in following steps. 
# Mine is:
# kubeadm join 34.67.73.212:6443 --token <token-value> \
#         --discovery-token-ca-cert-hash sha256:<hash>
sudo kubeadm init --config=kubeadm-config.yaml


# Finalize the setup. 
mkdir -p $HOME/.kube
sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config
sudo chown $(id -u):$(id -g) $HOME/.kube/config
kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml
exit
exit
```

### Node 1 configuration

IMPORTANT: Run each one of these commands separately. Do not copy and paste entire block otherwise, steps will be missed.

```bash
gcloud compute ssh  kub-n-1 --zone=$ZONE --project=$PROJECT_ID

sudo su -
systemctl stop firewalld
systemctl disable firewalld

sudo sed -i 's/^SELINUX=enforcing$/SELINUX=disabled/' /etc/selinux/config

cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
nf_conntrack
EOF

cat <<EOF | sudo tee /etc/modules-load.d/ipvs.conf
ip_vs
ip_vs_rr
ip_vs_wrr
ip_vs_sh
EOF

cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
vm.swappiness=0
net.ipv4.ip_forward = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-arptables=1
EOF

yum install -y epel-release

yum install -y dnf-utils yum-utils wget lrzsz ipvsadm telnet wget net-tools conntrack ipset jq iptables curl sysstat libseccomp socat nfs-utils fuse

yum-config-manager --add-repo http://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo

yum install -y containerd.io

# generate configuration
containerd config default > /etc/containerd/config.toml

# find SystemdCgroup in /etc/containerd/config.toml and update to true
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

systemctl daemon-reload
systemctl enable containerd.service --now
systemctl status containerd.service

cat <<EOF | tee /etc/crictl.yaml
runtime-endpoint: "unix:///var/run/containerd/containerd.sock"
image-endpoint: "unix:///var/run/containerd/containerd.sock"
timeout: 10
debug: false
pull-image-on-create: true
disable-pull-on-run: false
EOF


cat <<EOF | sudo tee /etc/yum.repos.d/kubernetes.repo
[kubernetes]
name=Kubernetes
baseurl=https://pkgs.k8s.io/core:/stable:/v1.28/rpm/
enabled=1
gpgcheck=1
gpgkey=https://pkgs.k8s.io/core:/stable:/v1.28/rpm/repodata/repomd.xml.key
exclude=kubelet kubeadm kubectl cri-tools kubernetes-cni
EOF

sudo yum install -y kubelet kubeadm kubectl --disableexcludes=kubernetes

reboot

# ssh to node again
gcloud compute ssh  kub-n-1 --zone=$ZONE --project=$PROJECT_ID

sudo su -
systemctl enable --now kubelet

# Join node to cluster.
# IMPORTANT: This is the last thing output from kubeadm init command run in master vm.
# DO NOT just copy and paste the kubeadm join command listed below.

kubeadm join 34.67.73.212:6443 --token <token-value> \
        --discovery-token-ca-cert-hash sha256:<hash>

exit
exit
```

### Node 2 configuration

```bash
gcloud compute ssh  kub-n-2 --zone=$ZONE --project=$PROJECT_ID

sudo su -
systemctl stop firewalld
systemctl disable firewalld

sudo sed -i 's/^SELINUX=enforcing$/SELINUX=disabled/' /etc/selinux/config

cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
nf_conntrack
EOF

cat <<EOF | sudo tee /etc/modules-load.d/ipvs.conf
ip_vs
ip_vs_rr
ip_vs_wrr
ip_vs_sh
EOF

cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
vm.swappiness=0
net.ipv4.ip_forward = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-arptables=1
EOF

yum install -y epel-release

yum install -y dnf-utils yum-utils wget lrzsz ipvsadm telnet wget net-tools conntrack ipset jq iptables curl sysstat libseccomp socat nfs-utils fuse

yum-config-manager --add-repo http://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo

yum install -y containerd.io

# generate configuration
containerd config default > /etc/containerd/config.toml

sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml


systemctl daemon-reload
systemctl enable containerd.service --now
systemctl status containerd.service

cat <<EOF | tee /etc/crictl.yaml
runtime-endpoint: "unix:///var/run/containerd/containerd.sock"
image-endpoint: "unix:///var/run/containerd/containerd.sock"
timeout: 10
debug: false
pull-image-on-create: true
disable-pull-on-run: false
EOF


cat <<EOF | sudo tee /etc/yum.repos.d/kubernetes.repo
[kubernetes]
name=Kubernetes
baseurl=https://pkgs.k8s.io/core:/stable:/v1.28/rpm/
enabled=1
gpgcheck=1
gpgkey=https://pkgs.k8s.io/core:/stable:/v1.28/rpm/repodata/repomd.xml.key
exclude=kubelet kubeadm kubectl cri-tools kubernetes-cni
EOF

sudo yum install -y kubelet kubeadm kubectl --disableexcludes=kubernetes

reboot

# ssh to node again
gcloud compute ssh  kub-n-2 --zone=$ZONE --project=$PROJECT_ID
sudo su -
systemctl enable --now kubelet

# Join cluster. IMPORTANT: This is the last thing output from kubeadm init  command run in master vm, do not copy paste mine.
kubeadm join 34.67.73.212:6443 --token <token-value> \
        --discovery-token-ca-cert-hash sha256:<hash>

exit
exit
```

# Configure Workload Identity


The GCSFuse CSI Driver utilizes two different mechanisms for STS token exchanges.
1. **Node Driver**: The first exchange is a direct API call to STS from within the node driver. This exchange will only succeed when using the GKE Managed Workload Identity Pool (`<PROJECT_ID>.svc.id.goog`) and its associated GKE created Identity Provider.
2. **Sidecar Mounter**: The second exchange occurs when the GCSFuse process (running in a sidecar within the user's pod) makes GCS API calls. These calls are intercepted by the GKE metadata server, which then handles the token exchange with STS. To do this, the GKE Metadata Server requests a token from the Kubernetes serviceaccount/token API, and it uses the correct audience for the configured Workload Identity pool.

In OSS K8s clusters, where you must provide your own Workload Identity Pool and Provider, the first token exchange in the node driver will fail due to its dependency on the GKE-managed infrastructure.

To work around this limitation in a OSS K8s cluster, you have two options.
1. **[Workload Identity Federation + skipCSIBucketAccessCheck](#option-1-workload-identity-federation--skipcsibucketaccesscheck)**: Bypass the node-level check by setting `skipCSIBucketAccessCheck: true` in your Pod manifest, which instructs the driver to bypass the problematic node-level STS token exchange entirely.
2. **[Link KSA to IAM Service Account](#option-2-link-ksa-to-iam-service-account):** Use an alternative authentication method to [Workload Identity Federation](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#authenticating_to) by directly [linking your Kubernetes Service Account (KSA) to an IAM service account](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#kubernetes-sa-to-iam). This allows the driver to impersonate the IAM account to acquire GCP credentials. This approach typically requires more hands-on configuration, including creating and managing IAM service accounts and annotating the corresponding KSAs.

**We provide instructions for both workaround options below, so choose one of the two options. Both options require completing the [Workload Identity Common Setup](#workload-identity-common-setup) below.**

## Workload Identity Common Setup

First, create a workload identity pool that will trust your Kubernetes cluster.

```bash
# Set WORKLOAD_IDENTITY_POOL to your own value.
export WORKLOAD_IDENTITY_POOL=wi-pool-amacaskill-k8s-cluster
gcloud iam workload-identity-pools create $WORKLOAD_IDENTITY_POOL \
 --project=$PROJECT_ID \
 --location="global" \
 --display-name=$WORKLOAD_IDENTITY_POOL

# All kubectl commands must be run on the master VM.
gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID

sudo -s

# Copy value of SUDO_USER to use below.
echo "SUDO_USER is $SUDO_USER"

exit
sudo su -


# This has to be run inside of the master vm (any kubectl commands do)
kubectl get --raw /.well-known/openid-configuration | jq -r .issuer

kubectl get --raw /openid/v1/jwks > cluster-jwks.json

# move cluster-jwks.json from the master vm to the dev machine in order to run the below gcloud commands.
# IMPORTANT: replace value of SUDO_USER below value you copied above when sshed with sudo -s
export S_USER=<SUDO_USER>
sudo mv /root/cluster-jwks.json /home/$S_USER/

sudo chown $S_USER:$S_USER /home/$S_USER/cluster-jwks.json

exit
exit

gcloud compute scp kub-m:~/cluster-jwks.json . \
    --zone=$ZONE \
    --project=$PROJECT_ID
```

Create a provider within the pool that trusts your Kubernetes cluster.

```bash
# Set WORKLOAD_IDENTITY_POOL_PROVIDER to your own value.
export WORKLOAD_IDENTITY_POOL_PROVIDER=wi-p-amacaskill-k8s-cluster
gcloud iam workload-identity-pools providers create-oidc $WORKLOAD_IDENTITY_POOL_PROVIDER \
   --location="global" \
   --workload-identity-pool="$WORKLOAD_IDENTITY_POOL" \
   --issuer-uri="https://kubernetes.default.svc.cluster.local" \
   --attribute-mapping="google.subject=assertion.sub" \
   --display-name=$WORKLOAD_IDENTITY_POOL_PROVIDER \
   --jwk-json-path="cluster-jwks.json"
```

Create a Kubernetes Namespace and Service Account for your application to use. You can also use any existing Kubernetes ServiceAccount in any namespace, including the default Kubernetes ServiceAccount.

```bash
# IMPORTANT: Replace NAMESPACE and KSA values with your desired values.
set_and_sync_env NAMESPACE "gcsfuse-test-ns"
set_and_sync_env KSA "k8s-on-gce-ksa"
gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID
sudo su -

kubectl create namespace $NAMESPACE
kubectl create serviceaccount $KSA --namespace $NAMESPACE

# Gcloud isn't run within master
exit
exit
```

## Option 1: Workload Identity Federation + skipCSIBucketAccessCheck

<!--TODO(amacaskill): Enhance this section with improvements from https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/pull/979 -->

In this model, you grant permissions directly to the federated identity.

Create the Credential Configuration File. This command generates a configuration that uses the KSA token to get a Google access token representing the federated identity.

```bash
gcloud iam workload-identity-pools create-cred-config \
projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$WORKLOAD_IDENTITY_POOL/providers/$WORKLOAD_IDENTITY_POOL_PROVIDER \
   --credential-source-file=/var/run/service-account/token \
   --credential-source-type=text \
   --output-file=credential-configuration.json
```

Create the Kubernetes ConfigMap. This makes the credential configuration available inside the cluster.

```bash
gcloud compute scp credential-configuration.json kub-m:~/ \
    --zone=$ZONE \
    --project=$PROJECT_ID

gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID
sudo su -

# IMPORTANT, replace SUDO_USER with the value you copied above when sshed with sudo -s.
export S_USER=<SUDO_USER>
kubectl create configmap gcs-csi-driver-config \
 --from-file=/home/$S_USER/credential-configuration.json \
 --namespace $NAMESPACE

exit
exit
```

Grant IAM Permissions to the Federated Identity. Create a Cloud Storage bucket if you haven't already, following instructions [here](https://cloud.google.com/storage/docs/creating-buckets). Then, grant the federated identity (principal://...) permissions to access it.

```bash
# IMPORTANT: Replace BUCKET_NAME with your own value.
export BUCKET_NAME="k8s-on-gce-amacaskill"
export ROLE_NAME="roles/storage.objectUser" # it could be any role you wish to grant

# Grant a given role
gcloud storage buckets add-iam-policy-binding gs://$BUCKET_NAME --member "principal://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$WORKLOAD_IDENTITY_POOL/subject/ns/$NAMESPACE/sa/$KSA" --role "$ROLE_NAME"
```

## Option 2: Link KSA to IAM Service Account

In this model, your KSA impersonates a Google Service Account (GSA), and permissions are granted to that GSA.

```bash
# IMPORTANT: Set GSA to your own value.
set_and_sync_env GSA "amacaskill-gcs-fuse"

gcloud iam service-accounts create $GSA \
   --project="$PROJECT_ID" \
   --display-name="GCS FUSE CSI Driver SA"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
--member="serviceAccount:$GSA@$PROJECT_ID.iam.gserviceaccount.com" \
--role="roles/storage.admin"

# Grant the KSA's identity permission to impersonate the GSA.
gcloud iam service-accounts add-iam-policy-binding "$GSA@$PROJECT_ID.iam.gserviceaccount.com" \
   --role="roles/iam.workloadIdentityUser" \
   --member="principal://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$WORKLOAD_IDENTITY_POOL/subject/system:serviceaccount:$NAMESPACE:$KSA"


gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID
sudo su -

# This is required because the driver uses this annotation to link the Kubernetes Service Account with a GCP SA in fetchGCPSAToken. This is so the driver can configure the KSA to impersonate IAM service accounts, which configures GKE to exchange the federated access token for an access token from the IAM Service Account Credentials API.
kubectl annotate serviceaccount $KSA \
    --namespace $NAMESPACE \
iam.gke.io/gcp-service-account=$GSA@$PROJECT_ID.iam.gserviceaccount.com

# Gcloud isn't run within master
exit
exit
```

Create the Credential Configuration File. This command generates a configuration file for impersonation by using the --service-account flag.

```bash
gcloud iam workload-identity-pools create-cred-config \
projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$WORKLOAD_IDENTITY_POOL/providers/$WORKLOAD_IDENTITY_POOL_PROVIDER \
   --service-account=$GSA@$PROJECT_ID.iam.gserviceaccount.com \
   --credential-source-file=/var/run/service-account/token \
   --credential-source-type=text \
   --output-file=credential-configuration.json
```

Create the Kubernetes ConfigMap. This makes the credential configuration available inside the cluster.

```bash
gcloud compute scp credential-configuration.json kub-m:~/ \
    --zone=$ZONE \
    --project=$PROJECT_ID

gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID
sudo su -

# IMPORTANT, replace SUDO_USER with the value you copied above when sshed with sudo -s.
export S_USER=<SUDO_USER>
kubectl create configmap gcs-csi-driver-config \
 --from-file=/home/$S_USER/credential-configuration.json \
 --namespace $NAMESPACE

exit
exit
```

## Test Workload Identity - Common Test for both Options (Optional)

Below is how you would confirm Workload Identity is working properly, which is required for the GCSFuse CSI driver to function properly. This section doesn't do any additional setup, but just confirms Workload Identity is working, before you try to install the driver. 

```bash
# Identity SA token audience which you will add to test-wi-pod.yaml below.
set_and_sync_env SA_TOKEN_AUDIENCE "//iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$WORKLOAD_IDENTITY_POOL/providers/$WORKLOAD_IDENTITY_POOL_PROVIDER"

gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID
sudo su -

vim test-wi-pod.yaml
```

Save the following into test-wi-pod.yaml.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example
  # IMPORTANT: replace this with the value of NAMESPACE you set in 'Workload Identity Common Setup'
  namespace: <NAMESPACE>
spec:
 containers:
 - name: example
   image: google/cloud-sdk:alpine
   command: ["/bin/sh", "-c", "gcloud auth login --cred-file $GOOGLE_APPLICATION_CREDENTIALS && gcloud auth list && sleep 600"]
   volumeMounts:
   - name: token
     mountPath: "/var/run/service-account"
     readOnly: true
   - name: workload-identity-credential-configuration
     mountPath: "/etc/workload-identity"
     readOnly: true
   env:
   - name: GOOGLE_APPLICATION_CREDENTIALS
     value: "/etc/workload-identity/credential-configuration.json"
 # IMPORTANT: replace this with the value of KSA you set in 'Workload Identity Common Setup'
 serviceAccountName: <KSA>
 volumes:
 - name: token
   projected:
     sources:
     - serviceAccountToken:
         # IMPORTANT: Replace this value with $SA_TOKEN_AUDIENCE from above.
         audience: <SA_TOKEN_AUDIENCE>
         expirationSeconds: 3600
         path: token
 - name: workload-identity-credential-configuration
   configMap:
     name: gcs-csi-driver-config
```


### Deploy the pod

```bash
kubectl apply -f test-wi-pod.yaml
```

Verify if it works (still within master vm). You might need to wait 30s for pod to become running before this will succeed.

```bash
kubectl exec example --namespace $NAMESPACE -- gcloud auth print-access-token

# This should output something similar to the following (but not exact as access tokens will differ) 
# ya29.c.<long-hash>
```

# Configure GCS Fuse Driver

We recommend installing the driver from a fully released + GKE cluster qualified GCSFuse CSI Driver image, as these images have passed extensive qualification on GKE clusters; 
however, support for using these qualified images directly is not supported because the OSS image for `gcs-fuse-csi-driver-webhook` is not currently hosted in a public repository. As a workaround until this is supported, you will need to build and push images for the GCSFuse CSI driver before you can start the installation process. 

To build and push your image, we recommend checking out the GCSFuse CSI driver from the latest release tag, and building from that tag. **The minimum supported GCSFuse CSI Driver version for the OSS K8s Support is [v1.19.3](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/tag/v1.19.3)**

To find the latest GCSFuse CSI Driver release tag, run the following curl command, or look for the release with the latest tag in [Releases](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/releases).

```bash
export LATEST_TAG=$(curl -s "https://api.github.com/repos/GoogleCloudPlatform/gcs-fuse-csi-driver/releases/latest" | jq -r .tag_name)
export GCSFUSE_TAG=$(curl -sL "https://raw.githubusercontent.com/GoogleCloudPlatform/gcs-fuse-csi-driver/$LATEST_TAG/cmd/sidecar_mounter/gcsfuse_binary" | cut -d'/' -f5 | cut -d'-' -f1)
echo "latest GCSFuse CSI driver release is $LATEST_TAG, which uses GCSFuse version $GCSFUSE_TAG"
```

## Building GCSFuse CSI Driver Image

1. **Create your Artifact registry** following the steps in [Development Prerequisites](./development.md#prerequisites)

2. **Use cloud build to build and push image** to your artifact registry. 

```bash
git clone https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver.git
cd gcs-fuse-csi-driver
git checkout $LATEST_TAG
# Set REGISTRY to your own value based on the name you chose in Create your Artifact registry.
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
gcloud builds submit . --config=cloudbuild-build-image.yaml --substitutions=_REGISTRY=$REGISTRY,_BUILD_GCSFUSE_FROM_SOURCE=true,_GCSFUSE_TAG=$GCSFUSE_TAG,_STAGINGVERSION=custom-$LATEST_TAG
```


## Installing the GCSFuse CSI driver

This outlines the steps needed to [install the GCSFuse CSI driver on self-built k8s clusters](./installation.md#installing-with-cloud-build-on-self-built-k8s-clusters)

1. **Create a Kube Config Secret** following the instructions in [Creating a KUBECONFIG_SECRET](./installation.md#creating-a-kubeconfig-secret). In the following step, this secret will be passed to the cloud build installation script with the substitution `_KUBECONFIG_SECRET`.

```bash
# Grant Permissions to Cloud Build: The Cloud Build service account needs permission to access secrets in your project.
export PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")
gcloud projects add-iam-policy-binding $PROJECT_ID \
    --member="serviceAccount:$PROJECT_NUMBER@cloudbuild.gserviceaccount.com" \
    --role="roles/secretmanager.secretAccessor"

# Copy kubeconfig in master vm to local machine.
gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID   --command "sudo cat /root/.kube/config" > new-kube-config

# Create the secret container: 
export KUBECONFIG_SECRET="gcsfuse-kubeconfig-secret"
gcloud secrets create $KUBECONFIG_SECRET --replication-policy="automatic"

# add kube config to secret.
gcloud secrets versions add $KUBECONFIG_SECRET \
    --data-file=new-kube-config \
    --project=$PROJECT_ID

gcloud secrets add-iam-policy-binding $KUBECONFIG_SECRET \
  --member="serviceAccount:$PROJECT_NUMBER-compute@developer.gserviceaccount.com" \
  --role="roles/secretmanager.secretAccessor" \
  --project="$PROJECT_ID"
```

2. **Install the driver** using the cloud build installation script. See [Installing with Cloud Build on self-built K8s Clusters](./installation.md#installing-with-cloud-build-on-self-built-k8s-clusters) for more details on what these flags mean. 

```bash
# Replace all env variables with your own values based on the names you chose throughout this tutorial. These are just an example based on the values I used throughout this tutorial.
export PROJECT_ID="amacaskill-joonix"
# The region from your subnet and VM configuration
export REGION='us-central1'
# The Artifact Registry path, constructed from the variables above
export REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/csi-dev"
# The name of the Workload Identity Pool you created
export WORKLOAD_IDENTITY_POOL="wi-pool-amacaskill-k8s-cluster"
# The full URI for the Workload Identity Provider, constructed from your setup
export WORKLOAD_IDENTITY_PROVIDER="//iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$WORKLOAD_IDENTITY_POOL/providers/$WORKLOAD_IDENTITY_POOL_PROVIDER"
# NOTE: You created this in create private pool section
export WORKER_POOL_NAME=amacaskill-worker
export WORKER_POOL=projects/$PROJECT_ID/locations/$REGION/workerPools/$WORKER_POOL_NAME

# Submit the Cloud Build job with the configured values
gcloud builds submit . --config=cloudbuild-install.yaml --worker-pool=$WORKER_POOL --region=$REGION --substitutions=_REGISTRY=$REGISTRY,_PROJECT_ID=$PROJECT_ID,_IDENTITY_POOL=$WORKLOAD_IDENTITY_POOL,_IDENTITY_PROVIDER=$WORKLOAD_IDENTITY_PROVIDER,_SELF_MANAGED_K8S=true,_KUBECONFIG_SECRET=$KUBECONFIG_SECRET,_STAGINGVERSION=custom-$LATEST_TAG
```
3. **Confirm the GCSFuse CSI Driver is running.**

```bash
gcloud compute ssh kub-m --zone=$ZONE --project=$PROJECT_ID
sudo su -
[root@kub-m ~]# kubectl get CSIDriver,Deployment,DaemonSet,Pods -n gcs-fuse-csi-driver
NAME                                                  ATTACHREQUIRED   PODINFOONMOUNT   STORAGECAPACITY   TOKENREQUESTS                    REQUIRESREPUBLISH   MODES                  AGE
csidriver.storage.k8s.io/gcsfuse.csi.storage.gke.io   false            true             false             wi-pool-amacaskill-k8s-cluster   true                Persistent,Ephemeral   4m22s

NAME                                          READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/gcs-fuse-csi-driver-webhook   1/1     1            1           4m22s

NAME                             DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR            AGE
daemonset.apps/gcsfusecsi-node   3         3         3       3            3           kubernetes.io/os=linux   4m22s

NAME                                               READY   STATUS    RESTARTS   AGE
pod/gcs-fuse-csi-driver-webhook-5f96ff9b65-hktbf   1/1     Running   0          4m22s
pod/gcsfusecsi-node-8dxzd                          2/2     Running   0          4m22s
pod/gcsfusecsi-node-hq92b                          2/2     Running   0          4m22s
pod/gcsfusecsi-node-qw5fb                          2/2     Running   0          4m22s

exit
exit
```

## Deploy a pod 

Deploy the pod below. Use any existing bucket, or follow instructions [here](https://cloud.google.com/storage/docs/creating-buckets) to create a new Cloud Storage bucket. For [Option 1: (Workload Identity Federation + skipCSIBucketAccessCheck)](#option-1-workload-identity-federation--skipcsibucketaccesscheck), you MUST specify `skipCSIBucketAccessCheck: true` in VolumeAttributes as shown below. For [Option 2: (Link KSA to IAM Service Account)](#option-2-link-ksa-to-iam-service-account) this is not required, and it will work with or without the `skipCSIBucketAccessCheck: true` VolumeAttribute.
 

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gcsfuse-test
  # IMPORTANT: replace this with the value of NAMESPACE you set in 'Workload Identity Common Setup'
  namespace: <NAMESPACE>
  annotations:
    gke-gcsfuse/volumes: "true"
spec:
  terminationGracePeriodSeconds: 60
  # IMPORTANT: replace this with the value of KSA you set in 'Workload Identity Common Setup'
  serviceAccountName: <KSA>
  containers:
  - image: busybox
    name: busybox
    # This command writes a file and then keeps the container running
    command: ["/bin/sh", "-c"]
    args:
    - >
      echo "Hello GCS from my Pod!" > /data/test-file.txt &&
      echo "File successfully written to /data/test-file.txt" &&
      sleep infinity
    volumeMounts:
    - name: gcsfuse-test
      mountPath: /data
  volumes:
  - name: gcsfuse-test
    csi:
      driver: gcsfuse.csi.storage.gke.io
      volumeAttributes:
        bucketName: BUCKET_NAME # IMPORTANT: Replace BUCKET_NAME with the name of the bucket you created.
        mountOptions: "implicit-dirs"
        skipCSIBucketAccessCheck: "true" # Required for option 1, optional for option 2.
```

## Deploy a pod with hostnetwork

Hostnetwork is not fully supported on OSS K8s. Hostnetwork is only supported on OSS K8s if you create your VMs with the cloud-platform scope with the node default SA. We did this in the [VM creation](#vm-creation) above by specifying the following on VM creation. 

```bash
--service-account=$PROJECT_NUMBER-compute@developer.gserviceaccount.com \
--scopes=https://www.googleapis.com/auth/cloud-platform
``` 

To use hostnetwork on OSS K8s, you must do the following:
1. Configure your VMs with the cloud-platform scope with the node default SA.
2. Specify `hostNetwork: true` in your pod spec.
3. Do NOT specify `hostNetworkPodKSA: "true"` or `identityProvider` in VolumeAttributes.
4. For [Option 1: (Workload Identity Federation + skipCSIBucketAccessCheck)](#option-1-workload-identity-federation--skipcsibucketaccesscheck), you MUST specify `skipCSIBucketAccessCheck: true` in VolumeAttributes as shown below. For [Option 2: (Link KSA to IAM Service Account)](#option-2-link-ksa-to-iam-service-account), this is not required, and it will work with or without the `skipCSIBucketAccessCheck: true` VolumeAttribute.


```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gcsfuse-test
  # IMPORTANT: replace this with the value of NAMESPACE you set in 'Workload Identity Common Setup'
  namespace: <NAMESPACE>
  annotations:
    gke-gcsfuse/volumes: "true"
spec:
  terminationGracePeriodSeconds: 60
  # IMPORTANT: replace this with the value of KSA you set in 'Workload Identity Common Setup'
  serviceAccountName: <KSA>
  hostNetwork: true
  containers:
  - image: busybox
    name: busybox
    # This command writes a file and then keeps the container running
    command: ["/bin/sh", "-c"]
    args:
    - >
      echo "Hello GCS from my Pod!" > /data/test-file.txt &&
      echo "File successfully written to /data/test-file.txt" &&
      sleep infinity
    volumeMounts:
    - name: gcsfuse-test
      mountPath: /data
  volumes:
  - name: gcsfuse-test
    csi:
      driver: gcsfuse.csi.storage.gke.io
      volumeAttributes:
        bucketName: BUCKET_NAME # IMPORTANT: Replace BUCKET_NAME with the name of the bucket you created.
        mountOptions: "implicit-dirs"
        skipCSIBucketAccessCheck: "true" # Required for option 1, optional for option 2.
  ```
