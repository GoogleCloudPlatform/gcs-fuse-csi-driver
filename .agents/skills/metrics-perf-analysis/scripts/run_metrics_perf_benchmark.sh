#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# End-to-End 80-Pod GCSFuse gRPC Metrics CSI Driver Memory & Performance Benchmark
#
# Evaluates GCSFuse CSI Driver memory consumption (Working Set, RSS, kernel cgroup v2
# memory.peak), OOMKill resilience, CPU usage, and Prometheus :9920/metrics scrape latency
# under an 80-pod concurrent gRPC + metrics workload across one or more CSI driver git
# refs/branches/commits (and an optional GKE Managed CSI Driver baseline cluster).

set -euo pipefail

export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_CSI_DIR="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"

PROJECT="${PROJECT:-gcs-fuse-test}"
ZONE="${ZONE:-us-west1-a}"
REGION="${REGION:-us-west1}"
BUCKET="${BUCKET:-${PROJECT}-${USER:-perf}-csi-metrics-e2e}"
REGISTRY="${REGISTRY:-${REGION}-docker.pkg.dev/${PROJECT}/csi-dev}"

GKE_CHANNEL="${GKE_CHANNEL:-regular}"
GKE_VERSION="${GKE_VERSION:-}"
MACHINE_TYPE="${MACHINE_TYPE:-n2-standard-64}"
DISK_SIZE="${DISK_SIZE:-500}"
OVERLAY="${OVERLAY:-dev}"

BUILD_GCSFUSE_FROM_SOURCE="${BUILD_GCSFUSE_FROM_SOURCE:-false}"
GCSFUSE_TAG="${GCSFUSE_TAG:-master}"

CSI_DIR="${CSI_DIR:-${DEFAULT_CSI_DIR}}"
WORK_DIR="${WORK_DIR:-/tmp/gcsfuse-csi-metrics-perf-$(date -u +'%Y%m%d-%H%M%S')}"
CLUSTER_PREFIX="${CLUSTER_PREFIX:-${USER:-csi}-perf}"
INCLUDE_MANAGED_BASELINE="${INCLUDE_MANAGED_BASELINE:-false}"
KEEP_CLUSTERS="${KEEP_CLUSTERS:-false}"

declare -a TARGET_SPECS=()

usage() {
  cat <<EOF
Usage: $(basename "$0") [OPTIONS] [COMMAND]

Commands:
  prepare-bucket      Create the GCS test bucket (if needed) and upload 5x 1 MiB seed files
  build               Build and push multi-arch CSI driver & sidecar images for each target
  create-clusters     Provision single-node n2-standard-64 GKE clusters and install CSI driver
  deploy-and-measure  Run Phase A (idle), 80-pod deploy + 120s warmup, Phase B (60s), Phase C (10s)
  analyze             Run analyze_metrics_perf_benchmark.py to generate JSON & Markdown reports
  cleanup             Delete all temporary benchmark GKE clusters created for the targets
  all                 Run prepare-bucket, build, create-clusters, deploy-and-measure, analyze, and cleanup

Options:
  --target SPEC                 Target CSI specification:
                                  <label>:<git_ref>[:<mem_limit>[:<staging_version>]]
                                Use git_ref="WORKTREE" (or ".") to test the current working tree
                                in --csi-dir as-is (including uncommitted changes).
                                Default mem_limit is "100Mi" (matches GKE Managed GOMEMLIMIT=95MiB).
                                Can be specified multiple times to compare branches/limits in parallel.
                                Examples:
                                  --target "candidate:WORKTREE:100Mi"
                                  --target "main-100m:origin/main:100Mi"
                                  --target "main-200m:origin/main:200Mi"
                                  --target "seq-mounts:stream-mounts-sequentially:100Mi"
                                  --target "seq-families:stream-families-individually:100Mi"
  --include-managed-baseline    Also provision and benchmark a GKE cluster with the default
                                GKE Managed CSI Driver (--addons=GcsFuseCsiDriver, 100Mi limit)
  --project PROJECT             GCP project ID (default: ${PROJECT})
  --zone ZONE                   GCP compute zone (default: ${ZONE})
  --region REGION               GCP region for Artifact Registry / bucket (default: ${REGION})
  --bucket BUCKET               GCS bucket name for 80-pod workload (default: ${BUCKET})
  --registry REGISTRY           Artifact Registry path (default: ${REGISTRY})
  --gke-channel CHANNEL         GKE release channel (default: ${GKE_CHANNEL})
  --gke-version VERSION         Explicit GKE version (optional; defaults to channel latest)
  --machine-type TYPE           GKE node machine type (default: ${MACHINE_TYPE})
  --disk-size GB                GKE node boot disk size in GB (default: ${DISK_SIZE})
  --csi-dir DIR                 Path to gcs-fuse-csi-driver repository (default: ${CSI_DIR})
  --work-dir DIR                Working directory for logs, kubeconfigs, and telemetry data
  --cluster-prefix PREFIX       Prefix for temporary GKE cluster names (default: ${CLUSTER_PREFIX})
  --build-gcsfuse-from-source   Build gcsfuse binary from source during CSI image build
  --gcsfuse-tag TAG             GCSFuse git tag or branch when building from source (default: ${GCSFUSE_TAG})
  --keep-clusters               Do not automatically delete GKE clusters at the end of 'all'
  -h, --help                    Show this help message
EOF
}

COMMAND="all"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      TARGET_SPECS+=("$2")
      shift 2
      ;;
    --include-managed-baseline)
      INCLUDE_MANAGED_BASELINE="true"
      shift
      ;;
    --project)
      PROJECT="$2"
      shift 2
      ;;
    --zone)
      ZONE="$2"
      shift 2
      ;;
    --region)
      REGION="$2"
      shift 2
      ;;
    --bucket)
      BUCKET="$2"
      shift 2
      ;;
    --registry)
      REGISTRY="$2"
      shift 2
      ;;
    --gke-channel)
      GKE_CHANNEL="$2"
      shift 2
      ;;
    --gke-version)
      GKE_VERSION="$2"
      shift 2
      ;;
    --machine-type)
      MACHINE_TYPE="$2"
      shift 2
      ;;
    --disk-size)
      DISK_SIZE="$2"
      shift 2
      ;;
    --csi-dir)
      CSI_DIR="$2"
      shift 2
      ;;
    --work-dir)
      WORK_DIR="$2"
      shift 2
      ;;
    --cluster-prefix)
      CLUSTER_PREFIX="$2"
      shift 2
      ;;
    --build-gcsfuse-from-source)
      BUILD_GCSFUSE_FROM_SOURCE="true"
      shift
      ;;
    --gcsfuse-tag)
      GCSFUSE_TAG="$2"
      shift 2
      ;;
    --keep-clusters)
      KEEP_CLUSTERS="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    prepare-bucket|build|create-clusters|deploy-and-measure|analyze|cleanup|all)
      COMMAND="$1"
      shift
      ;;
    *)
      echo "ERROR: Unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ ${#TARGET_SPECS[@]} -eq 0 && "${INCLUDE_MANAGED_BASELINE}" != "true" ]]; then
  TARGET_SPECS=("worktree:WORKTREE:100Mi")
fi

KUBE_DIR="${WORK_DIR}/kubeconfigs"
WORKTREE_ROOT="${WORK_DIR}/git-worktrees"
MANIFEST_FILE="${WORK_DIR}/targets_manifest.tsv"
mkdir -p "${WORK_DIR}" "${KUBE_DIR}" "${WORKTREE_ROOT}"

log() {
  echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')] $*"
}

sanitize_name() {
  echo "$1" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9-]+/-/g; s/^-+//; s/-+$//'
}

# Resolve all targets into parallel arrays:
#   LABELS, GIT_REFS, MEM_LIMITS, STAGING_VERSIONS, CLUSTER_NAMES, IS_MANAGED
declare -a LABELS=()
declare -a GIT_REFS=()
declare -a MEM_LIMITS=()
declare -a STAGING_VERSIONS=()
declare -a CLUSTER_NAMES=()
declare -a IS_MANAGED=()

resolve_targets() {
  : > "${MANIFEST_FILE}"
  if [[ "${INCLUDE_MANAGED_BASELINE}" == "true" ]]; then
    local cl_name
    cl_name="$(sanitize_name "${CLUSTER_PREFIX}-managed-80p" | cut -c1-40)"
    LABELS+=("managed_baseline")
    GIT_REFS+=("GKE_MANAGED")
    MEM_LIMITS+=("100Mi")
    STAGING_VERSIONS+=("GKE_MANAGED")
    CLUSTER_NAMES+=("${cl_name}")
    IS_MANAGED+=("true")
    printf "%s\t%s\t%s\t%s\t%s\t%s\n" \
      "managed_baseline" "${cl_name}" "GKE_MANAGED" "100Mi" "GKE_MANAGED" "true" >> "${MANIFEST_FILE}"
  fi

  for spec in "${TARGET_SPECS[@]}"; do
    IFS=':' read -r label git_ref mem_limit staging_ver <<< "${spec}"
    if [[ -z "${label}" || -z "${git_ref}" ]]; then
      echo "ERROR: Invalid --target '${spec}'. Expected <label>:<git_ref>[:<mem_limit>[:<staging_version>]]" >&2
      exit 1
    fi
    mem_limit="${mem_limit:-100Mi}"

    local short_sha
    if [[ "${git_ref}" == "WORKTREE" || "${git_ref}" == "." ]]; then
      short_sha="$(git -C "${CSI_DIR}" rev-parse --short=8 HEAD)"
      if [[ -n "$(git -C "${CSI_DIR}" status --porcelain)" ]]; then
        short_sha="${short_sha}-dirty"
      fi
    else
      short_sha="$(git -C "${CSI_DIR}" rev-parse --short=8 "${git_ref}")"
    fi

    local safe_label
    safe_label="$(sanitize_name "${label}")"
    if [[ -z "${staging_ver}" ]]; then
      staging_ver="v999.999.999-${safe_label}-${short_sha}"
    fi

    local cl_name
    cl_name="$(sanitize_name "${CLUSTER_PREFIX}-${safe_label}-80p" | cut -c1-40 | sed 's/-$//')"

    LABELS+=("${label}")
    GIT_REFS+=("${git_ref}")
    MEM_LIMITS+=("${mem_limit}")
    STAGING_VERSIONS+=("${staging_ver}")
    CLUSTER_NAMES+=("${cl_name}")
    IS_MANAGED+=("false")
    printf "%s\t%s\t%s\t%s\t%s\t%s\n" \
      "${label}" "${cl_name}" "${git_ref}" "${mem_limit}" "${staging_ver}" "false" >> "${MANIFEST_FILE}"
  done
}

# Obtain an isolated checkout path for a given git_ref so we never disturb the user's working tree
get_checkout_dir() {
  local git_ref="$1"
  if [[ "${git_ref}" == "WORKTREE" || "${git_ref}" == "." ]]; then
    echo "${CSI_DIR}"
    return
  fi
  local safe_ref
  safe_ref="$(sanitize_name "${git_ref}")"
  local wt_dir="${WORKTREE_ROOT}/${safe_ref}"
  if [[ ! -d "${wt_dir}" ]]; then
    git -C "${CSI_DIR}" worktree add --detach "${wt_dir}" "${git_ref}" >&2
  fi
  echo "${wt_dir}"
}

cleanup_worktrees() {
  if [[ -d "${WORKTREE_ROOT}" ]]; then
    for wt in "${WORKTREE_ROOT}"/*; do
      if [[ -d "${wt}" ]]; then
        git -C "${CSI_DIR}" worktree remove --force "${wt}" >/dev/null 2>&1 || rm -rf "${wt}"
      fi
    done
    git -C "${CSI_DIR}" worktree prune >/dev/null 2>&1 || true
  fi
}
trap cleanup_worktrees EXIT

prepare_bucket() {
  log "=== Preparing GCS bucket gs://${BUCKET} and 5x 1 MiB seed files ==="
  if ! gcloud storage buckets describe "gs://${BUCKET}" --project="${PROJECT}" >/dev/null 2>&1; then
    log "Creating bucket gs://${BUCKET} in ${REGION}..."
    gcloud storage buckets create "gs://${BUCKET}" \
      --project="${PROJECT}" \
      --location="${REGION}" \
      --uniform-bucket-level-access
  fi

  local seed_tmp="${WORK_DIR}/seed_tmp"
  mkdir -p "${seed_tmp}"
  for idx in 1 2 3 4 5; do
    if ! gcloud storage ls "gs://${BUCKET}/perf-shared/seed_${idx}.bin" >/dev/null 2>&1; then
      log "Generating and uploading gs://${BUCKET}/perf-shared/seed_${idx}.bin (1 MiB)..."
      dd if=/dev/urandom of="${seed_tmp}/seed_${idx}.bin" bs=1M count=1 status=none
      gcloud storage cp "${seed_tmp}/seed_${idx}.bin" "gs://${BUCKET}/perf-shared/seed_${idx}.bin" --project="${PROJECT}"
    fi
  done
  rm -rf "${seed_tmp}"
  log "Bucket gs://${BUCKET} is ready with 5x 1 MiB seed files."
}

build_images() {
  declare -A built_tags=()
  for i in "${!LABELS[@]}"; do
    if [[ "${IS_MANAGED[$i]}" == "true" ]]; then
      continue
    fi
    local git_ref="${GIT_REFS[$i]}"
    local staging_ver="${STAGING_VERSIONS[$i]}"
    if [[ -n "${built_tags[${staging_ver}]:-}" ]]; then
      log "Image tag ${staging_ver} already built in this session; reusing."
      continue
    fi

    local src_dir
    src_dir="$(get_checkout_dir "${git_ref}")"
    log "=== Building and pushing CSI images for ${LABELS[$i]} (ref=${git_ref}, tag=${staging_ver}) from ${src_dir} ==="
    (
      cd "${src_dir}"
      make build-image-and-push-multi-arch \
        REGISTRY="${REGISTRY}" \
        STAGINGVERSION="${staging_ver}" \
        BUILD_GCSFUSE_FROM_SOURCE="${BUILD_GCSFUSE_FROM_SOURCE}" \
        GCSFUSE_TAG="${GCSFUSE_TAG}"
    )
    built_tags["${staging_ver}"]="1"
  done
}

install_custom_csi() {
  local cluster="$1"
  local git_ref="$2"
  local staging_ver="$3"
  local mem_limit="$4"
  local kc="${KUBE_DIR}/${cluster}.kubeconfig"
  local src_dir
  src_dir="$(get_checkout_dir "${git_ref}")"

  log "=== Installing Custom CSI Driver on ${cluster} (ref=${git_ref}, image=${staging_ver}, mem_limit=${mem_limit}) ==="
  (
    cd "${src_dir}"
    KUBECONFIG="${kc}" make uninstall OVERLAY="${OVERLAY}" REGISTRY="${REGISTRY}" STAGINGVERSION="${staging_ver}" || true
    KUBECONFIG="${kc}" make install OVERLAY="${OVERLAY}" REGISTRY="${REGISTRY}" STAGINGVERSION="${staging_ver}"
  )

  log "Patching gcsfusecsi-node DaemonSet on ${cluster} to memory limit ${mem_limit} (20Mi req / 5m cpu req, no cpu limit)..."
  KUBECONFIG="${kc}" kubectl -n gcs-fuse-csi-driver get ds gcsfusecsi-node -o json | jq --arg ml "${mem_limit}" '
    (.spec.template.spec.containers[] | select(.name == "gcs-fuse-csi-driver") | .resources) =
      {"limits": {"memory": $ml}, "requests": {"cpu": "5m", "memory": "20Mi"}}
  ' | KUBECONFIG="${kc}" kubectl apply -f -

  KUBECONFIG="${kc}" kubectl -n gcs-fuse-csi-driver rollout status ds/gcsfusecsi-node --timeout=180s
  KUBECONFIG="${kc}" kubectl -n gcs-fuse-csi-driver rollout status deploy/gcs-fuse-csi-driver-webhook --timeout=180s
}

create_and_setup_clusters() {
  for i in "${!LABELS[@]}"; do
    local cl="${CLUSTER_NAMES[$i]}"
    local managed="${IS_MANAGED[$i]}"
    if gcloud container clusters describe "${cl}" --zone="${ZONE}" --project="${PROJECT}" >/dev/null 2>&1; then
      log "Cluster ${cl} already exists; skipping gcloud container clusters create."
      continue
    fi

    local -a version_args=("--release-channel=${GKE_CHANNEL}")
    if [[ -n "${GKE_VERSION}" ]]; then
      version_args+=("--cluster-version=${GKE_VERSION}")
    fi

    if [[ "${managed}" == "true" ]]; then
      log "Starting async creation of GKE Managed Baseline cluster ${cl}..."
      gcloud container clusters create "${cl}" \
        --zone="${ZONE}" \
        --project="${PROJECT}" \
        "${version_args[@]}" \
        --machine-type="${MACHINE_TYPE}" \
        --num-nodes=1 \
        --disk-size="${DISK_SIZE}" \
        --workload-pool="${PROJECT}.svc.id.goog" \
        --addons=GcsFuseCsiDriver \
        --async
    else
      log "Starting async creation of Custom CSI test cluster ${cl} (for ${LABELS[$i]})..."
      gcloud container clusters create "${cl}" \
        --zone="${ZONE}" \
        --project="${PROJECT}" \
        "${version_args[@]}" \
        --machine-type="${MACHINE_TYPE}" \
        --num-nodes=1 \
        --disk-size="${DISK_SIZE}" \
        --workload-pool="${PROJECT}.svc.id.goog" \
        --async
    fi
  done

  for i in "${!LABELS[@]}"; do
    local cl="${CLUSTER_NAMES[$i]}"
    log "Waiting for cluster ${cl} to reach RUNNING status..."
    while true; do
      local status
      status="$(gcloud container clusters describe "${cl}" --zone="${ZONE}" --project="${PROJECT}" --format="value(status)" 2>/dev/null || echo "UNKNOWN")"
      if [[ "${status}" == "RUNNING" ]]; then
        log "Cluster ${cl} is RUNNING."
        break
      elif [[ "${status}" == "ERROR" || "${status}" == "DEGRADED" ]]; then
        echo "ERROR: Cluster ${cl} entered status ${status}" >&2
        exit 1
      fi
      sleep 15
    done
    KUBECONFIG="${KUBE_DIR}/${cl}.kubeconfig" gcloud container clusters get-credentials "${cl}" --zone="${ZONE}" --project="${PROJECT}"
  done

  local proj_num
  proj_num="$(gcloud projects describe "${PROJECT}" --format="value(projectNumber)")"
  log "Granting Workload Identity default SA roles/storage.objectUser on gs://${BUCKET}..."
  gcloud storage buckets add-iam-policy-binding "gs://${BUCKET}" \
    --member="principal://iam.googleapis.com/projects/${proj_num}/locations/global/workloadIdentityPools/${PROJECT}.svc.id.goog/subject/ns/default/sa/default" \
    --role="roles/storage.objectUser" --project="${PROJECT}" >/dev/null || true

  for i in "${!LABELS[@]}"; do
    if [[ "${IS_MANAGED[$i]}" == "false" ]]; then
      install_custom_csi "${CLUSTER_NAMES[$i]}" "${GIT_REFS[$i]}" "${STAGING_VERSIONS[$i]}" "${MEM_LIMITS[$i]}"
    fi
  done
}

deploy_80_pods() {
  local cluster="$1"
  local kc="${KUBE_DIR}/${cluster}.kubeconfig"
  local manifest="${WORK_DIR}/workload_80pods_${cluster}.yaml"

  local mount_opts="implicit-dirs,client-protocol=grpc,metrics:experimental-enable-grpc-metrics:true,file-cache:max-size-mb:10,file-cache:cache-file-for-range-read:true,metadata-cache:ttl-secs:60,metadata-cache:stat-cache-max-size-mb:4,metadata-cache:type-cache-max-size-mb:4"

  local io_script='
set -u
MOUNT="/data"
POD_DIR="${MOUNT}/pods/${HOSTNAME}"
mkdir -p "${POD_DIR}"
while true; do
  # 1. Sequential read from shared seed files (first pass misses FileCache -> GCS read; subsequent passes hit FileCache)
  cat "${MOUNT}/perf-shared/seed_1.bin" "${MOUNT}/perf-shared/seed_2.bin" > /dev/null 2>&1 || true
  # 2. Random offset range reads using dd skip (triggers random FileCache read_type & range reads)
  dd if="${MOUNT}/perf-shared/seed_3.bin" of=/dev/null bs=4K skip=10 count=4 status=none 2>/dev/null || true
  dd if="${MOUNT}/perf-shared/seed_4.bin" of=/dev/null bs=4K skip=35 count=4 status=none 2>/dev/null || true
  dd if="${MOUNT}/perf-shared/seed_5.bin" of=/dev/null bs=4K skip=2  count=4 status=none 2>/dev/null || true
  # 3. Create, write, fsync, rename, read, unlink per-pod file
  dd if=/dev/zero of="${POD_DIR}/temp_write.bin" bs=32K count=4 conv=fsync status=none 2>/dev/null || true
  mv "${POD_DIR}/temp_write.bin" "${POD_DIR}/committed.bin" 2>/dev/null || true
  cat "${POD_DIR}/committed.bin" > /dev/null 2>&1 || true
  # 4. Metadata listing & positive stat
  ls -la "${MOUNT}/perf-shared" "${POD_DIR}" > /dev/null 2>&1 || true
  # 5. Intentional ENOENT negative lookup to exercise fs_ops_error_count
  cat "${POD_DIR}/non_existent_${RANDOM}.txt" > /dev/null 2>&1 || true
  rm -f "${POD_DIR}/committed.bin" 2>/dev/null || true
  sleep 2
done
'

  cat <<EOF > "${manifest}"
apiVersion: v1
kind: Service
metadata:
  name: gcsfuse-perf-sts-svc
  namespace: default
spec:
  clusterIP: None
  selector:
    app: gcsfuse-perf-sts
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gcsfuse-perf-deploy
  namespace: default
spec:
  replicas: 60
  selector:
    matchLabels:
      app: gcsfuse-perf-deploy
  template:
    metadata:
      labels:
        app: gcsfuse-perf-deploy
      annotations:
        gke-gcsfuse/volumes: "true"
        gke-gcsfuse/ephemeral-storage-request: "1Gi"
    spec:
      serviceAccountName: default
      terminationGracePeriodSeconds: 5
      containers:
      - name: io-worker
        image: busybox:1.36
        command: ["/bin/sh", "-c"]
        args:
        - |
$(echo "${io_script}" | sed 's/^/          /')
        resources:
          requests:
            cpu: 10m
            memory: 32Mi
        volumeMounts:
        - name: gcsfuse-vol
          mountPath: /data
      volumes:
      - name: gcsfuse-vol
        csi:
          driver: gcsfuse.csi.storage.gke.io
          volumeAttributes:
            bucketName: "${BUCKET}"
            mountOptions: "${mount_opts}"
            fileCacheCapacity: "20Mi"
            fileCacheForRangeRead: "true"
            metadataStatCacheCapacity: "4Mi"
            metadataTypeCacheCapacity: "4Mi"
            metadataCacheTTLSeconds: "60"
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: gcsfuse-perf-sts
  namespace: default
spec:
  serviceName: gcsfuse-perf-sts-svc
  replicas: 10
  podManagementPolicy: Parallel
  selector:
    matchLabels:
      app: gcsfuse-perf-sts
  template:
    metadata:
      labels:
        app: gcsfuse-perf-sts
      annotations:
        gke-gcsfuse/volumes: "true"
        gke-gcsfuse/ephemeral-storage-request: "1Gi"
    spec:
      serviceAccountName: default
      terminationGracePeriodSeconds: 5
      containers:
      - name: io-worker
        image: busybox:1.36
        command: ["/bin/sh", "-c"]
        args:
        - |
$(echo "${io_script}" | sed 's/^/          /')
        resources:
          requests:
            cpu: 10m
            memory: 32Mi
        volumeMounts:
        - name: gcsfuse-vol
          mountPath: /data
      volumes:
      - name: gcsfuse-vol
        csi:
          driver: gcsfuse.csi.storage.gke.io
          volumeAttributes:
            bucketName: "${BUCKET}"
            mountOptions: "${mount_opts}"
            fileCacheCapacity: "20Mi"
            fileCacheForRangeRead: "true"
            metadataStatCacheCapacity: "4Mi"
            metadataTypeCacheCapacity: "4Mi"
            metadataCacheTTLSeconds: "60"
---
apiVersion: batch/v1
kind: Job
metadata:
  name: gcsfuse-perf-jobset
  namespace: default
spec:
  parallelism: 10
  completions: 10
  template:
    metadata:
      labels:
        app: gcsfuse-perf-jobset
        jobset.sigs.k8s.io/jobset-name: "gcsfuse-perf-jobset"
      annotations:
        gke-gcsfuse/volumes: "true"
        gke-gcsfuse/ephemeral-storage-request: "1Gi"
    spec:
      serviceAccountName: default
      restartPolicy: OnFailure
      terminationGracePeriodSeconds: 5
      containers:
      - name: io-worker
        image: busybox:1.36
        command: ["/bin/sh", "-c"]
        args:
        - |
$(echo "${io_script}" | sed 's/^/          /')
        resources:
          requests:
            cpu: 10m
            memory: 32Mi
        volumeMounts:
        - name: gcsfuse-vol
          mountPath: /data
      volumes:
      - name: gcsfuse-vol
        csi:
          driver: gcsfuse.csi.storage.gke.io
          volumeAttributes:
            bucketName: "${BUCKET}"
            mountOptions: "${mount_opts}"
            fileCacheCapacity: "20Mi"
            fileCacheForRangeRead: "true"
            metadataStatCacheCapacity: "4Mi"
            metadataTypeCacheCapacity: "4Mi"
            metadataCacheTTLSeconds: "60"
EOF

  log "Applying 80-pod workload manifest on ${cluster}..."
  KUBECONFIG="${kc}" kubectl apply -f "${manifest}"

  log "Waiting for all 80 pods to become Ready on ${cluster}..."
  KUBECONFIG="${kc}" kubectl rollout status deploy/gcsfuse-perf-deploy -n default --timeout=600s
  KUBECONFIG="${kc}" kubectl rollout status sts/gcsfuse-perf-sts -n default --timeout=600s
  KUBECONFIG="${kc}" kubectl wait --for=condition=Ready pod -l app=gcsfuse-perf-jobset -n default --timeout=600s
  local ready_count
  ready_count="$(KUBECONFIG="${kc}" kubectl get pods -n default --field-selector=status.phase=Running -o name | wc -l)"
  log "Cluster ${cluster} has ${ready_count} Running workload pods."
}

collect_snapshot() {
  local cluster="$1"
  local is_managed="$2"
  local out_dir="$3"
  local idx="$4"
  local do_scrape="${5:-true}"
  local kc="${KUBE_DIR}/${cluster}.kubeconfig"
  mkdir -p "${out_dir}"

  local node_name
  node_name="$(KUBECONFIG="${kc}" kubectl get nodes -o jsonpath='{.items[0].metadata.name}')"

  local csi_ns="gcs-fuse-csi-driver"
  if [[ "${is_managed}" == "true" ]]; then
    csi_ns="kube-system"
  fi
  local csi_pod
  csi_pod="$(KUBECONFIG="${kc}" kubectl get pods -n "${csi_ns}" -l k8s-app=gcs-fuse-csi-driver -o jsonpath='{.items[0].metadata.name}')"

  : > "${out_dir}/cadvisor_${idx}.prom"

  # 1. Timed CSI Driver :9920/metrics scrape (required because custom CSI installs via
  #    make install OVERLAY=dev do not deploy the GKE-managed gcsfusecsi-metrics-collector).
  if [[ "${do_scrape}" == "true" ]]; then
    local t0 t1 elapsed_ms bytes lines prom_path
    prom_path="${out_dir}/csi_9920_last.prom"
    # Sample cAdvisor every 1.5s mid-scrape in case the scrape triggers an OOMKill that resets the container cgroup
    (
      for _ in 1 2 3 4 5 6; do
        sleep 1.5
        KUBECONFIG="${kc}" kubectl get --raw "/api/v1/nodes/${node_name}/proxy/metrics/cadvisor" 2>/dev/null \
          | grep -E '^container_(memory|cpu|oom)' | grep -E 'gcsfusecsi-node|gcs-fuse-csi-driver-webhook' \
          >> "${out_dir}/cadvisor_${idx}.prom" || true
      done
    ) &
    local sampler_pid=$!

    t0="$(date +%s%3N)"
    if KUBECONFIG="${kc}" kubectl get --raw "/api/v1/namespaces/${csi_ns}/pods/${csi_pod}:9920/proxy/metrics" \
      > "${prom_path}.tmp" 2>/dev/null && [[ -s "${prom_path}.tmp" ]]; then
      t1="$(date +%s%3N)"
      mv "${prom_path}.tmp" "${prom_path}"
      elapsed_ms=$((t1 - t0))
      bytes="$(wc -c < "${prom_path}")"
      lines="$(awk '!/^#/ && NF {c++} END {print c+0}' "${prom_path}")"
      echo "{\"sample\": ${idx}, \"timestamp\": \"$(date -u +'%Y-%m-%dT%H:%M:%SZ')\", \"scrape_ms\": ${elapsed_ms}, \"bytes\": ${bytes}, \"series_count\": ${lines}, \"status\": \"ok\"}" \
        >> "${out_dir}/scrape_meta.jsonl"
    else
      t1="$(date +%s%3N)"
      rm -f "${prom_path}.tmp"
      elapsed_ms=$((t1 - t0))
      echo "{\"sample\": ${idx}, \"timestamp\": \"$(date -u +'%Y-%m-%dT%H:%M:%SZ')\", \"scrape_ms\": ${elapsed_ms}, \"bytes\": 0, \"series_count\": 0, \"status\": \"error\"}" \
        >> "${out_dir}/scrape_meta.jsonl"
    fi
    kill "${sampler_pid}" 2>/dev/null || true
    wait "${sampler_pid}" 2>/dev/null || true
  fi

  # 2. Kubelet /stats/summary
  KUBECONFIG="${kc}" kubectl get --raw "/api/v1/nodes/${node_name}/proxy/stats/summary" \
    > "${out_dir}/stats_summary_${idx}.json" || true

  # 3. Kubelet /metrics/cadvisor (capture cgroup v2 memory.peak / container_memory_max_usage_bytes post-scrape)
  KUBECONFIG="${kc}" kubectl get --raw "/api/v1/nodes/${node_name}/proxy/metrics/cadvisor" 2>/dev/null \
    | grep -E '^container_(memory|cpu|oom)' | grep -E 'gcsfusecsi-node|gcs-fuse-csi-driver-webhook' \
    >> "${out_dir}/cadvisor_${idx}.prom" || true
}

measure_cluster_phases() {
  local label="$1"
  local cluster="$2"
  local is_managed="$3"
  local git_ref="$4"
  local mem_limit="$5"
  local staging_ver="$6"
  local kc="${KUBE_DIR}/${cluster}.kubeconfig"
  local base_dir="${WORK_DIR}/data/${cluster}"
  mkdir -p "${base_dir}/phase_a" "${base_dir}/phase_b_60s" "${base_dir}/phase_c_10s"

  cat <<EOF > "${base_dir}/target_metadata.json"
{
  "label": "${label}",
  "cluster_name": "${cluster}",
  "is_managed": ${is_managed},
  "git_ref": "${git_ref}",
  "memory_limit": "${mem_limit}",
  "staging_version": "${staging_ver}"
}
EOF

  log "=== [${cluster}] Ensuring clean 0-pod state before Phase A ==="
  KUBECONFIG="${kc}" kubectl delete deploy/gcsfuse-perf-deploy sts/gcsfuse-perf-sts job/gcsfuse-perf-jobset svc/gcsfuse-perf-sts-svc -n default --ignore-not-found
  KUBECONFIG="${kc}" kubectl wait --for=delete pod -l 'app in (gcsfuse-perf-deploy,gcsfuse-perf-sts,gcsfuse-perf-jobset)' -n default --timeout=180s || true
  sleep 10

  log "=== [${cluster}] Phase A: Pre-Workload Idle Baseline ==="
  rm -rf "${base_dir}/phase_a" && mkdir -p "${base_dir}/phase_a"
  collect_snapshot "${cluster}" "${is_managed}" "${base_dir}/phase_a" 0 "true"

  log "=== [${cluster}] Deploying 80 Pods & Warming Up Caches (120s) ==="
  deploy_80_pods "${cluster}"
  sleep 120

  rm -rf "${base_dir}/phase_b_60s" "${base_dir}/phase_c_10s"
  mkdir -p "${base_dir}/phase_b_60s" "${base_dir}/phase_c_10s"

  log "=== [${cluster}] Phase B: Standard 60s Scrape Cadence (6 samples over 5m) ==="
  for i in 1 2 3 4 5 6; do
    log "[${cluster}] Phase B sample ${i}/6..."
    collect_snapshot "${cluster}" "${is_managed}" "${base_dir}/phase_b_60s" "${i}" "true"
    if [[ "${i}" -lt 6 ]]; then
      sleep 60
    fi
  done

  local csi_ns="gcs-fuse-csi-driver"
  if [[ "${is_managed}" == "true" ]]; then
    csi_ns="kube-system"
  fi
  KUBECONFIG="${kc}" kubectl get pods -n "${csi_ns}" -l k8s-app=gcs-fuse-csi-driver -o json > "${base_dir}/csi_node_pod_status_phase_b.json"

  log "=== [${cluster}] Phase C: Stress 10s Scrape Cadence (12 samples over 2m) ==="
  for i in $(seq 1 12); do
    log "[${cluster}] Phase C sample ${i}/12..."
    collect_snapshot "${cluster}" "${is_managed}" "${base_dir}/phase_c_10s" "${i}" "true"
    if [[ "${i}" -lt 12 ]]; then
      sleep 10
    fi
  done

  KUBECONFIG="${kc}" kubectl get pods -n "${csi_ns}" -l k8s-app=gcs-fuse-csi-driver -o json > "${base_dir}/csi_node_pod_status.json"
  local csi_pod
  csi_pod="$(KUBECONFIG="${kc}" kubectl get pods -n "${csi_ns}" -l k8s-app=gcs-fuse-csi-driver -o jsonpath='{.items[0].metadata.name}')"
  KUBECONFIG="${kc}" kubectl logs -n "${csi_ns}" "${csi_pod}" -c gcs-fuse-csi-driver --tail=200 > "${base_dir}/csi_driver_logs.txt" 2>&1 || true
  log "=== [${cluster}] All measurement phases complete! ==="
}

deploy_and_measure_all() {
  declare -a pids=()
  for i in "${!LABELS[@]}"; do
    local cl="${CLUSTER_NAMES[$i]}"
    measure_cluster_phases \
      "${LABELS[$i]}" \
      "${cl}" \
      "${IS_MANAGED[$i]}" \
      "${GIT_REFS[$i]}" \
      "${MEM_LIMITS[$i]}" \
      "${STAGING_VERSIONS[$i]}" \
      > "${WORK_DIR}/measure_${cl}.log" 2>&1 &
    pids+=("$!")
    log "Launched measurement pipeline for ${LABELS[$i]} on cluster ${cl} (PID ${pids[$i]}, log: ${WORK_DIR}/measure_${cl}.log)"
  done

  for i in "${!pids[@]}"; do
    local cl="${CLUSTER_NAMES[$i]}"
    wait "${pids[$i]}"
    log "Measurement pipeline for ${LABELS[$i]} (${cl}) succeeded."
  done
}

run_analysis() {
  log "=== Running Python Telemetry Analysis ==="
  python3 "${SCRIPT_DIR}/analyze_metrics_perf_benchmark.py" \
    --data-dir "${WORK_DIR}/data" \
    --output-json "${WORK_DIR}/metrics_resource_impact_summary.json" \
    --output-markdown "${WORK_DIR}/metrics_comparison_report.md"
  log "Markdown report written to ${WORK_DIR}/metrics_comparison_report.md"
  log "JSON summary written to ${WORK_DIR}/metrics_resource_impact_summary.json"
}

cleanup_clusters() {
  log "=== Cleaning up temporary benchmark GKE clusters ==="
  for cl in "${CLUSTER_NAMES[@]}"; do
    if gcloud container clusters describe "${cl}" --zone="${ZONE}" --project="${PROJECT}" >/dev/null 2>&1; then
      log "Deleting temporary benchmark cluster ${cl}..."
      gcloud container clusters delete "${cl}" --zone="${ZONE}" --project="${PROJECT}" --quiet --async
    fi
  done
}

resolve_targets

case "${COMMAND}" in
  prepare-bucket)
    prepare_bucket
    ;;
  build)
    build_images
    ;;
  create-clusters)
    create_and_setup_clusters
    ;;
  deploy-and-measure)
    deploy_and_measure_all
    ;;
  analyze)
    run_analysis
    ;;
  cleanup)
    cleanup_clusters
    ;;
  all)
    prepare_bucket
    build_images
    create_and_setup_clusters
    deploy_and_measure_all
    run_analysis
    if [[ "${KEEP_CLUSTERS}" != "true" ]]; then
      cleanup_clusters
    else
      log "--keep-clusters specified; skipping cluster deletion."
    fi
    ;;
esac
