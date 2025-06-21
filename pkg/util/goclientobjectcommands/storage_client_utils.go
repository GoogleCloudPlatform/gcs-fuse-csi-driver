/*
Copyright 2025 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package goclientobjectcommands

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"cloud.google.com/go/storage"
	"cloud.google.com/go/storage/experimental"
)

// NewGCSClient creates and returns a new Google Cloud Storage client.
func NewGCSClient(ctx context.Context, isZBEnabled bool) (*storage.Client, error) {
	var client *storage.Client
	var err error
	if isZBEnabled {
		client, err = storage.NewGRPCClient(ctx, experimental.WithGRPCBidiReads())
	} else {
		client, err = storage.NewClient(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	return client, nil
}

// UploadGcsObject uploads a local file to a specified GCS bucket and object.
// Handles gzip compression if requested.
func UploadGcsObject(ctx context.Context, client *storage.Client, localPath, bucketName, objectName string, uploadGzipEncoded bool, zbEnabled bool) error {
	// Create a writer to upload the object.
	obj := client.Bucket(bucketName).Object(objectName)
	w, err := NewWriter(ctx, obj, client, zbEnabled)
	if err != nil {
		return fmt.Errorf("failed to open writer for GCS object gs://%s/%s: %w", bucketName, objectName, err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			log.Printf("Failed to close GCS object gs://%s/%s: %v", bucketName, objectName, err)
		}
	}()

	filePathToUpload := localPath
	// Set content encoding if gzip compression is needed.
	if uploadGzipEncoded {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return err
		}

		content := string(data)
		if filePathToUpload, err = CreateLocalTempFile(content, true); err != nil {
			return fmt.Errorf("failed to create local gzip file from %s for upload to bucket: %w", localPath, err)
		}
		defer func() {
			if removeErr := os.Remove(filePathToUpload); removeErr != nil {
				log.Printf("Error removing temporary gzip file %s: %v", filePathToUpload, removeErr)
			}
		}()
	}

	// Open the local file for reading.
	f, err := OpenFileAsReadonly(filePathToUpload)
	if err != nil {
		return fmt.Errorf("failed to open local file %s: %w", filePathToUpload, err)
	}
	defer CloseFile(f)

	// Copy the file contents to the object writer.
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("failed to copy file %s to gs://%s/%s: %w", localPath, bucketName, objectName, err)
	}
	return nil
}

// NewWriter is a wrapper over storage.NewWriter which
// extends support to zonal buckets.
func NewWriter(ctx context.Context, o *storage.ObjectHandle, client *storage.Client, zbEnabled bool) (wc *storage.Writer, err error) {
	wc = o.NewWriter(ctx)
	wc.FinalizeOnClose = true

	// Changes specific to zonal bucket
	var attrs *storage.BucketAttrs
	attrs, err = client.Bucket(o.BucketName()).Attrs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get attributes for bucket %q: %w", o.BucketName(), err)
	}
	if attrs.StorageClass == "RAPID" {
		if zbEnabled {
			// Zonal bucket writers require append-flag to be set.
			wc.Append = true
		} else {
			return nil, fmt.Errorf("found zonal bucket %q in non-zonal e2e test run (--zonal=false)", o.BucketName())
		}
	}

	return
}
