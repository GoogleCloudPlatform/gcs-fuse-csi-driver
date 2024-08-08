<!--
Copyright 2022 The Kubernetes Authors.
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

# How to mount a sub-directory

1. Create a bucket

    ```bash
    gcloud create bucket gs://<your-bucket-name> --uniform-bucket-level-access
    ```

1. Create a namespace `test`

    ```bash
    kubectl create ns test
    ```

1. Follow the steps in [GKE WI Federation setup](https://cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/cloud-storage-fuse-csi-driver#authentication) to configure access to a bucket for the given k8s namespace.

1. Deploy the PVC, PV and Deployment from [this](../examples/static/sub-dir-mount/pv-pvc-deployment.yaml). On the yaml spec, replace `<your-bucket-name>` with your own bucket name. This PVC mounts a sub-directory named `dir1` (this sub-directory may or may not exist to begin with). In this example we assume the sub-directory does not exist.

1. Verify pod is up and running. This pod creates a file with its own pod name to the mounted path "/data/". Note this /data mount path points to a sub-directory "dir1" as specified in the "PV.spec.mountOptions:only-dir=dir1"

    ```bash
    $ kubectl get pod
    NAME                                          READY   STATUS    RESTARTS   AGE
    gcp-gcs-csi-static-example-6bc997d676-lshqz   2/2     Running   0          5s
    ```

1. Check the bucket contents from the pod

    ```bash
    $ kubectl exec --tty -i gcp-gcs-csi-static-example-6bc997d676-lshqz -c reader -- ls /data
    gcp-gcs-csi-static-example-6bc997d676-lshqz
    ```

1. From the gcloud storage UI page [screenshot](../docs/images/bucket-subdir.png) we can see that objects "dir1/" and `gcp-gcs-csi-static-example-6bc997d676-lshqz` are created.
