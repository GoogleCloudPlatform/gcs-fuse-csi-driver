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

# Istio Compatibility

The Cloud Storage FUSE CSI driver works with [Istio service mesh](https://istio.io/latest/about/service-mesh/). Istio is not a supported Google product. We recommend running managed [Cloud Service Mesh](https://cloud.google.com/service-mesh/docs/onboarding/provision-control-plane) instead. You can also follow the documentation [Secure Kubernetes Services with Istio](https://cloud.google.com/kubernetes-engine/docs/tutorials/secure-services-istio) to install unmanaged Istio on a GKE cluster.

To use the Cloud Storage FUSE CSI driver with Istio, you'll need to adjust the following settings.

1. Pod annotations

    Add the following Pod-level annotations.

    ```yaml
    apiVersion: v1
    kind: Pod
    metadata:
      labels:
        sidecar.istio.io/inject: "true"
      annotations:
        gke-gcsfuse/volumes: "true"
        proxy.istio.io/config: '{ "holdApplicationUntilProxyStarts": true }'
        traffic.sidecar.istio.io/excludeOutboundIPRanges: 169.254.169.254/32
      name: gcsfuse-istio-test
    spec:
    ...
    ```

1. Istio `ServiceEntry`

    When the [outboundTrafficPolicy mode](https://istio.io/latest/docs/tasks/traffic-management/egress/egress-control/#envoy-passthrough-to-external-services) is configured to `REGISTRY_ONLY`, you need to create a `ServiceEntry` to communicate with the Storage Google API. Below is an example.

    ```yaml
    apiVersion: networking.istio.io/v1beta1
    kind: ServiceEntry
    metadata:
      name: googleapi
    spec:
      hosts:
      - storage.googleapis.com
      location: MESH_EXTERNAL
      ports:
      - name: https
        number: 443
        protocol: TLS
      resolution: DNS
    ```
