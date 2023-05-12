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
limitations under the License. -->

Dependencies and vendoring
--------------------------

Most projects will (eventually) use `go mod` to manage
dependencies.

The usual instructions for using [go
modules](https://github.com/golang/go/wiki/Modules) apply. Here's a cheat sheet
for some of the relevant commands:
- list available updates: `go list -u -m all`
- update or add a single dependency: `go get <package>`
- update all dependencies to their next minor or patch release:
  `go get ./...` (add `-u=patch` to limit to patch
  releases)
- lock onto a specific version: `go get <package>@<version>`
- clean up `go.mod`: `go mod tidy`
- update vendor directory: `go mod vendor`

`go mod tidy` must be used to ensure that the listed dependencies are
really still needed. Changing import statements or a tentative `go
get` can result in stale dependencies.

The `test-vendor` verifies that it was used when run locally or in a
pre-merge CI job. If a `vendor` directory is present, it will also
verify that it's content is up-to-date.

The `vendor` directory is optional. It is still present in projects
because it avoids downloading sources during CI builds. If this is no
longer deemed necessary, then a project can also remove the directory.

### Updating Kubernetes dependencies

When using packages that are part of the Kubernetes source code, the
commands above are not enough because the [lack of semantic
versioning](https://github.com/kubernetes/kubernetes/issues/72638)
prevents `go mod` from finding newer releases. Importing directly from
`kubernetes/kubernetes` also needs `replace` statements to override
the fake `v0.0.0` versions
(https://github.com/kubernetes/kubernetes/issues/79384). The
`go-get-kubernetes.sh` script can be used to update all packages in
lockstep to a different Kubernetes version. Example usage:
```
$ ./release-tools/go-get-kubernetes.sh 1.26.4
```
Since the go.mod file already requires `k8s.io/kubernetes v1.26.4`, I passed this version to the script.

