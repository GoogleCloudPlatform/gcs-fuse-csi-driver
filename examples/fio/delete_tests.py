import subprocess

def run_command(command: str):
    result = subprocess.run(command.split(" "), capture_output=True, text=True)
    print(result.stdout)
    print(result.stderr)

fileSizes = ["64K", "128K", "1M", "100M", "200G"]
scenarios = ["gcsfuse-file-cache", "gcsfuse-no-file-cache", "local-ssd"]

for fileSize in fileSizes:
    if fileSize in ["100M", "200G"]:
        run_command("gcloud container clusters get-credentials --zone us-central1-a gcsfuse-csi-test-cluster")
    else:
        run_command("gcloud container clusters get-credentials --zone us-west1-c cluster-1-29-us-west1")
    
    for readType in ["read", "randread"]:
        for scenario in scenarios:
            if readType == "randread" and fileSize in ["64K", "128K"]:
                continue
            
            helm_command = f"helm uninstall fio-loading-test-{fileSize.lower()}-{readType}-{scenario}"
            run_command(helm_command)
