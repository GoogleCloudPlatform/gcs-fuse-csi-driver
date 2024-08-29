#!/bin/bash

# Copyright 2019 The Kubernetes Authors.
# Copyright 2024 Google LLC
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

# This script will install kustomize, which is a tool that simplifies patching
# Kubernetes manifests for different environments.
# https://github.com/kubernetes-sigs/kustomize

set -o nounset
set -o errexit

readonly INSTALL_DIR="$( dirname -- "$( readlink -f -- "$0"; )"; )/../../../bin"

if [ ! -f "${INSTALL_DIR}" ]; then
  mkdir -p "${INSTALL_DIR}"
fi

cd ${INSTALL_DIR}

curl -L https://istio.io/downloadIstio | sh -

cd istio-${ISTIO_VERSION}
export PATH=$PWD/bin:$PATH

istioctl install \
--set profile="minimal" \
--set values.pilot.tolerations[0].operator=Exists \
--skip-confirmation
