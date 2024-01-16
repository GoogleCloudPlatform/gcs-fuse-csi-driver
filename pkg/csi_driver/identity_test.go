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

package driver

import (
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
)

const (
	testDriver  = "test-driver"
	testVersion = "test-version"
)

func initTestIdentityServer(t *testing.T) csi.IdentityServer {
	t.Helper()

	return newIdentityServer(initTestDriver(t, nil))
}

func TestGetPluginInfo(t *testing.T) {
	t.Parallel()
	s := initTestIdentityServer(t)

	resp, err := s.GetPluginInfo(context.TODO(), nil)
	if err != nil {
		t.Fatalf("GetPluginInfo failed: %v", err)
	}

	if resp == nil {
		t.Fatalf("GetPluginInfo resp is nil")
	}

	if resp.GetName() != testDriver {
		t.Errorf("got driver name %v", resp.GetName())
	}

	if resp.GetVendorVersion() != testVersion {
		t.Errorf("got driver version %v", resp.GetName())
	}
}

func TestGetPluginCapabilities(t *testing.T) {
	t.Parallel()
	s := initTestIdentityServer(t)

	resp, err := s.GetPluginCapabilities(context.TODO(), nil)
	if err != nil {
		t.Fatalf("GetPluginCapabilities failed: %v", err)
	}

	if resp == nil {
		t.Fatalf("GetPluginCapabilities resp is nil")
	}

	if len(resp.GetCapabilities()) != 1 {
		t.Fatalf("returned %v capabilities", len(resp.GetCapabilities()))
	}

	if resp.GetCapabilities()[0].GetType() == nil {
		t.Fatalf("returned nil capability type")
	}

	service := resp.GetCapabilities()[0].GetService()
	if service == nil {
		t.Fatalf("returned nil capability service")
	}

	if serviceType := service.GetType(); serviceType != csi.PluginCapability_Service_CONTROLLER_SERVICE {
		t.Fatalf("returned %v capability service", serviceType)
	}
}

func TestProbe(t *testing.T) {
	t.Parallel()
	s := initTestIdentityServer(t)

	resp, err := s.Probe(context.TODO(), nil)
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}

	if resp == nil {
		t.Fatalf("Probe resp is nil")
	}
}
