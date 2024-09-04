#!/bin/bash

# Copyright 2018 The Kubernetes Authors.
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

set -e

usage() {
    cat <<EOF
Install or uninstall ValidatingAdmissionPolicy and ValidatingAdmissionPolicyBinding.
usage: ${0} [OPTIONS]
One of the following flags are required: --install or --uninstall
EOF
    exit 1
}

while [[ $# -gt 0 ]]; do
    case ${1} in
        --install)
            install=true
            ;;
        --uninstall)
            uninstall=true
            ;;
        *)
            usage
            ;;
    esac
    shift
done

[ -z ${install} ] && install=false
[ -z ${uninstall} ] && uninstall=false

versionStr=$(kubectl version | sed -n '3p' | cut -d " " -f 3)

# Extract the version number
versionRegex="^v([0-9]+)\.([0-9]+)\.([0-9]+).*$"
if [[ $versionStr =~ $versionRegex ]]; then
    majorVersion=${BASH_REMATCH[1]}
    minorVersion=${BASH_REMATCH[2]}

    # Check if version is greater than or equal to 1.30
    if (( majorVersion >= 1 && minorVersion >= 30 )) || (( majorVersion > 1 )); then
        echo "Cluster version is greater than or equal to 1.30"
        script_path=$(dirname "$(realpath "$0")")
        
        if ( $install == "true" ); then
            echo "Installing ValidatingAdmissionPolicy and ValidatingAdmissionPolicyBinding"
            kubectl apply -f "${script_path}/validating_admission_policy.yaml"
        fi

        if ( $uninstall == "true" ); then
            echo "Uninstalling ValidatingAdmissionPolicy and ValidatingAdmissionPolicyBinding"
            kubectl delete -f "${script_path}/validating_admission_policy.yaml"
        fi

    else
        echo "Cluster version is less than 1.30, skip ValidatingAdmissionPolicy management"
    fi
else
    echo "Invalid version format: ${versionStr}"
fi
