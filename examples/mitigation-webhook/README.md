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
Steps to update the memory limits of the containers in gcsfusecsi-node daemonset in gke cluster

1. Fetch the certificate-authority-data for the given cluster
```
kubectl config view --raw -o json | jq '.clusters[]' | jq "select(.name == \"$(kubectl config current-context)\")" | jq '.cluster."certificate-authority-data"' | head -n 1
```

2. Run the create-cert.sh script
```
$ chmod +x create-cert.sh
$ ./create-cert.sh
```

3. In webhook.yaml, change the `TARGET_CONTAINERS` and `TARGET_CONTAINER_MEMORY_LIMIT` to the desired container name and target container name. These are expected to be the container of the gke gcsfusecsi-node daemonset. The default values are set for `gcs-fuse-csi-driver` and `200Mi`. Replace the <cabunle> string with the string from step 1 above

4. Apply the yaml spec
```
$ kubectl apply -f webhook.yaml

$ kubectl get MutatingWebhookConfiguration gcsfuse-csi-memory-webhook
NAME                         WEBHOOKS   AGE
gcsfuse-csi-memory-webhook   1          50m
$ kubectl get deployment gcsfuse-csi-memory-webhook -n kube-system
NAME                         READY   UP-TO-DATE   AVAILABLE   AGE
gcsfuse-csi-memory-webhook   1/1     1            1           51m

```

5. Start a rolling upgrade of the gcsfusecsi-node daemonset. As the pods restart the mutating webhook will intercept the events and update the resource limits
```
$ kubectl rollout restart daemonset gcsfusecsi-node -n kube-system
```
 
6.  Verify the resource limits are changed.

```
$ kubectl get po -n kube-system -o json | jq '.items[] | select(.metadata.name | startswith("gcsfusecsi-node")) | .spec.containers[] | select(.name == "gcs-fuse-csi-driver") | .resources.limits'

{
  "memory": "200Mi"
}
```