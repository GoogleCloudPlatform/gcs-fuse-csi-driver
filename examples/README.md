<!--
Copyright 2018 The Kubernetes Authors.
Copyright 2022 Google LLC

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

# Example Applications

## CSI Ephemeral Volume Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/ephemeral/deployment.yaml
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/ephemeral/deployment-non-root.yaml
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/ephemeral/deployment-two-vols.yaml

# install a Deployment using CSI Ephemeral Inline volume
kubectl apply -f ./examples/ephemeral/deployment.yaml
kubectl apply -f ./examples/ephemeral/deployment-non-root.yaml
kubectl apply -f ./examples/ephemeral/deployment-two-vols.yaml

# clean up
kubectl delete -f ./examples/ephemeral/deployment.yaml
kubectl delete -f ./examples/ephemeral/deployment-non-root.yaml
kubectl delete -f ./examples/ephemeral/deployment-two-vols.yaml
```

## Static Provisioning Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/static/pv-pvc-deployment.yaml
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/static/pv-pvc-deploymen-non-root.yaml

# install PV/PVC and a Deployment
kubectl apply -f ./examples/static/pv-pvc-deployment.yaml
kubectl apply -f ./examples/static/pv-pvc-deploymen-non-root.yaml

# clean up
# the PV deletion will not delete your GCS bucket
kubectl delete -f ./examples/static/pv-pvc-deployment.yaml
kubectl delete -f ./examples/static/pv-pvc-deploymen-non-root.yaml
```

## Batch Job Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/batch-job/job.yaml

# install a Job using CSI Ephemeral Inline volume
kubectl apply -f ./examples/batch-job/job.yaml

# clean up
kubectl delete -f ./examples/batch-job/job.yaml
```

## Jupyter Notebook Example

```bash
# replace <bucket-name> with your pre-provisioned GCS bucket name
GCS_BUCKET_NAME=your-bucket-name
sed -i "s/<bucket-name>/$GCS_BUCKET_NAME/g" ./examples/jupyter/jupyter-notebook-server.yaml

# install a Jupyter Notebook server using CSI Ephemeral Inline volume
kubectl apply -f ./examples/jupyter/jupyter-notebook-server.yaml

# access the Jupyter Notebook via http://localhost:8888
kubectl port-forward jupyter-notebook-server 8888:8888

# clean up
kubectl delete -f ./examples/jupyter/jupyter-notebook-server.yaml
```

## Multi-NIC Example

If running on a VM instance (k8s node) with multiple NICs, the `multiNICIndex`
volume attribute can be used to attach a mount point to a particular NIC. If the
instance NICs are aligned to NUMA nodes, then the index corresponds to the NUMA
node, otherwise the index is that of gve and virtio_net devices given by `ip
link`.

To use multiple NICs, the pod must run in host network. If the
`gke-gcsfuse/enable-numa-pinning=true` annotation is used, then the gcs fuse
daemon will be pinned to the corresponding NUMA node.

As an example, the following command on GKE creates an n2 instance with 2 NUMA
nodes and two NICs. There is no actual NUMA alignment for the NICs, but this
is an inexpensive way to test the behavior of machines like TPU v7x which do
have NUMA aligned NICs.

```bash
gcloud container node-pools create ${NODE_POOL} \
  --machine-type n2-standard-64 --num-nodes 1 \
  --enable-gvnic \
  --additional-node-network=network=additional,subnetwork=alt
```

It assumes you have created the network and subnetwork with commands like the
following (the exact ranges used will depend on your specifics). The router
commands are necessary to expose the network externally, so it can reach GCS
endpoints. The parameters used are not critical and depend on your setup as
well. The network and subnet names are only used for node pool creation and are
not critcal for your pod configuration.

```bash
gcloud compute networks create additional
gcloud compute networks subnests create alt \
  --range=10.144.16.0/20 \
  --network=additional \
  --secondary-range=alt=10.144.32.0/20
  --region=${CLUSTER_LOCATION}
gcloud compute routers create additional \
  --network=additional \
  --region=${CLUSTER_LOCATION}
gcloud compute routers nats create additional-nat \
  --router=additional \
  --region=${CLUSTER_LOCATION} \
  --auto-allocate-nat-external-ips \
  --nat-all-subnet-ip-ranges
```

Then deploy a pod which uses the second NIC from a node in your new node
pool. Ensure that you have set up permissions to your GCS bucket as described
elsewhere.

```bash
cat ./multi-nic/index.yaml | \
  sed s/BUCKET/${BUCKET}/ | \
  sed s/PROJECT/${PROJECT}/ | \
  sed s/LOCATION/${CLUSTER_LOCATION}/ | \
  sed s/CLUSTER/${CLUSTER}/ | \
  sed s/NODE-POOL/${NODE_POOL}/ | \
  kubectl apply -f -
```
