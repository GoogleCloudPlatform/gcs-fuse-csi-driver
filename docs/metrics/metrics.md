<!--
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

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

# Metrics

Google Cloud Storage FUSE offers client-side Prometheus metrics for monitoring file system operations and performance. These metrics, gathered by the sidecar container, are forwarded to the CSI driver node servers. You can access these metrics via a Prometheus server. This guide explains how to enable client-side metrics, set up a Prometheus server, and query the metrics for insights.

## Enable metrics collection in your workload

You don't need to set anything to enable the metrics collection. The metrics collection is enabled by default.

To **disable** the metrics collection, set the volume attribute `disableMetrics: "true"`.

For in-line ephemeral volumes:

```yaml
...
spec:
    volumes:
    - name: gcs-fuse-csi-ephemeral
    csi:
        driver: gcsfuse.csi.storage.gke.io
        volumeAttributes:
            bucketName: <bucket-name>
            disableMetrics: "true"
```

For `PersistentVolume` volumes:

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: gcs-fuse-csi-pv
spec:
  ...
  csi:
    driver: gcsfuse.csi.storage.gke.io
    volumeHandle: <bucket-name>
    volumeAttributes:
        disableMetrics: "true"
```

## Install Helm

The example uses Helm charts to manage Prometheus server. Follow the [Helm documentation](https://helm.sh/docs/intro/install/#from-script) to install Helm.

## Install a Prometheus server to collect metrics

Add Prometheus Helm repo and update the repo.

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
```

Create a new Kubernetes namespace `prometheus`, and install a Prometheus server using Helm. Note that the following example only installs a Prometheus server without other auxiliary components.

```bash
kubectl create namespace prometheus

helm install prometheus prometheus-community/prometheus \
--namespace prometheus \
--values ./docs/metrics/prometheus-values.yaml
```

## Connect to the Prometheus server

Create a new terminal session and run the following command to forward traffic from your local machine to the Prometheus server.

```bash
export POD_NAME=$(kubectl get pods --namespace prometheus -l "app.kubernetes.io/name=prometheus,app.kubernetes.io/instance=prometheus" -o jsonpath="{.items[0].metadata.name}")
kubectl --namespace prometheus port-forward $POD_NAME 9920
```

## Open Prometheus UI to query metrics

Open a web browser and go to the following URL. The example shows a graph of the metric `fs_ops_count`.

> <http://localhost:9090/graph?g0.expr=fs_ops_count&g0.tab=0&g0.display_mode=lines&g0.show_exemplars=0&g0.range_input=10m>

Below is a list of supported Google Cloud Storage FUSE metrics:

- fs_ops_count
- fs_ops_error_count
- fs_ops_latency
- gcs_download_bytes_count
- gcs_read_count
- gcs_read_bytes_count
- gcs_reader_count
- gcs_request_count
- gcs_request_latencies
- file_cache_read_count
- file_cache_read_bytes_count
- file_cache_read_latencies

See the [Google Cloud Storage FUSE Metrics documentation](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/docs/metrics.md) for detailed explanation.

In the CSI driver, each metric record includes the following extra labels so that you can filter and aggregate metrics.

- pod_name
- namespace_name
- volume_name
- bucket_name

The Prometheus UI provides an easy interface to query and visualize metrics. See [Querying Prometheus documentation](https://prometheus.io/docs/prometheus/latest/querying/basics/) for details.

## Clean up Prometheus server

Run the following command to clean up the Prometheus server.

Warning: the following command will clean up the PV and PVC storing Prometheus data. If you need to retain the metrics data, in the step [Install a Prometheus server to collect metrics](#install-a-prometheus-server-to-collect-metrics), create a `StorageClass` with `reclaimPolicy: Retain`, and set the helm parameter `server.persistentVolume.storageClass` using the new `StorageClass` name.

```bash
helm uninstall prometheus --namespace prometheus
```
