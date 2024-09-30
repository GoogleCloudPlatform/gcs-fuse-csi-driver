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

# Sidecar Manual Injection

This document explains how to manually inject the GCSFuse sidecar container to your workload. While this is usually done automatically by a webhook, this guide is useful for troubleshooting situations where the automatic process fails.

1. Look up for the sidecar container image signature

    Refer to this [page](./releases.md) to look for a compatible public sidecar container image according to your GKE cluster version. Click the sidecar container image link, and copy the full `sha256` image signature.

1. Modify your workload spec

    Add the `gke-gcsfuse-sidecar` container to your workload as a regular container, and also add three auxiliary volumes. Make sure the container `gke-gcsfuse-sidecar` is before your workload contianer. Meanwhile, remove the annotation `gke-gcsfuse/volumes: "true"`.

    Below is an example. Replace the image signature `xxx` with the value copied in the previous step.

    ```yaml
    apiVersion: v1
    kind: Pod
    metadata:
      name: sidecar-manual-injection
      annotations:
        # gke-gcsfuse/volumes: "true" <- remove this annotation
    spec:
      containers:
      # add the gke-gcsfuse-sidecar container BEFORE your workload container
      - args:
        - --v=5
        image: gke.gcr.io/gcs-fuse-csi-driver-sidecar-mounter@sha256:xxx
        imagePullPolicy: IfNotPresent
        name: gke-gcsfuse-sidecar
        resources:
          requests:
            cpu: 250m
            ephemeral-storage: 5Gi
            memory: 256Mi
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
          readOnlyRootFilesystem: true
          runAsGroup: 65534
          runAsNonRoot: true
          runAsUser: 65534
          seccompProfile:
            type: RuntimeDefault
        volumeMounts:
        - mountPath: /gcsfuse-tmp
          name: gke-gcsfuse-tmp
        - mountPath: /gcsfuse-buffer
          name: gke-gcsfuse-buffer
        - mountPath: /gcsfuse-cache
          name: gke-gcsfuse-cache
      - name: your-workload
      ...
      volumes:
      # add following three volumes
      - emptyDir: {}
        name: gke-gcsfuse-tmp
      - emptyDir: {}
        name: gke-gcsfuse-buffer
      - emptyDir: {}
        name: gke-gcsfuse-cache
    ```

1. Re-deploy your workload
