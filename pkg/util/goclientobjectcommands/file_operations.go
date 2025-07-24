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
	"fmt"
	"log"
	"os"
	"syscall"
)

const (
	FilePermission_0400 = 0400
)

func OpenFileAsReadonly(filepath string) (*os.File, error) {
	f, err := os.OpenFile(filepath, os.O_RDONLY|syscall.O_DIRECT, FilePermission_0400)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s as readonly: %v", filepath, err)
	}

	return f, nil
}

// Todo: change to CloseFileShouldNotThrowError
func CloseFile(file *os.File) {
	if err := file.Close(); err != nil {
		log.Fatalf("error in closing: %v", err)
	}
}
