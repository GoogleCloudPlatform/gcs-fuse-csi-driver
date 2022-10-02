# GCS CSI Driver Limitations

## GCS Fuse Limitations
See GCS Fuse documentation [Key differences from a POSIX file system](https://cloud.google.com/storage/docs/gcs-fuse).

## Known Issues
- In very rare occasion when the GCS Fuse instance is terminated, you will see the error message `Transport endpoint is not connected` in your workload log. Restart your workload Pod will fix the issue.