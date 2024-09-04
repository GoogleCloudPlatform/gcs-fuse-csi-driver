#!/bin/bash

# Copyright 2018 The Kubernetes Authors.
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

TOOL_VERSION="v1.60.1"

export PATH=$PATH:$(go env GOPATH)/bin
go install "github.com/golangci/golangci-lint/cmd/golangci-lint@${TOOL_VERSION}"

echo "Verifying golint..."

golangci-lint cache clean

# disable deprecated linters and ohter unnecessary linters
golangci-lint run --no-config --timeout=10m --sort-results \
--verbose \
--fix \
--enable-all  \
--max-same-issues 100 \
--disable exhaustruct,gomnd,lll,gochecknoglobals,funlen,varnamelen,wsl,testpackage,wrapcheck,err113,ireturn,gocyclo,cyclop,godox,gocognit,nestif,gomoddirectives,maintidx,depguard,mnd,execinquery \
--go 1.23.0 # the builder version

echo "Congratulations! Lint check completed for all Go source files."
