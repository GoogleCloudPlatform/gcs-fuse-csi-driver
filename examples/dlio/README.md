# DLIO Unet3D Loading Tests

## Prerequisites

### Build DLIO docker container image

```bash
# Replace the docker registry.
git clone https://github.com/argonne-lcf/dlio_benchmark.git
cd dlio_benchmark/
docker build -t jiaxun/dlio:v1.0.0 .
docker image push jiaxun/dlio:v1.0.0
```

### Create a new node pool

For an existing GKE cluster, use the following command to create a new node pool. Make sure the cluster has the [Workload Identity feature enabled](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#enable).

> In this early stage test, the managed GCS FUSE CSI driver feature is disabled, and the driver is manually installed.

```bash
# Replace the cluster name and zone.
gcloud container node-pools create large-pool \
    --cluster gcsfuse-csi-test-cluster \
    --ephemeral-storage-local-ssd count=16 \
    --machine-type n2-standard-96 \
    --zone us-central1-a \
    --num-nodes 3
```

### Set up GCS bucket

Create a GCS bucket using `Location type`: `Region`, and select the same region where your cluster runs. Follow the [GKE documentation](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#authentication) to configure the access. This example uses the default Kubernetes service account in the default Kubernetes namespace.

### Install Helm

The example uses Helm charts to manage the applications. Follow the [Helm documentation](https://helm.sh/docs/intro/install/#from-script) to install Helm.

## DLIO Unet3D Datasets Loading

Run the following commands to generate Unet3D datasets using DLIO, and upload to the bucket. You may need to `--set image=<your-registry>/dlio:v1.0.0` and `--set bucketName=<your-bucket-name>` to set your registry and bucket name.

```bash
cd ./examples/dlio

helm install dlio-unet3d-100kb-500k-data-loader data-loader \
--set bucketName=gke-dlio-unet3d-100kb-500k \
--set dlio.numFilesTrain=500000 \
--set dlio.recordLength=102400

helm install dlio-unet3d-500kb-1m-data-loader data-loader \
--set bucketName=gke-dlio-unet3d-500kb-1m \
--set dlio.numFilesTrain=1000000 \
--set dlio.recordLength=512000

helm install dlio-unet3d-3mb-100k-data-loader data-loader \
--set bucketName=gke-dlio-unet3d-3mb-100k \
--set dlio.numFilesTrain=100000 \
--set dlio.recordLength=3145728

helm install dlio-unet3d-150mb-5k-data-loader data-loader \
--set bucketName=gke-dlio-unet3d-150mb-5k \
--set dlio.numFilesTrain=5000 \
--set dlio.recordLength=157286400

# Clean up
helm uninstall \
dlio-unet3d-100kb-500k-data-loader \
dlio-unet3d-500kb-1m-data-loader \
dlio-unet3d-3mb-100k-data-loader \
dlio-unet3d-150mb-5k-data-loader
```

## DLIO Unet3D Loading Tests

Change the directory to `./examples/dlio`. Run the following commands to run the loading tests. Each `helm install` command will deploy a Pod to run the test, and upload logs to the bucket. You may need to `--set image=<your-registry>/dlio:v1.0.0` and `--set bucketName=<your-bucket-name>` to set your registry and bucket name.

### dlio-unet3d-100kb-500k dlio.batchSize=800

```bash
helm install dlio-unet3d-100kb-500k-800-local-ssd unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-100kb-500k \
--set dlio.numFilesTrain=500000 \
--set dlio.recordLength=102400 \
--set dlio.batchSize=800 \
--set scenario=local-ssd

helm install dlio-unet3d-100kb-500k-800-gcsfuse-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-100kb-500k \
--set dlio.numFilesTrain=500000 \
--set dlio.recordLength=102400 \
--set dlio.batchSize=800 \
--set scenario=gcsfuse-file-cache

helm install dlio-unet3d-100kb-500k-800-gcsfuse-no-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-100kb-500k \
--set dlio.numFilesTrain=500000 \
--set dlio.recordLength=102400 \
--set dlio.batchSize=800 \
--set scenario=gcsfuse-no-file-cache

# Clean up
helm uninstall \
dlio-unet3d-100kb-500k-800-local-ssd \
dlio-unet3d-100kb-500k-800-gcsfuse-file-cache \
dlio-unet3d-100kb-500k-800-gcsfuse-no-file-cache
```

### dlio-unet3d-100kb-500k dlio.batchSize=128

```bash
helm install dlio-unet3d-100kb-500k-128-local-ssd unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-100kb-500k \
--set dlio.numFilesTrain=500000 \
--set dlio.recordLength=102400 \
--set dlio.batchSize=128 \
--set scenario=local-ssd

helm install dlio-unet3d-100kb-500k-128-gcsfuse-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-100kb-500k \
--set dlio.numFilesTrain=500000 \
--set dlio.recordLength=102400 \
--set dlio.batchSize=128 \
--set scenario=gcsfuse-file-cache

helm install dlio-unet3d-100kb-500k-128-gcsfuse-no-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-100kb-500k \
--set dlio.numFilesTrain=500000 \
--set dlio.recordLength=102400 \
--set dlio.batchSize=128 \
--set scenario=gcsfuse-no-file-cache

# Clean up
helm uninstall \
dlio-unet3d-100kb-500k-128-local-ssd \
dlio-unet3d-100kb-500k-128-gcsfuse-file-cache \
dlio-unet3d-100kb-500k-128-gcsfuse-no-file-cache
```

### dlio-unet3d-500kb-1m dlio.batchSize=800

```bash
helm install dlio-unet3d-500kb-1m-800-local-ssd unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-500kb-1m \
--set dlio.numFilesTrain=1000000 \
--set dlio.recordLength=512000 \
--set dlio.batchSize=800 \
--set scenario=local-ssd

helm install dlio-unet3d-500kb-1m-800-gcsfuse-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-500kb-1m \
--set dlio.numFilesTrain=1000000 \
--set dlio.recordLength=512000 \
--set dlio.batchSize=800 \
--set scenario=gcsfuse-file-cache

helm install dlio-unet3d-500kb-1m-800-gcsfuse-no-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-500kb-1m \
--set dlio.numFilesTrain=1000000 \
--set dlio.recordLength=512000 \
--set dlio.batchSize=800 \
--set scenario=gcsfuse-no-file-cache

# Clean up
helm uninstall \
dlio-unet3d-500kb-1m-800-local-ssd \
dlio-unet3d-500kb-1m-800-gcsfuse-file-cache \
dlio-unet3d-500kb-1m-800-gcsfuse-no-file-cache
```

### dlio-unet3d-500kb-1m dlio.batchSize=128

```bash
helm install dlio-unet3d-500kb-1m-128-local-ssd unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-500kb-1m \
--set dlio.numFilesTrain=1000000 \
--set dlio.recordLength=512000 \
--set dlio.batchSize=128 \
--set scenario=local-ssd

helm install dlio-unet3d-500kb-1m-128-gcsfuse-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-500kb-1m \
--set dlio.numFilesTrain=1000000 \
--set dlio.recordLength=512000 \
--set dlio.batchSize=128 \
--set scenario=gcsfuse-file-cache

helm install dlio-unet3d-500kb-1m-128-gcsfuse-no-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-500kb-1m \
--set dlio.numFilesTrain=1000000 \
--set dlio.recordLength=512000 \
--set dlio.batchSize=128 \
--set scenario=gcsfuse-no-file-cache

# Clean up
helm uninstall \
dlio-unet3d-500kb-1m-128-local-ssd \
dlio-unet3d-500kb-1m-128-gcsfuse-file-cache \
dlio-unet3d-500kb-1m-128-gcsfuse-no-file-cache
```

### dlio-unet3d-3mb-100k dlio.batchSize=200

```bash
helm install dlio-unet3d-3mb-100k-200-local-ssd unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-3mb-100k \
--set dlio.numFilesTrain=100000 \
--set dlio.recordLength=3145728 \
--set dlio.batchSize=200 \
--set scenario=local-ssd

helm install dlio-unet3d-3mb-100k-200-gcsfuse-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-3mb-100k \
--set dlio.numFilesTrain=100000 \
--set dlio.recordLength=3145728 \
--set dlio.batchSize=200 \
--set scenario=gcsfuse-file-cache

helm install dlio-unet3d-3mb-100k-200-gcsfuse-no-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-3mb-100k \
--set dlio.numFilesTrain=100000 \
--set dlio.recordLength=3145728 \
--set dlio.batchSize=200 \
--set scenario=gcsfuse-no-file-cache

# Clean up
helm uninstall \
dlio-unet3d-3mb-100k-200-local-ssd \
dlio-unet3d-3mb-100k-200-gcsfuse-file-cache \
dlio-unet3d-3mb-100k-200-gcsfuse-no-file-cache
```

### dlio-unet3d-150mb-5k dlio.batchSize=4

```bash
helm install dlio-unet3d-150mb-5k-4-local-ssd unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-150mb-5k \
--set dlio.numFilesTrain=5000 \
--set dlio.recordLength=157286400 \
--set dlio.batchSize=4 \
--set scenario=local-ssd

helm install dlio-unet3d-150mb-5k-4-gcsfuse-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-150mb-5k \
--set dlio.numFilesTrain=5000 \
--set dlio.recordLength=157286400 \
--set dlio.batchSize=4 \
--set scenario=gcsfuse-file-cache

helm install dlio-unet3d-150mb-5k-4-gcsfuse-no-file-cache unet3d-loading-test \
--set bucketName=gke-dlio-unet3d-150mb-5k \
--set dlio.numFilesTrain=5000 \
--set dlio.recordLength=157286400 \
--set dlio.batchSize=4 \
--set scenario=gcsfuse-no-file-cache

# Clean up
helm uninstall \
dlio-unet3d-150mb-5k-4-local-ssd \
dlio-unet3d-150mb-5k-4-gcsfuse-file-cache \
dlio-unet3d-150mb-5k-4-gcsfuse-no-file-cache
```

## Parsing the test results

Run the following python script to parse the logs. The results will be saved in `./examples/dlio/output.csv`.

```bash
cd ./examples/dlio
python ./parse_logs.py
```