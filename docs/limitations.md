# GCS FUSE CSI Driver Limitations

## GCS Fuse Limitations
See GCS Fuse documentation [Key differences from a POSIX file system](https://cloud.google.com/storage/docs/gcs-fuse).

## Known Issues
- When the GCS Fuse instance is terminated, you will see the error message `Transport endpoint is not connected` in your workload log. The only way to fix the issue is to restart your workload Pod.