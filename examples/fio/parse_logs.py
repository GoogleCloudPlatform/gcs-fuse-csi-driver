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

import subprocess, os, json
import pprint
import datetime
from typing import Tuple

LOCAL_LOGS_LOCATION = "../../bin/fio-logs"

record = {
    "pod_name": "",
    "epoch": 0,
    "scenario": "",
    "duration": 0,
    "IOPS": 0,
    "throughput_mb_per_second": 0,
    "throughput_over_local_ssd": 0,
    "start": "",
    "end": "",
    "highest_memory": 0,
    "lowest_memory": 0,
    "highest_cpu": 0.0,
    "lowest_cpu": 0.0,
}

def get_memory(pod_name: str, start: str, end: str) -> Tuple[int, int]:
    # for some reason, the mash filter does not always work, so we fetch all the metrics for all the pods and filter later.
    result = subprocess.run(["mash", "--namespace=cloud_prod", "--output=csv", 
                             f"Query(Fetch(Raw('cloud.kubernetes.K8sContainer', 'kubernetes.io/container/memory/used_bytes'), {{'project': '641665282868', 'metric:memory_type': 'non-evictable'}})| Window(Align('10m'))| GroupBy(['pod_name', 'container_name'], Max()), TimeInterval('{start}', '{end}'), '5s')"], 
                             capture_output=True, text=True)

    data_points_int = []
    data_points_by_pod_container = result.stdout.strip().split("\n")
    for data_points in data_points_by_pod_container[1:]:
        data_points_split = data_points.split(",")
        pn = data_points_split[4]
        container_name = data_points_split[5]
        if pn == pod_name and container_name == "gke-gcsfuse-sidecar":
            try:
                data_points_int = [int(d) for d in data_points_split[7:]]
            except:
                print(f"failed to parse memory for pod {pod_name}, {start}, {end}, data {data_points_int}")
            break
    if not data_points_int:
        return 0, 0
    
    return int(min(data_points_int) / 1024 ** 2) , int(max(data_points_int) / 1024 ** 2)

def get_cpu(pod_name: str, start: str, end: str) -> Tuple[float, float]:
    # for some reason, the mash filter does not always work, so we fetch all the metrics for all the pods and filter later.
    result = subprocess.run(["mash", "--namespace=cloud_prod", "--output=csv", 
                             f"Query(Fetch(Raw('cloud.kubernetes.K8sContainer', 'kubernetes.io/container/cpu/core_usage_time'), {{'project': '641665282868'}})| Window(Rate('10m'))| GroupBy(['pod_name', 'container_name'], Max()), TimeInterval('{start}', '{end}'), '5s')"], 
                             capture_output=True, text=True)

    data_points_float = []
    data_points_by_pod_container = result.stdout.split("\n")
    for data_points in data_points_by_pod_container[1:]:
        data_points_split = data_points.split(",")
        pn = data_points_split[4]
        container_name = data_points_split[5]
        if pn == pod_name and container_name == "gke-gcsfuse-sidecar":
            try:
                data_points_float = [float(d) for d in data_points_split[6:]]
            except:
                print(f"failed to parse CPU for pod {pod_name}, {start}, {end}, data {data_points_float}")
            
            break
    
    if not data_points_float:
        return 0.0, 0.0
    
    return round(min(data_points_float), 5) , round(max(data_points_float), 5)

def unix_to_timestamp(unix_timestamp: int) -> str:
    # Convert Unix timestamp to a datetime object (aware of UTC)
    datetime_utc = datetime.datetime.fromtimestamp(unix_timestamp / 1000, tz=datetime.timezone.utc)

    # Format the datetime object as a string (if desired)
    utc_timestamp_string = datetime_utc.strftime('%Y-%m-%d %H:%M:%S UTC')
    
    return utc_timestamp_string

if __name__ == "__main__":
    logLocations = [("princer-read-cache-load-test-west/64K", "64K"), ("princer-read-cache-load-test-west/128K", "128K"), ("princer-read-cache-load-test-west/1M", "1M"), ("fio-100mb-50k", "100M"), ("fio-200gb-1", "200G")]
    
    try:
        os.makedirs(LOCAL_LOGS_LOCATION)
    except FileExistsError:
        pass
    
    for folder, fileSize in logLocations:
        try:
            os.makedirs(LOCAL_LOGS_LOCATION+"/"+fileSize)
        except FileExistsError:
            pass
        print(f"Download FIO output from the folder {folder}...")
        result = subprocess.run(["gsutil", "-m", "cp", "-r", f"gs://{folder}/fio-output", LOCAL_LOGS_LOCATION+"/"+fileSize], capture_output=False, text=True)
        if result.returncode < 0:
            print(f"failed to fetch FIO output, error: {result.stderr}")
    
    '''
    "{read_type}-{mean_file_size}":
        "mean_file_size": str
        "read_type": str
        "records":
            "local-ssd": [record1, record2, record3, record4]
            "gcsfuse-file-cache": [record1, record2, record3, record4]
            "gcsfuse-no-file-cache": [record1, record2, record3, record4]
    '''
    output = {}

    for root, _, files in os.walk(LOCAL_LOGS_LOCATION):
        for file in files:
            per_epoch_output = root + f"/{file}"
            root_split = root.split("/")
            mean_file_size = root_split[-4]
            scenario = root_split[-2]
            read_type = root_split[-1]
            epoch = int(file.split(".")[0][-1])
            
            with open(per_epoch_output, 'r') as f:
                per_epoch_output_data = json.load(f)
            
            key = "-".join([read_type, mean_file_size])
            if key not in output:
                output[key] = {
                    "mean_file_size": mean_file_size,
                    "read_type": read_type,
                    "records": {
                        "local-ssd": [],
                        "gcsfuse-file-cache": [],
                        "gcsfuse-no-file-cache": [],
                    },
                }
                
            r = record.copy()
            bs = per_epoch_output_data["jobs"][0]["job options"]["bs"]
            r["pod_name"] = f"fio-tester-{read_type}-{mean_file_size.lower()}-{bs.lower()}-{scenario}"
            r["epoch"] = epoch
            r["scenario"] = scenario
            r["duration"] = int(per_epoch_output_data["jobs"][0]["read"]["runtime"] / 1000)
            r["IOPS"] = int(per_epoch_output_data["jobs"][0]["read"]["iops"])
            r["throughput_mb_per_second"] = int(per_epoch_output_data["jobs"][0]["read"]["bw_bytes"] / (1024 ** 2))
            r["start"] = unix_to_timestamp(per_epoch_output_data["jobs"][0]["job_start"])
            r["end"] = unix_to_timestamp(per_epoch_output_data["timestamp_ms"])
            if r["scenario"] != "local-ssd":
                r["lowest_memory"], r["highest_memory"] = get_memory(r["pod_name"], r["start"], r["end"])
                r["lowest_cpu"], r["highest_cpu"] = get_cpu(r["pod_name"], r["start"], r["end"])

            pprint.pprint(r)
            
            while len(output[key]["records"][scenario]) < epoch:
                output[key]["records"][scenario].append({})
            
            output[key]["records"][scenario][epoch-1] = r

    output_order = ["read-64K", "read-128K", "read-1M", "read-100M", "read-200G", "randread-1M", "randread-100M", "randread-200G"]
    scenario_order = ["local-ssd", "gcsfuse-no-file-cache", "gcsfuse-file-cache"]

    output_file = open("./output.csv", "a")
    output_file.write("File Size,Read Type,Scenario,Epoch,Duration (s),Throughput (MB/s),IOPS,GCSFuse Lowest Memory (MB),GCSFuse Highest Memory (MB),GCSFuse Lowest CPU (core),GCSFuse Highest CPU (core),Pod,Start,End\n")
    
    for key in output_order:
        if key not in output:
            continue
        record_set = output[key]

        for scenario in scenario_order:
            for i in range(len(record_set["records"][scenario])):
                r = record_set["records"][scenario][i]
                # r["throughput_over_local_ssd"] = round(r["throughput_mb_per_second"] / record_set["records"]["local-ssd"][i]["throughput_mb_per_second"] * 100, 2)
                output_file.write(f"{record_set['mean_file_size']},{record_set['read_type']},{scenario},{r['epoch']},{r['duration']},{r['throughput_mb_per_second']},{r['IOPS']},{r['lowest_memory']},{r['highest_memory']},{r['lowest_cpu']},{r['highest_cpu']},{r['pod_name']},{r['start']},{r['end']}\n")

    output_file.close()
