#!/bin/bash

# Copyright 2022 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
set -o errexit
set -o nounset
set -o pipefail

TOOL_VERSION="v1.51.2"

export PATH=$PATH:$(go env GOPATH)/bin
go install "github.com/golangci/golangci-lint/cmd/golangci-lint@${TOOL_VERSION}"

echo "Verifying golint..."

# enabled by default: deadcode,errcheck,gosimple,govet,ineffassign,staticcheck,structcheck,typecheck,unused,varcheck
golangci-lint run --no-config --deadline=10m --sort-results \
--enable gofmt,revive,misspell,exportloopref,asciicheck,bodyclose,depguard,durationcheck,errname,forbidigo

echo "Congratulations! Lint check completed for all Go source files."