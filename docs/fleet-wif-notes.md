# Fleet Workload Identity Authentication

This page contains example configuration to configure the `gcs-fuse-csi-driver`  
with [Fleet Workload Identity](https://cloud.google.com/anthos/fleet-management/docs/use-workload-identity) 
authentication in environments configured for 
[Workload Identity Federation](https://cloud.google.com/iam/docs/workload-identity-federation) 
outside of the Google Cloud.

## `external_account` Credentials

Instead of the Google Service Account key file, it is possible to pass a Fleet Workload Identity configuration
JSON file to the process that needs authenticating to the Google API in a Kubernetes cluster configured for 
the Workload Identity Federation. The `gcs-fuse-csi-driver` pods are such processes
that need to authenticate to the Google Cloud Storage service API to provide access to the data stores in the 
Google Cloud Storage buckets.

Such configuration file contains `external_account` type of credential that does not contain any secrets similar 
to the Google Service Account key. The configuration should be passed via the `GOOGLE_APPLICATION_CREDENTIALS` 
environment variable, which requires the file name of the file containing the configuration on 
the pod's local file system.

A ConfigMap to host the contents of the configuration file for the `GOOGLE_APPLICATION_CREDENTIALS` environment variable 
of pods on Kubernetes clusters, such as Anthos on Bare Metal clusters, that require accessing Google Cloud API using 
[Fleet Workload Identity](https://cloud.google.com/kubernetes-engine/fleet-management/docs/use-workload-identity) can be created 
like illustrated in the following snippet:

```yaml
cat <<EOF | kubectl apply -f -
kind: ConfigMap
apiVersion: v1
metadata:
  namespace: gcs-fuse-csi-driver
  name: default-creds-config
data:
  config: |
    {
      "type": "external_account",
      "audience": "identitynamespace:$FLEET_PROJECT_ID.svc.id.goog:https://gkehub.googleapis.com/projects/$FLEET_PROJECT_ID/locations/global/memberships/$CLUSTER_NAME",
      "service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/GSA_NAME@GSA_PROJECT_ID.iam.gserviceaccount.com:generateAccessToken",
      "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
      "token_url": "https://sts.googleapis.com/v1/token",
      "credential_source": {
        "file": "/var/run/secrets/tokens/gcp-ksa/token"
      }
    }
EOF
```

Please note, that the `service_account_impersonation_url` attribute in the snippet above is only necessary if you 
link a Google Service Account with the Kubernetes Service account using `iam.gke.io/gcp-service-account` annotation
and `roles/iam.workloadIdentityUser` IAM role. Otherwise, you can omit this attribute in the configuration.

## Pass `GOOGLE_APPLICATION_CREDENTIALS`

Following snippet illustrates passing the ConfigMap with `external_account` credential to the 
`gcs-fuse-csi-driver` pods of the `gcsfusecsi-node` DaemonSet that needs Fleet Workload Identity Authentication 
for accessing data in Google Cloud Storage buckets using the `GOOGLE_APPLICATION_CREDENTIALS` environment variable.

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: gcsfusecsi-node
  namespace: gcs-fuse-csi-driver
  ...
spec:
  ...
  template:
    ...
    spec:
    ...
      containers:
        - name: gcs-fuse-csi-driver
          image: gcr.io/$PROJECT_ID/gcp-filestore-csi-driver/gcs-fuse-csi-driver:$GCP_PROVIDER_SHA
      ...
          env:
        ...
            - name: GOOGLE_APPLICATION_CREDENTIALS
              value: /var/run/secrets/tokens/gcp-ksa/google-application-credentials.json
          volumeMounts:
        ...
            - mountPath: /var/run/secrets/tokens/gcp-ksa
              name: gcp-ksa
              readOnly: true
      ...
      volumes:
      ...
        - name: gcp-ksa
          projected:
            defaultMode: 420
            sources:
            - serviceAccountToken:
                audience: $FLEET_PROJECT_ID.svc.id.goog
                expirationSeconds: 172800
                path: token
            - configMap:
                items:
                - key: config
                  path: google-application-credentials.json
                name: default-creds-config
                optional: false
```
