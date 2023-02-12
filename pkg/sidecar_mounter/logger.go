/*
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
	"regexp"
	"time"
)

type stdoutWriter struct {
	w          io.Writer
	volumeName string
}

type stderrWriter struct {
	w          io.Writer
	volumeName string
	errorFile  string
}

func NewStdoutWriter(w io.Writer, volumeName string) io.Writer {
	return &stdoutWriter{w: w, volumeName: volumeName}
}

func NewStderrWriter(w io.Writer, volumeName string, errorFile string) io.Writer {
	return &stderrWriter{w: w, volumeName: volumeName, errorFile: errorFile}
}

// Write writes the output from a gcsfuse instance to stdout with the volume name added as prefix.
func (f *stdoutWriter) Write(p []byte) (n int, err error) {
	msg := string(p)
	exp := regexp.MustCompile(`(^|\n)(N|I|E|D)(\d*|$)`)
	msg = exp.ReplaceAllString(msg, fmt.Sprintf("${1}[%v] ${2}${3}", f.volumeName))
	_, err = f.w.Write([]byte(msg))
	if err != nil {
		return
	}
	n = len(p)
	return
}

// Write writes any error message of a gcsfuse instance to stderr with the volume name added as prefix.
// Meanwhile, it writes the error message to a given local file.
func (f *stderrWriter) Write(p []byte) (n int, err error) {
	msg := fmt.Sprintf("[%v] E%v %v", f.volumeName, time.Now().Format("0102 15:04:05.000000"), string(p))
	_, err = f.w.Write([]byte(msg))
	if err != nil {
		return
	}

	var ef *os.File
	if f.errorFile != "" {
		ef, err = os.OpenFile(f.errorFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		defer ef.Close()
		if _, err = ef.WriteString(msg); err != nil {
			return
		}
	}
	n = len(p)
	return
}
