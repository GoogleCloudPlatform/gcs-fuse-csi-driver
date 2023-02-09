# Test

## Unit test

```bash
make unit-test
```

## Sanity test

```bash
make sanity-test
```

## End-to-end test
### Prerequisites
Enable the following GCP API:
- [Cloud Resource Manager API](https://cloud.google.com/resource-manager/reference/rest)
- [Google Cloud Storage JSON API](https://cloud.google.com/storage/docs/json_api)

### Run end-to-end test
```
gcloud auth login
gcloud config set ${PROJECT_ID}
gcloud auth application-default login
make e2e-test OVERLAY=dev
```