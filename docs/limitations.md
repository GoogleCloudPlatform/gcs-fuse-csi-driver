# Cloud Storage FUSE CSI Driver Limitations

## Cloud Storage FUSE Limitations
See Cloud Storage FUSE documentation [Key differences from a POSIX file system](https://cloud.google.com/storage/docs/gcs-fuse).

## Known Issues
- When the Cloud Storage FUSE instance is terminated, you will see the error message `Transport endpoint is not connected` in your workload log. The only way to fix the issue is to restart your workload Pod.