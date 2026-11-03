#!/usr/bin/env python3
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

"""Analyzes 80-Pod GCSFuse gRPC Metrics CSI Driver Memory & Resource Impact across GKE clusters.

Reads telemetry collected by run_metrics_perf_benchmark.sh (Kubelet /stats/summary,
cAdvisor container_memory_max_usage_bytes, :9920/metrics scrape metadata & payloads,
and pod status JSONs) and produces both a structured JSON summary and a Markdown
comparison report.
"""

import argparse
from datetime import datetime
import glob
import json
import math
import os
import re
from typing import Any, Dict, List, Tuple

LIMITS = {
    "gcs-fuse-csi-driver": {
        "mem_req_mib": 20.0,
        "mem_limit_gke_mib": 100.0,
        "gomemlimit_gke_mib": 95.0,
        "mem_limit_oss_mib": 200.0,
        "gomemlimit_oss_mib": 190.0,
        "cpu_req_m": 5.0,
    },
    "gcsfusecsi-metrics-collector": {
        "mem_req_mib": 30.0,
        "mem_limit_gke_mib": 100.0,
        "cpu_req_m": 5.0,
        "cpu_limit_gke_m": 1000.0,
    },
    "csi-driver-registrar": {
        "mem_req_mib": 10.0,
        "mem_limit_gke_mib": 50.0,
        "mem_limit_oss_mib": 100.0,
        "cpu_req_m": 5.0,
    },
    "gke-gcsfuse-sidecar": {
        "mem_req_mib": 256.0,
        "cpu_req_m": 250.0,
        "ephemeral_storage_gib": 5.0,
    },
    "gke-gcsfuse-metadata-prefetch": {
        "mem_req_mib": 10.0,
        "mem_limit_gke_mib": 50.0,
        "cpu_req_m": 10.0,
        "cpu_limit_gke_m": 250.0,
    },
    "gcs-fuse-csi-driver-webhook": {
        "mem_req_mib": 20.0,
        "mem_limit_oss_mib": 200.0,
        "cpu_req_m": 10.0,
        "cpu_limit_oss_m": 200.0,
    },
}


def compute_stats(values: List[float]) -> Dict[str, Any]:
  if not values:
    return {
        "count": 0,
        "mean": 0.0,
        "std": 0.0,
        "min": 0.0,
        "p50": 0.0,
        "p95": 0.0,
        "max": 0.0,
        "raw_samples": [],
    }
  s = sorted(values)
  n = len(s)
  mean = sum(s) / n
  variance = sum((x - mean) ** 2 for x in s) / n if n > 1 else 0.0

  def pct(p: float) -> float:
    idx = (n - 1) * p
    lo = int(math.floor(idx))
    hi = int(math.ceil(idx))
    if lo == hi:
      return s[lo]
    return s[lo] + (s[hi] - s[lo]) * (idx - lo)

  return {
      "count": n,
      "mean": round(mean, 3),
      "std": round(math.sqrt(variance), 3),
      "min": round(s[0], 3),
      "p50": round(pct(0.50), 3),
      "p95": round(pct(0.95), 3),
      "max": round(s[-1], 3),
      "raw_samples": [round(v, 3) for v in values],
  }


def parse_iso_ts(ts_str: str) -> float:
  if not ts_str:
    return 0.0
  ts_str = re.sub(r"\.(\d{6})\d*Z$", r".\1+00:00", ts_str)
  ts_str = ts_str.replace("Z", "+00:00")
  try:
    return datetime.fromisoformat(ts_str).timestamp()
  except ValueError:
    return 0.0


def parse_pod_status(status_path: str) -> Dict[str, Any]:
  if not os.path.exists(status_path):
    return {}
  with open(status_path, "r", encoding="utf-8") as f:
    data = json.load(f)
  items = data.get("items", [])
  if not items:
    return {}
  pod = items[0]
  out = {}
  for cs in pod.get("status", {}).get("containerStatuses", []):
    cname = cs.get("name", "")
    last_term = cs.get("lastState", {}).get("terminated", {})
    out[cname] = {
        "restartCount": cs.get("restartCount", 0),
        "ready": cs.get("ready", False),
        "lastTerminatedReason": last_term.get("reason", ""),
        "lastTerminatedExitCode": last_term.get("exitCode", 0),
    }
  return out


def analyze_phase_dir(phase_dir: str) -> Dict[str, Any]:
  stat_files = sorted(
      glob.glob(os.path.join(phase_dir, "stats_summary_*.json")),
      key=lambda p: int(re.search(r"(\d+)\.json$", p).group(1)),
  )

  csi_containers: Dict[str, Dict[str, List[float]]] = {
      "gcs-fuse-csi-driver": {"ws_mib": [], "rss_mib": [], "cpu_m": []},
      "gcsfusecsi-metrics-collector": {"ws_mib": [], "rss_mib": [], "cpu_m": []},
      "csi-driver-registrar": {"ws_mib": [], "rss_mib": [], "cpu_m": []},
      "gcs-fuse-csi-driver-webhook": {"ws_mib": [], "rss_mib": [], "cpu_m": []},
  }
  sidecar_per_pod = {"ws_mib": [], "rss_mib": [], "cpu_m": []}
  sidecar_80pod_total = {"ws_mib": [], "rss_mib": [], "cpu_m": []}
  prefetch_per_pod = {"ws_mib": [], "rss_mib": [], "cpu_m": []}
  prefetch_80pod_total = {"ws_mib": [], "rss_mib": [], "cpu_m": []}
  ioworker_per_pod = {"ws_mib": [], "rss_mib": [], "cpu_m": []}
  ioworker_80pod_total = {"ws_mib": [], "rss_mib": [], "cpu_m": []}

  node_metrics = {"ws_mib": [], "rss_mib": [], "cpu_m": []}
  net_tracks = []
  workload_net_tracks = []

  for sf in stat_files:
    try:
      with open(sf, "r", encoding="utf-8") as f:
        data = json.load(f)
    except (json.JSONDecodeError, OSError):
      continue

    node = data.get("node", {})
    node_cpu_nc = node.get("cpu", {}).get("usageNanoCores", 0) or 0
    node_ws_b = node.get("memory", {}).get("workingSetBytes", 0) or 0
    node_rss_b = node.get("memory", {}).get("rssBytes", 0) or 0
    node_metrics["cpu_m"].append(node_cpu_nc / 1e6)
    node_metrics["ws_mib"].append(node_ws_b / (1024 * 1024))
    node_metrics["rss_mib"].append(node_rss_b / (1024 * 1024))

    n_net = node.get("network", {})
    n_net_ts = parse_iso_ts(n_net.get("time", ""))
    n_rx = n_net.get("rxBytes", 0) or 0
    n_tx = n_net.get("txBytes", 0) or 0

    snap_sc_ws, snap_sc_rss, snap_sc_cpu = 0.0, 0.0, 0.0
    snap_sc_count = 0
    snap_pf_ws, snap_pf_rss, snap_pf_cpu = 0.0, 0.0, 0.0
    snap_pf_count = 0
    snap_io_ws, snap_io_rss, snap_io_cpu = 0.0, 0.0, 0.0
    snap_io_count = 0

    csi_rx, csi_tx, csi_net_ts = 0, 0, 0.0
    w_rx_sum, w_tx_sum, w_net_ts = 0, 0, 0.0
    w_pod_count = 0

    for pod in data.get("pods", []):
      pref = pod.get("podRef", {})
      p_name = pref.get("name", "")
      p_ns = pref.get("namespace", "")

      if p_name.startswith("gcsfusecsi-node-"):
        p_net = pod.get("network", {})
        csi_net_ts = parse_iso_ts(p_net.get("time", ""))
        csi_rx = p_net.get("rxBytes", 0) or 0
        csi_tx = p_net.get("txBytes", 0) or 0
        for c in pod.get("containers", []):
          cname = c.get("name", "")
          if cname in csi_containers:
            ws = (c.get("memory", {}).get("workingSetBytes", 0) or 0) / (1024 * 1024)
            rss = (c.get("memory", {}).get("rssBytes", 0) or 0) / (1024 * 1024)
            cpu = (c.get("cpu", {}).get("usageNanoCores", 0) or 0) / 1e6
            csi_containers[cname]["ws_mib"].append(ws)
            csi_containers[cname]["rss_mib"].append(rss)
            csi_containers[cname]["cpu_m"].append(cpu)

      elif p_name.startswith("gcs-fuse-csi-driver-webhook-"):
        for c in pod.get("containers", []):
          if c.get("name") == "gcs-fuse-csi-driver-webhook":
            ws = (c.get("memory", {}).get("workingSetBytes", 0) or 0) / (1024 * 1024)
            rss = (c.get("memory", {}).get("rssBytes", 0) or 0) / (1024 * 1024)
            cpu = (c.get("cpu", {}).get("usageNanoCores", 0) or 0) / 1e6
            csi_containers["gcs-fuse-csi-driver-webhook"]["ws_mib"].append(ws)
            csi_containers["gcs-fuse-csi-driver-webhook"]["rss_mib"].append(rss)
            csi_containers["gcs-fuse-csi-driver-webhook"]["cpu_m"].append(cpu)

      elif p_ns == "default" and p_name.startswith("gcsfuse-perf-"):
        p_net = pod.get("network", {})
        p_ts = parse_iso_ts(p_net.get("time", ""))
        if p_ts > w_net_ts:
          w_net_ts = p_ts
        w_rx_sum += p_net.get("rxBytes", 0) or 0
        w_tx_sum += p_net.get("txBytes", 0) or 0
        w_pod_count += 1

        for c in pod.get("containers", []):
          cname = c.get("name", "")
          ws = (c.get("memory", {}).get("workingSetBytes", 0) or 0) / (1024 * 1024)
          rss = (c.get("memory", {}).get("rssBytes", 0) or 0) / (1024 * 1024)
          cpu = (c.get("cpu", {}).get("usageNanoCores", 0) or 0) / 1e6
          if cname == "gke-gcsfuse-sidecar":
            sidecar_per_pod["ws_mib"].append(ws)
            sidecar_per_pod["rss_mib"].append(rss)
            sidecar_per_pod["cpu_m"].append(cpu)
            snap_sc_ws += ws
            snap_sc_rss += rss
            snap_sc_cpu += cpu
            snap_sc_count += 1
          elif cname == "gke-gcsfuse-metadata-prefetch":
            prefetch_per_pod["ws_mib"].append(ws)
            prefetch_per_pod["rss_mib"].append(rss)
            prefetch_per_pod["cpu_m"].append(cpu)
            snap_pf_ws += ws
            snap_pf_rss += rss
            snap_pf_cpu += cpu
            snap_pf_count += 1
          elif cname == "io-worker":
            ioworker_per_pod["ws_mib"].append(ws)
            ioworker_per_pod["rss_mib"].append(rss)
            ioworker_per_pod["cpu_m"].append(cpu)
            snap_io_ws += ws
            snap_io_rss += rss
            snap_io_cpu += cpu
            snap_io_count += 1

    if snap_sc_count > 0:
      sidecar_80pod_total["ws_mib"].append(snap_sc_ws)
      sidecar_80pod_total["rss_mib"].append(snap_sc_rss)
      sidecar_80pod_total["cpu_m"].append(snap_sc_cpu)
    if snap_pf_count > 0:
      prefetch_80pod_total["ws_mib"].append(snap_pf_ws)
      prefetch_80pod_total["rss_mib"].append(snap_pf_rss)
      prefetch_80pod_total["cpu_m"].append(snap_pf_cpu)
    if snap_io_count > 0:
      ioworker_80pod_total["ws_mib"].append(snap_io_ws)
      ioworker_80pod_total["rss_mib"].append(snap_io_rss)
      ioworker_80pod_total["cpu_m"].append(snap_io_cpu)

    net_tracks.append((n_net_ts, n_rx, n_tx, csi_net_ts, csi_rx, csi_tx))
    if w_pod_count > 0:
      workload_net_tracks.append((w_net_ts, w_rx_sum, w_tx_sum, w_pod_count))

  net_rates = {
      "node_rx_kbps": 0.0,
      "node_tx_kbps": 0.0,
      "csi_pod_rx_kbps": 0.0,
      "csi_pod_tx_kbps": 0.0,
      "workload_80pod_rx_kbps": 0.0,
      "workload_80pod_tx_kbps": 0.0,
  }
  if len(net_tracks) >= 2:
    t0, r0, x0, ct0, cr0, cx0 = net_tracks[0]
    t1, r1, x1, ct1, cr1, cx1 = net_tracks[-1]
    if t1 > t0:
      net_rates["node_rx_kbps"] = round((r1 - r0) / 1024.0 / (t1 - t0), 2)
      net_rates["node_tx_kbps"] = round((x1 - x0) / 1024.0 / (t1 - t0), 2)
    if ct1 > ct0:
      net_rates["csi_pod_rx_kbps"] = round((cr1 - cr0) / 1024.0 / (ct1 - ct0), 2)
      net_rates["csi_pod_tx_kbps"] = round((cx1 - cx0) / 1024.0 / (ct1 - ct0), 2)
  if len(workload_net_tracks) >= 2:
    wt0, wr0, wx0, _ = workload_net_tracks[0]
    wt1, wr1, wx1, _ = workload_net_tracks[-1]
    if wt1 > wt0:
      net_rates["workload_80pod_rx_kbps"] = round((wr1 - wr0) / 1024.0 / (wt1 - wt0), 2)
      net_rates["workload_80pod_tx_kbps"] = round((wx1 - wx0) / 1024.0 / (wt1 - wt0), 2)

  scrape_ms, scrape_kib, scrape_series = [], [], []
  scrape_errors = 0
  meta_file = os.path.join(phase_dir, "scrape_meta.jsonl")
  if os.path.exists(meta_file):
    with open(meta_file, "r", encoding="utf-8") as f:
      for line in f:
        if line.strip():
          row = json.loads(line)
          if row.get("status") == "error":
            scrape_errors += 1
          else:
            scrape_ms.append(float(row["scrape_ms"]))
            scrape_kib.append(float(row["bytes"]) / 1024.0)
            scrape_series.append(float(row["series_count"]))

  schema_audit = {}
  prom_files = [
      p
      for p in sorted(glob.glob(os.path.join(phase_dir, "csi_9920_*.prom")))
      if os.path.getsize(p) > 0
  ]
  if prom_files:
    last_prom = prom_files[-1]
    fam_set = set()
    type_header_count = 0
    help_header_count = 0
    vol_prefixed = set()
    grpc_fams = set()
    ctrl_types = set()
    pods_seen = set()
    with open(last_prom, "r", encoding="utf-8") as f:
      for line in f:
        if line.startswith("# HELP "):
          help_header_count += 1
        elif line.startswith("# TYPE "):
          type_header_count += 1
          parts = line.strip().split()
          if len(parts) >= 3:
            fn = parts[2]
            fam_set.add(fn)
            if fn.startswith("volume_"):
              vol_prefixed.add(fn)
            elif fn.startswith("grpc_"):
              grpc_fams.add(fn)
        elif not line.startswith("#") and line.strip():
          m_ctrl = re.search(r'k8s_controller_type="([^"]*)"', line)
          if m_ctrl:
            ctrl_types.add(m_ctrl.group(1))
          m_pod = re.search(r'pod_name="([^"]+)"', line)
          if m_pod:
            pods_seen.add(m_pod.group(1))
    schema_audit = {
        "total_families": len(fam_set),
        "type_header_count": type_header_count,
        "help_header_count": help_header_count,
        "volume_prefixed_families": len(vol_prefixed),
        "grpc_families": len(grpc_fams),
        "distinct_pods_scraped": len(pods_seen),
        "controller_types_observed": sorted(ctrl_types),
    }

  cadvisor_max_usage_mib: Dict[str, List[float]] = {
      "gcs-fuse-csi-driver": [],
      "gcsfusecsi-metrics-collector": [],
      "csi-driver-registrar": [],
  }
  cadvisor_files = sorted(glob.glob(os.path.join(phase_dir, "cadvisor_*.prom")))
  for cf in cadvisor_files:
    file_max: Dict[str, float] = {}
    with open(cf, "r", encoding="utf-8") as f:
      for line in f:
        if line.startswith("container_memory_max_usage_bytes{") and 'pod="gcsfusecsi-node-' in line:
          m_c = re.search(r'container="([^"]+)"', line)
          parts = line.strip().split()
          if m_c and len(parts) >= 2:
            cname = m_c.group(1)
            if cname in cadvisor_max_usage_mib:
              try:
                val_mib = round(
                    float(parts[-2] if len(parts) >= 3 else parts[1]) / (1024.0 * 1024.0),
                    3,
                )
                if val_mib > file_max.get(cname, -1.0):
                  file_max[cname] = val_mib
              except ValueError:
                pass
    for cname, val_mib in file_max.items():
      cadvisor_max_usage_mib[cname].append(val_mib)

  return {
      "csi_containers": {
          cname: {k: compute_stats(v) for k, v in metrics.items()}
          for cname, metrics in csi_containers.items()
      },
      "cadvisor_cgroup_peak_mib": {
          cname: compute_stats(vals) for cname, vals in cadvisor_max_usage_mib.items()
      },
      "sidecar_per_pod": {k: compute_stats(v) for k, v in sidecar_per_pod.items()},
      "sidecar_80pod_total": {k: compute_stats(v) for k, v in sidecar_80pod_total.items()},
      "prefetch_per_pod": {k: compute_stats(v) for k, v in prefetch_per_pod.items()},
      "prefetch_80pod_total": {k: compute_stats(v) for k, v in prefetch_80pod_total.items()},
      "ioworker_per_pod": {k: compute_stats(v) for k, v in ioworker_per_pod.items()},
      "ioworker_80pod_total": {k: compute_stats(v) for k, v in ioworker_80pod_total.items()},
      "node_metrics": {k: compute_stats(v) for k, v in node_metrics.items()},
      "network_rates": net_rates,
      "scrape_9920": {
          "duration_ms": compute_stats(scrape_ms),
          "payload_kib": compute_stats(scrape_kib),
          "series_count": compute_stats(scrape_series),
          "scrape_errors": scrape_errors,
      },
      "schema_audit": schema_audit,
  }


def discover_clusters(
    data_root: str, explicit_clusters: List[str]
) -> List[Tuple[str, str, Dict[str, Any]]]:
  results: List[Tuple[str, str, Dict[str, Any]]] = []
  if explicit_clusters:
    for item in explicit_clusters:
      if "=" in item:
        label, cl_name = item.split("=", 1)
      else:
        label, cl_name = item, item
      cl_dir = os.path.join(data_root, cl_name)
      meta_path = os.path.join(cl_dir, "target_metadata.json")
      meta = {}
      if os.path.exists(meta_path):
        with open(meta_path, "r", encoding="utf-8") as f:
          meta = json.load(f)
      results.append((label, cl_name, meta))
    return results

  if not os.path.isdir(data_root):
    return results

  for entry in sorted(os.listdir(data_root)):
    cl_dir = os.path.join(data_root, entry)
    if not os.path.isdir(cl_dir):
      continue
    meta_path = os.path.join(cl_dir, "target_metadata.json")
    meta = {}
    label = entry
    if os.path.exists(meta_path):
      with open(meta_path, "r", encoding="utf-8") as f:
        meta = json.load(f)
      label = meta.get("label", entry)
    results.append((label, entry, meta))
  return results


def render_markdown_report(summary: Dict[str, Any]) -> str:
  flavors = summary.get("flavors", {})
  if not flavors:
    return "# GCSFuse CSI Driver 80-Pod gRPC Metrics Performance Report\n\nNo cluster data found.\n"

  keys = list(flavors.keys())
  lines = [
      "# GCSFuse CSI Driver 80-Pod gRPC Metrics Memory & Performance Comparison",
      "",
      "## 1. Target Configurations",
      "",
      "| Target Label | Cluster Name | Git Ref / Branch | Memory Limit | Staging Image Version |",
      "| :--- | :--- | :--- | :--- | :--- |",
  ]
  for k in keys:
    f = flavors[k]
    meta = f.get("metadata", {})
    lines.append(
        f"| **`{k}`** | `{f.get('cluster_name', k)}` | `{meta.get('git_ref', 'N/A')}` | "
        f"`{meta.get('memory_limit', '100Mi')}` | `{meta.get('staging_version', 'N/A')}` |"
    )

  def get_phase_row_cells(phase_key: str, extractor) -> str:
    return " | ".join(extractor(flavors[k].get(phase_key, {}), flavors[k]) for k in keys)

  header_cols = " | ".join(f"**`{k}`**" for k in keys)
  sep_cols = " | ".join(":---" for _ in keys)

  for phase_title, phase_key, status_key, total_scrapes in [
      ("2. Phase A: Idle Baseline (0 Workload Pods)", "phase_a_idle", "pod_status_after_phase_b", 1),
      (
          "3. Phase B: 80 Pods + Standard 60s Scrape Cadence (6 Samples / 5m)",
          "phase_b_60s",
          "pod_status_after_phase_b",
          6,
      ),
      (
          "4. Phase C: 80 Pods + Stress 10s Scrape Cadence (12 Samples / 2m)",
          "phase_c_10s",
          "pod_status_after_phase_c",
          12,
      ),
  ]:
    lines.extend([
        "",
        f"## {phase_title}",
        "",
        f"| Metric (`gcs-fuse-csi-driver`) | {header_cols} |",
        f"| :--- | {sep_cols} |",
    ])

    lines.append(
        "| **Working Set Mean / Max (MiB)** | "
        + get_phase_row_cells(
            phase_key,
            lambda p, _: (
                f"{p.get('csi_containers', {}).get('gcs-fuse-csi-driver', {}).get('ws_mib', {}).get('mean', 0):.2f} / "
                f"**{p.get('csi_containers', {}).get('gcs-fuse-csi-driver', {}).get('ws_mib', {}).get('max', 0):.2f} MiB**"
            ),
        )
        + " |"
    )
    lines.append(
        "| **RSS Mean / Max (MiB)** | "
        + get_phase_row_cells(
            phase_key,
            lambda p, _: (
                f"{p.get('csi_containers', {}).get('gcs-fuse-csi-driver', {}).get('rss_mib', {}).get('mean', 0):.2f} / "
                f"**{p.get('csi_containers', {}).get('gcs-fuse-csi-driver', {}).get('rss_mib', {}).get('max', 0):.2f} MiB**"
            ),
        )
        + " |"
    )
    lines.append(
        "| **Kernel Cgroup `memory.peak` (MiB)** | "
        + get_phase_row_cells(
            phase_key,
            lambda p, _: (
                f"**{p.get('cadvisor_cgroup_peak_mib', {}).get('gcs-fuse-csi-driver', {}).get('max', 0):.2f} MiB**"
            ),
        )
        + " |"
    )
    if phase_key != "phase_a_idle":
      lines.append(
          "| **OOMKills / Container Restarts** | "
          + get_phase_row_cells(
              phase_key,
              lambda _, f, sk=status_key: (
                  f"**{f.get(sk, {}).get('gcs-fuse-csi-driver', {}).get('restartCount', 0)}** "
                  f"(`{f.get(sk, {}).get('gcs-fuse-csi-driver', {}).get('lastTerminatedReason', '') or 'None'}`)"
              ),
          )
          + " |"
      )
    lines.append(
        "| **CPU Mean / Max (mCPU)** | "
        + get_phase_row_cells(
            phase_key,
            lambda p, _: (
                f"{p.get('csi_containers', {}).get('gcs-fuse-csi-driver', {}).get('cpu_m', {}).get('mean', 0):.1f}m / "
                f"**{p.get('csi_containers', {}).get('gcs-fuse-csi-driver', {}).get('cpu_m', {}).get('max', 0):.1f}m**"
            ),
        )
        + " |"
    )
    lines.append(
        f"| **`:9920/metrics` Scrape Success / Failed** | "
        + get_phase_row_cells(
            phase_key,
            lambda p, _, ts=total_scrapes: (
                f"{p.get('scrape_9920', {}).get('duration_ms', {}).get('count', 0)}/{ts} ok "
                f"({p.get('scrape_9920', {}).get('scrape_errors', 0)} failed)"
            ),
        )
        + " |"
    )
    lines.append(
        "| **`:9920/metrics` Scrape Duration Mean / Max (ms)** | "
        + get_phase_row_cells(
            phase_key,
            lambda p, _: (
                f"{p.get('scrape_9920', {}).get('duration_ms', {}).get('mean', 0):.1f} / "
                f"**{p.get('scrape_9920', {}).get('duration_ms', {}).get('max', 0):.1f} ms**"
            ),
        )
        + " |"
    )
    lines.append(
        "| **`:9920/metrics` Payload Mean (MiB) / Series** | "
        + get_phase_row_cells(
            phase_key,
            lambda p, _: (
                f"{p.get('scrape_9920', {}).get('payload_kib', {}).get('mean', 0) / 1024.0:.2f} MiB "
                f"({int(p.get('scrape_9920', {}).get('series_count', {}).get('mean', 0)):,} series)"
            ),
        )
        + " |"
    )
    if phase_key != "phase_a_idle":
      lines.append(
          "| **Schema `# TYPE` Headers / Distinct Families** | "
          + get_phase_row_cells(
              phase_key,
              lambda p, _: (
                  f"`{p.get('schema_audit', {}).get('type_header_count', 0)}` headers / "
                  f"`{p.get('schema_audit', {}).get('total_families', 0)}` families"
              ),
          )
          + " |"
      )

  lines.append("")
  return "\n".join(lines)


def main() -> None:
  parser = argparse.ArgumentParser(
      description="Analyze 80-Pod GCSFuse gRPC Metrics CSI Driver benchmark telemetry."
  )
  parser.add_argument(
      "--data-dir",
      required=True,
      help="Path to the data directory containing per-cluster subdirectories.",
  )
  parser.add_argument(
      "--output-json",
      default="",
      help="Path to write structured JSON summary (default: <data-dir>/../metrics_resource_impact_summary.json).",
  )
  parser.add_argument(
      "--output-markdown",
      default="",
      help="Path to write Markdown comparison report (optional).",
  )
  parser.add_argument(
      "--cluster",
      action="append",
      default=[],
      help="Explicit cluster mapping '<label>=<cluster_dir_name>' (repeatable; auto-discovered if omitted).",
  )
  args = parser.parse_args()

  data_root = os.path.abspath(args.data_dir)
  out_json = args.output_json or os.path.join(
      os.path.dirname(data_root), "metrics_resource_impact_summary.json"
  )

  discovered = discover_clusters(data_root, args.cluster)
  summary: Dict[str, Any] = {"limits_reference": LIMITS, "flavors": {}}

  for label, cl_name, meta in discovered:
    cl_dir = os.path.join(data_root, cl_name)
    if not os.path.isdir(cl_dir):
      continue
    summary["flavors"][label] = {
        "cluster_name": cl_name,
        "metadata": meta,
        "pod_status_after_phase_b": parse_pod_status(
            os.path.join(cl_dir, "csi_node_pod_status_phase_b.json")
        ),
        "pod_status_after_phase_c": parse_pod_status(
            os.path.join(cl_dir, "csi_node_pod_status.json")
        ),
        "phase_a_idle": analyze_phase_dir(os.path.join(cl_dir, "phase_a")),
        "phase_b_60s": analyze_phase_dir(os.path.join(cl_dir, "phase_b_60s")),
        "phase_c_10s": analyze_phase_dir(os.path.join(cl_dir, "phase_c_10s")),
    }

  with open(out_json, "w", encoding="utf-8") as f:
    json.dump(summary, f, indent=2)
  print(f"Saved structured JSON summary to {out_json}")

  md_report = render_markdown_report(summary)
  if args.output_markdown:
    with open(args.output_markdown, "w", encoding="utf-8") as f:
      f.write(md_report)
    print(f"Saved Markdown report to {args.output_markdown}")

  print("\n" + md_report)


if __name__ == "__main__":
  main()
