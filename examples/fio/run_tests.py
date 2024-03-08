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

bucketName_fileSize_blockSize = [
    ("princer-read-cache-load-test-west", "64K", "64K"), 
    ("princer-read-cache-load-test-west", "128K", "128K"),
    ("princer-read-cache-load-test-west", "1M", "256K"),
    ("fio-100mb-50k", "100M", "1M"),
    ("fio-200gb-1", "200G", "1M")
    ]

scenarios = ["gcsfuse-file-cache", "gcsfuse-no-file-cache", "local-ssd"]

for bucketName, fileSize, blockSize in bucketName_fileSize_blockSize:
    if fileSize in ["100M", "200G"]:
        run_command("gcloud container clusters get-credentials --zone us-central1-c test-cluster-us-central1-c")
    else:
        run_command("gcloud container clusters get-credentials --zone us-west1-c test-cluster-us-west1-c")
    
    for readType in ["read", "randread"]:
        for scenario in scenarios:
            if readType == "randread" and fileSize in ["64K", "128K"]:
                continue
            
            commands = [f"helm install fio-loading-test-{fileSize.lower()}-{readType}-{scenario} loading-test",
                        f"--set bucketName={bucketName}",
                        f"--set scenario={scenario}",
                        f"--set fio.readType={readType}",
                        f"--set fio.fileSize={fileSize}",
                        f"--set fio.blockSize={blockSize}"]
            
            if fileSize == "200G":
                commands.append("--set gcsfuse.metadataStatCacheCapacity=0")
            
            helm_command = " ".join(commands)

            run_command(helm_command)
