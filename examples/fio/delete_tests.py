#!/usr/bin/env python

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

import subprocess

def run_command(command: str):
    result = subprocess.run(command.split(" "), capture_output=True, text=True)
    print(result.stdout)
    print(result.stderr)

fileSizes = ["64K", "128K", "1M", "100M", "200G"]
scenarios = ["gcsfuse-file-cache", "gcsfuse-no-file-cache", "local-ssd"]

for fileSize in fileSizes:
    if fileSize in ["100M", "200G"]:
        run_command("gcloud container clusters get-credentials --zone us-central1-c test-cluster-us-central1-c")
    else:
        run_command("gcloud container clusters get-credentials --zone us-west1-c test-cluster-us-west1-c")
    
    for readType in ["read", "randread"]:
        for scenario in scenarios:
            if readType == "randread" and fileSize in ["64K", "128K"]:
                continue
            
            helm_command = f"helm uninstall fio-loading-test-{fileSize.lower()}-{readType}-{scenario}"
            run_command(helm_command)
