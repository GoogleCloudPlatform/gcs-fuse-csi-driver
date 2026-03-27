/*
Copyright 2018 The Kubernetes Authors.
Copyright 2022 Google LLC

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

package sidecarmounter

import (
	"fmt"
	"io"
	"os"

	"k8s.io/klog/v2"
)

type stderrWriterInterface interface {
	io.Writer
	WriteMsg(errMsg string)
}

type stderrWriter struct {
	errorFile string
}

func NewErrorWriter(errorFile string) stderrWriterInterface {
	return &stderrWriter{errorFile: errorFile}
}

// Write writes the error message to a given local file.
func (w *stderrWriter) Write(msg []byte) (int, error) {
	if w.errorFile != "" {
		errFile, err := os.OpenFile(w.errorFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, fmt.Errorf("failed to open file: %w", err)
		}
		defer errFile.Close()

		if _, err = errFile.Write(msg); err != nil {
			return 0, fmt.Errorf("failed to write bytes: %w", err)
		}
	}

	return len(msg), nil
}

// WriteMsg calls Write func and handles errors.
func (w *stderrWriter) WriteMsg(errMsg string) {
	klog.Error(errMsg)
	if _, e := w.Write([]byte(errMsg)); e != nil {
		klog.Errorf("failed to write the error message %q: %v", errMsg, e)
	}
}
