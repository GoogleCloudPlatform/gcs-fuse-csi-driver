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