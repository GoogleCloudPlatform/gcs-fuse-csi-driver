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

package driver

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

type fakeNetworkManager struct {
	rtTables   string
	devices    []LinkDevice
	routes     []Route
	rules      []Rule
	poisonedIP string
	mutex      sync.Mutex
}

var _ NetworkManager = &fakeNetworkManager{}

func (mgr *fakeNetworkManager) Lock() {
	mgr.mutex.Lock()
}

func (mgr *fakeNetworkManager) Unlock() {
	mgr.mutex.Unlock()
}

func (mgr *fakeNetworkManager) GetRouteTable() (string, error) {
	return mgr.rtTables, nil
}

func (mgr *fakeNetworkManager) UpdateRouteTable(line string) error {
	mgr.rtTables += line
	return nil
}

func (mgr *fakeNetworkManager) ListDevices() ([]LinkDevice, error) {
	return mgr.devices, nil
}

func (mgr *fakeNetworkManager) ListRoutesForDevice(device string) ([]Route, error) {
	routes := []Route{}
	for _, r := range mgr.routes {
		if r.Device == device {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

func (mgr *fakeNetworkManager) ListTables() (map[int]bool, error) {
	tables := map[int]bool{}
	for _, r := range mgr.routes {
		tables[r.Table] = true
	}
	return tables, nil
}

func (mgr *fakeNetworkManager) ListRulesForTable(table int) ([]Rule, error) {
	rules := []Rule{}
	for _, r := range mgr.rules {
		if r.Table == table {
			rules = append(rules, r)
		}
	}
	return rules, nil
}

func (mgr *fakeNetworkManager) ListRoutesForTable(table int) ([]Route, error) {
	routes := []Route{}
	for _, r := range mgr.routes {
		if r.Table == table {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

func (mgr *fakeNetworkManager) AddRule(table int, sourceIP string) error {
	if sourceIP == mgr.poisonedIP {
		return fmt.Errorf("Poisoned source IP in AddRule")
	}
	mgr.rules = append(mgr.rules, Rule{Table: table, Source: sourceIP + "/32"})
	return nil
}

func (mgr *fakeNetworkManager) AddRoute(table int, gatewayIP, device string) error {
	if gatewayIP == mgr.poisonedIP {
		return fmt.Errorf("Poisoned gateway IP in AddRoute")
	}
	mgr.routes = append(mgr.routes, Route{
		Table:   table,
		Gateway: gatewayIP,
		Device:  device,
	})
	return nil
}

func TestTableNames(t *testing.T) {
	for _, tc := range []struct {
		name       string
		device     string
		expectedId int
		devices    []LinkDevice
		routes     []Route
		rules      []Rule
		rtTables   string
	}{
		{
			name:       "existing table",
			device:     "eth0",
			expectedId: 101,
			rtTables:   "# table list\nignore me gcsfusecsi_eth0\n99 foobar\n\n101 gcsfusecsi_eth0\n256 main",
		},
		{
			name:       "existing table in comment",
			device:     "eth0",
			expectedId: 101,
			rtTables:   "# table list\nignore me\n99 foobar\n101 gcsfusecsi_eth0  # this has been commented\n",
		},
		{
			name:       "ignored table in comment",
			device:     "eth0",
			expectedId: 50,
			rtTables:   "# table list\nignore me\n99 foobar\none-hundred gcsfusecsi_eth0  # ignore this\n",
		},
		{
			name:       "new table",
			device:     "eth0",
			expectedId: 50,
		},
		{
			name:       "new table with table conflict",
			device:     "eth0",
			expectedId: 51,
			rtTables:   "50 foobar",
		},
		{
			name:       "new table with route conflict",
			device:     "eth0",
			expectedId: 51,
			routes: []Route{
				{Table: 50},
				{Table: 52},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr := &fakeNetworkManager{
				rtTables: tc.rtTables,
				devices:  tc.devices,
				routes:   tc.routes,
				rules:    tc.rules,
			}
			id, err := getGcsFuseTableId(mgr, tc.device)
			if err != nil {
				t.Errorf("Unexpected error %v", err)
			}
			if id != tc.expectedId {
				t.Errorf("Expected %d, got %d", tc.expectedId, id)
			}
		})
	}
}

func TestTableUpdate(t *testing.T) {
	mgr := &fakeNetworkManager{}
	id, err := getGcsFuseTableId(mgr, "eth0")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if id != 50 {
		t.Errorf("Expected 50, got %d", id)
	}
	id, err = getGcsFuseTableId(mgr, "eth1")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if id != 51 {
		t.Errorf("Expected 51, got %d", id)
	}
}

func TestGetDeviceForNumaNode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		node           int
		devices        []LinkDevice
		expectedDevice string
		expectError    bool
	}{
		{
			name:        "no device",
			node:        0,
			expectError: true,
		},
		{
			name: "no node",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "gve",
					NumaNode: 1,
				},
			},
			node:        0,
			expectError: true,
		},
		{
			name: "non-gve device",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "virtio_net",
					NumaNode: 0,
				},
			},
			node:           0,
			expectedDevice: "eth0",
		},
		{
			name: "matching device",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "gve",
					NumaNode: 0,
				},
				{
					Name:     "eth1",
					Driver:   "gve",
					NumaNode: 1,
				},
			},
			node:           0,
			expectedDevice: "eth0",
		},
		{
			name: "matching device, take 2",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "gve",
					NumaNode: 0,
				},
				{
					Name:     "eth1",
					Driver:   "gve",
					NumaNode: 1,
				},
			},
			node:           1,
			expectedDevice: "eth1",
		},
		{
			name: "two matching devices, take the gve",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "virtio_net",
					NumaNode: 0,
				},
				{
					Name:     "eth1",
					Driver:   "gve",
					NumaNode: 0,
				},
			},
			node:           0,
			expectedDevice: "eth1",
		},
		{
			name: "two matching gve devices, take the first",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "gve",
					NumaNode: 0,
				},
				{
					Name:     "eth1",
					Driver:   "gve",
					NumaNode: 0,
				},
			},
			node:           0,
			expectedDevice: "eth0",
		},
		{
			name: "two matching non-gve devices, take the first",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "virtio_net",
					NumaNode: 0,
				},
				{
					Name:     "eth1",
					Driver:   "virtio_net",
					NumaNode: 0,
				},
			},
			node:           0,
			expectedDevice: "eth0",
		},
		{
			name: "not numa-aligned, but two devices",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "gve",
					NumaNode: -1,
				},
				{
					Name:     "eth1",
					Driver:   "gve",
					NumaNode: -1,
				},
			},
			node:           1,
			expectedDevice: "eth1",
		},
		{
			name: "not numa-aligned, only one device",
			devices: []LinkDevice{
				{
					Name:     "eth0",
					Driver:   "gve",
					NumaNode: -1,
				},
			},
			node:        1,
			expectError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr := &fakeNetworkManager{devices: tc.devices}
			device, _, err := GetDeviceForNumaNode(mgr, tc.node)
			if tc.expectError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if tc.expectError {
				return
			}
			if !tc.expectError && err != nil {
				t.Errorf("Expected no error but got %v", err)
			}
			if device != tc.expectedDevice {
				t.Errorf("Expected %s but got %s", tc.expectedDevice, device)
			}
		})
	}
}

func TestSourceRouteForDevice(t *testing.T) {
	for _, tc := range []struct {
		name              string
		device            string
		devices           []LinkDevice
		routes            []Route
		rules             []Rule
		rtTables          string
		expectedNewRoutes []Route
		expectedNewRules  []Rule
		expectedSource    string
		expectError       bool
	}{
		{
			name:              "no existing routes",
			device:            "eth0",
			devices:           []LinkDevice{{Name: "eth0", Driver: "gve"}},
			routes:            []Route{{Device: "eth0", Gateway: "10.128.0.1", Source: "10.128.0.13", Table: 254}},
			expectedNewRoutes: []Route{{Device: "eth0", Gateway: "10.128.0.1", Table: 50}},
			expectedNewRules:  []Rule{{Source: "10.128.0.13/32", Table: 50}},
			expectedSource:    "10.128.0.13",
		},
		{
			name:     "existing routes",
			device:   "eth0",
			devices:  []LinkDevice{{Name: "eth0", Driver: "gve"}},
			rtTables: "50 gcsfusecsi_eth0",
			routes: []Route{
				{Device: "eth0", Gateway: "10.128.0.1", Source: "10.128.0.13", Table: 254},
				{Device: "eth0", Gateway: "10.128.0.1", Table: 50},
			},
			rules:          []Rule{{Source: "10.128.0.13/32", Table: 50}},
			expectedSource: "10.128.0.13",
		},
		{
			name:    "other routes",
			device:  "eth0",
			devices: []LinkDevice{{Name: "eth0", Driver: "gve"}},
			routes: []Route{
				{Device: "eth0", Gateway: "10.128.0.1", Source: "10.128.0.13", Table: 254},
				{Device: "none", Gateway: "44.128.0.1", Source: "60.128.0.13", Table: 254},
				{Device: "local", Source: "60.128.0.13", Table: 50},
			},
			expectedNewRoutes: []Route{{Device: "eth0", Gateway: "10.128.0.1", Table: 51}},
			expectedNewRules:  []Rule{{Source: "10.128.0.13/32", Table: 51}},
			expectedSource:    "10.128.0.13",
		},
		{
			name:     "route no rule",
			device:   "eth0",
			devices:  []LinkDevice{{Name: "eth0", Driver: "gve"}},
			rtTables: "51 gcsfusecsi_eth0",
			routes: []Route{
				{Device: "eth0", Gateway: "10.128.0.1", Source: "10.128.0.13", Table: 254},
				{Device: "none", Gateway: "44.128.0.1", Source: "60.128.0.13", Table: 254},
				{Device: "eth0", Gateway: "10.128.0.1", Table: 51},
				{Device: "local", Source: "60.128.0.13", Table: 50},
			},
			expectedNewRules: []Rule{{Source: "10.128.0.13/32", Table: 51}},
			expectedSource:   "10.128.0.13",
		},
		{
			name:     "rule no route",
			device:   "eth0",
			devices:  []LinkDevice{{Name: "eth0", Driver: "gve"}},
			rtTables: "51 gcsfusecsi_eth0",
			routes: []Route{
				{Device: "eth0", Gateway: "10.128.0.1", Source: "10.128.0.13", Table: 254},
				{Device: "none", Gateway: "44.128.0.1", Source: "60.128.0.13", Table: 254},
				{Device: "local", Source: "60.128.0.13", Table: 50},
			},
			rules:             []Rule{{Source: "10.128.0.13/32", Table: 51}},
			expectedNewRoutes: []Route{{Device: "eth0", Gateway: "10.128.0.1", Table: 51}},
			expectedSource:    "10.128.0.13",
		},
		{
			name:        "weird device",
			device:      "wacky",
			devices:     []LinkDevice{{Name: "wacky", Driver: "woo-hoo!"}},
			expectError: true,
		},
		{
			name:        "missing device",
			device:      "eth0",
			expectError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr := &fakeNetworkManager{
				devices:  tc.devices,
				routes:   tc.routes,
				rules:    tc.rules,
				rtTables: tc.rtTables,
			}
			expectedRoutes := []Route{}
			for _, r := range tc.routes {
				expectedRoutes = append(expectedRoutes, r)
			}
			for _, r := range tc.expectedNewRoutes {
				expectedRoutes = append(expectedRoutes, r)
			}
			expectedRules := []Rule{}
			for _, r := range tc.rules {
				expectedRules = append(expectedRules, r)
			}
			for _, r := range tc.expectedNewRules {
				expectedRules = append(expectedRules, r)
			}

			source, err := AddSourceRouteForDevice(mgr, "eth0")
			if tc.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if tc.expectError {
				return
			}
			if !tc.expectError && err != nil {
				t.Errorf("expected no error but got %v", err)
			}
			if source != tc.expectedSource {
				t.Errorf("expected source %s but got %s", tc.expectedSource, source)
			}
			if !reflect.DeepEqual(append([]Route{}, mgr.routes...), expectedRoutes) {
				t.Errorf("Bad routes, expected %+v, got %+v", expectedRoutes, mgr.routes)
			}
			if !reflect.DeepEqual(append([]Rule{}, mgr.rules...), expectedRules) {
				t.Errorf("Bad rules, expected %+v, got %+v", expectedRules, mgr.rules)
			}
		})
	}
}
