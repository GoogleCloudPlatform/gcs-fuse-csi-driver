import subprocess

def run_command(command: str):
    result = subprocess.run(command.split(" "), capture_output=True, text=True)
    print(result.stdout)
    print(result.stderr)

metadataCacheTtlSecs = 6048000
bucketName_fileSize_blockSize_statCacheCapacity_fileCacheMaxSizeInMb = [
    ("princer-read-cache-load-test-west", "64K", "64K", 3000000, 650000), 
    ("princer-read-cache-load-test-west", "128K", "128K", 3000000, 1300000),
    ("princer-read-cache-load-test-west", "1M", "256K", 3000000, 1000000),
    ("fio-100mb-50k", "100M", "1M", 150000, 5000000),
    ("fio-200gb-1", "200G", "1M", 0, 300000)
    ]

scenarios = ["gcsfuse-file-cache", "gcsfuse-no-file-cache", "local-ssd"]

for bucketName, fileSize, blockSize, statCacheCapacity, fileCacheMaxSizeInMb in bucketName_fileSize_blockSize_statCacheCapacity_fileCacheMaxSizeInMb:
    if fileSize in ["100M", "200G"]:
        run_command("gcloud container clusters get-credentials --zone us-central1-a gcsfuse-csi-test-cluster")
    else:
        run_command("gcloud container clusters get-credentials --zone us-west1-c cluster-1-29-us-west1")
    
    for readType in ["read", "randread"]:
        for scenario in scenarios:
            if readType == "randread" and fileSize in ["64K", "128K"]:
                continue
            
            commands = [f"helm install fio-loading-test-{fileSize.lower()}-{readType}-{scenario} loading-test",
                        f"--set bucketName={bucketName}",
                        f"--set scenario={scenario}",
                        f"--set fio.readType={readType}",
                        f"--set fio.fileSize={fileSize}",
                        f"--set fio.blockSize={blockSize}",
                        f"--set gcsfuse.metadataCacheTtlSecs={metadataCacheTtlSecs}",
                        f"--set gcsfuse.statCacheCapacity={statCacheCapacity}",
                        f"--set gcsfuse.fileCacheMaxSizeInMb={fileCacheMaxSizeInMb}",
                        "--set gcsfuse.fileCacheForRangeRead=true"]
            
            helm_command = " ".join(commands)

            run_command(helm_command)
