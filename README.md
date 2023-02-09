# Google Cloud Storage FUSE CSI Driver
The Google Cloud Storage FUSE Container Storage Interface (CSI) Plugin.

> WARNNING: This project is still under development, please do not use the driver in production environments.

## Project Overview
GCS Fuse CSI Driver allows Kubernetes applications to upload and download [Google Cloud Storage (GCS)](https://cloud.google.com/storage) objects using standard file system semantics within Pods running on GKE. The CSI driver relies on [GCS Fuse](https://cloud.google.com/storage/docs/gcs-fuse) to mount Cloud Storage buckets as file systems on GKE nodes.

## Project Status
Status: internal test

## Get Started
- [GCS FUSE CSI Driver Installation](./docs/installation.md)
- [GCS FUSE CSI Driver Usage](./docs/usage.md)
- [Example Applications](./examples/README.md)

## Kubernetes Development
Refer to the [GCS FUSE CSI Driver Development Guide](./docs/development.md).