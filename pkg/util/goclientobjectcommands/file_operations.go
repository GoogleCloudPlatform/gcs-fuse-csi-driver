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
	"compress/gzip"
	"fmt"
	"log"
	"os"
	"syscall"
)

const (
	FilePermission_0400 = 0400
	// TmpDirectory specifies the directory where temporary files will be created.
	// In this case, we are using the system's default temporary directory.
	TmpDirectory = "/tmp"
)

func OpenFileAsReadonly(filepath string) (*os.File, error) {
	f, err := os.OpenFile(filepath, os.O_RDONLY|syscall.O_DIRECT, FilePermission_0400)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s as readonly: %v", filepath, err)
	}

	return f, nil
}

// Deprecated: please use CloseFileShouldNotThrowError instead.
func CloseFile(file *os.File) {
	if err := file.Close(); err != nil {
		log.Fatalf("error in closing: %v", err)
	}
}

// Creates a temporary file (name-collision-safe) in /tmp with given content.
// If gzipCompress is true, output file is a gzip-compressed file.
// Caller is responsible for deleting the created file when done using it.
// Failure cases:
// 1. os.CreateTemp() returned error or nil handle
// 2. gzip.NewWriter() returned nil handle
// 3. Failed to write the content to the created temp file
func CreateLocalTempFile(content string, gzipCompress bool) (string, error) {
	// create appropriate name template for temp file
	filenameTemplate := "testfile-*.txt"
	if gzipCompress {
		filenameTemplate += ".gz"
	}

	f, err := os.CreateTemp(TmpDirectory, filenameTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to create tempfile for template %s: %w", filenameTemplate, err)
	}
	if f == nil {
		return "", fmt.Errorf("nil file handle returned from os.CreateTemp")
	}
	defer CloseFile(f)
	if gzipCompress {
		return writeGzipToFile(f, f.Name(), content, len(content))
	}

	return writeTextToFile(f, f.Name(), content, len(content))
}

func writeGzipToFile(f *os.File, filepath, content string, contentSize int) (string, error) {
	w := gzip.NewWriter(f)
	if w == nil {
		return "", fmt.Errorf("failed to create gzip writer for file %s", filepath)
	}
	defer func() {
		if err := w.Close(); err != nil {
			log.Printf("failed to close gzip writer for file %s: %v", filepath, err)
		}
	}()

	n, err := w.Write([]byte(content))
	if err != nil {
		return "", fmt.Errorf("failed to write content to %s using gzip-writer: %w", filepath, err)
	}
	if n != contentSize {
		return "", fmt.Errorf("failed to write to gzip file %s. Content-size: %d bytes, wrote = %d bytes", filepath, contentSize, n)
	}

	return filepath, nil
}

func writeTextToFile(f *os.File, filepath, content string, contentSize int) (string, error) {
	n, err := f.WriteString(content)
	if err != nil {
		return "", err
	}
	if n != contentSize {
		return "", fmt.Errorf("failed to write to text file %s. Content-size: %d bytes, wrote = %d bytes", filepath, contentSize, n)
	}

	return filepath, nil
}
