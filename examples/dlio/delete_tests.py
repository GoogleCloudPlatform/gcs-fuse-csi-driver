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

bucketName_batchSize = [
    ("gke-dlio-unet3d-100kb-500k", 800), 
    ("gke-dlio-unet3d-100kb-500k", 128),
    ("gke-dlio-unet3d-500kb-1m", 800),
    ("gke-dlio-unet3d-500kb-1m", 128),
    ("gke-dlio-unet3d-3mb-100k", 200),
    ("gke-dlio-unet3d-150mb-5k", 4)
    ]
scenarios = ["gcsfuse-file-cache", "gcsfuse-no-file-cache", "local-ssd"]

for bucketName, batchSize in bucketName_batchSize:    
    for scenario in scenarios:
        helm_command = f"helm uninstall {bucketName}-{batchSize}-{scenario}"
        run_command(helm_command)
