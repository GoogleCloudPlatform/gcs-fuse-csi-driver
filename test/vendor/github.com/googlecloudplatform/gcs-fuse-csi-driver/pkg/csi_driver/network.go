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
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
)

const (
	gcsFuseTableName = "gcsfusecsi"
	iprouteTableName = "/etc/iproute2/rt_tables"
	sysDevTemplate   = "/sys/class/net/%s/device/numa_node"
	startingTableId  = 50
	maxTableId       = 252 // default, main and local table ids are 253-255.
	rulePriority     = 10000
)

// LinkDevice is information for a particular interface like eth0.
type LinkDevice struct {
	Name string

	// Driver is the driver name of the device, eg gve or virtio_net.
	Driver string

	// NumaNode is the node of the device, or -1 if no affinity.
	NumaNode int
}

// Route is a simplification of netlink Route.
type Route struct {
	Device  string
	Gateway string
	Source  string
	Table   int
}

// Rule is a simplification of netlink Rule.
type Rule struct {
	Source string
	Table  int
}

// NetworkManager sets up routing tables to configure NIC usage. It should be used with
// GetDeviceForNumaNode and AddSourceRouteForDevice; its purpose is to split application
// logic from system manipulation, for testing.
//
// The routines are not synchronized, because, eg, the combined operation of GetRouteTable and
// UpdateRouteTable need to be collectively atomic. The Lock and Unlock functions expose a mutex
// that can be used to serialize operations for a NetworkManager.
type NetworkManager interface {
	Lock()
	Unlock()
	GetRouteTable() (string, error)
	UpdateRouteTable(line string) error
	ListDevices() ([]LinkDevice, error)
	ListRoutesForDevice(device string) ([]Route, error)
	ListTables() (map[int]bool, error)
	ListRulesForTable(table int) ([]Rule, error)
	ListRoutesForTable(table int) ([]Route, error)
	AddRule(table int, sourceIP string) error
	AddRoute(table int, gatewayIP, device string) error
}

// GetDeviceForNumaNode returns a device name for the node, if one exists. If there are
// multiple devices, a gVINC driver is perferred. The message return value is a string
// that can be displayed in an event as a warning to the user.
//
// If no devices are associated with a NUMA node, then this uses node as an index into the
// list of gVNIC & virtio devices. This is to allow use with platforms that have multiple
// devices but no NUMA alignment, for example n2-standard-64.
//
// If there are any NUMA-associated devices, but none matching `node`, then this will
// error.
func GetDeviceForNumaNode(mgr NetworkManager, node int) (device, message string, err error) {
	devices, err := mgr.ListDevices()
	if err != nil {
		return "", "", err
	}
	matchingDevices := []LinkDevice{}
	for _, dev := range devices {
		if dev.NumaNode == node {
			matchingDevices = append(matchingDevices, dev)
		}
	}
	if len(matchingDevices) == 1 {
		return matchingDevices[0].Name, fmt.Sprintf("Used %s for NUMA node %d", matchingDevices[0].Name, node), nil
	}
	if len(matchingDevices) == 0 {
		return findNonNumaDevice(devices, node)
	}

	// If there are multiple devices on the same node, prefer gVINC (aka gve) devices.
	// It is not clear that any platforms exist with such a configuration, but you never know.
	gveDevices := []LinkDevice{}
	for _, dev := range matchingDevices {
		if dev.Driver == "gve" {
			gveDevices = append(gveDevices, dev)
		}
	}
	if len(gveDevices) == 1 {
		return gveDevices[0].Name, fmt.Sprintf("Used gVINC %s for NUMA node %d", gveDevices[0].Name, node), nil
	}
	var kind string
	others := []string{}
	if len(gveDevices) > 0 {
		device = gveDevices[0].Name
		for _, dev := range gveDevices[1:] {
			others = append(others, dev.Name)
		}
	} else {
		device = matchingDevices[0].Name
		for _, dev := range matchingDevices[1:] {
			others = append(others, dev.Name)
		}
	}
	if len(gveDevices) > 0 {
		kind = "gVNIC"
	} else {
		kind = "network"
	}
	return device, fmt.Sprintf("Used %s for NUMA node %d; other %s choices were %s", device, node, kind, strings.Join(others, ", ")), nil
}

// AddSourceRouteForDevice checks for an existing source routing for device, and adds routing table
// information if necessary.
//
// # To check this manually, run `ip route show all` and look for lines like
//
// 10.144.16.1 dev eth0 proto dhcp scope link src 10.144.16.8 metric 1024
//
// This says that the gateway for eth0 is 10.144.16.1, and the source IP for traffic emanating from
// eth0, or the source to bind to for sending from the device, is 10.144.16.8.
//
// The source IP used is returned.
func AddSourceRouteForDevice(mgr NetworkManager, device string) (string, error) {
	mgr.Lock()
	defer mgr.Unlock()

	routes, err := mgr.ListRoutesForDevice(device)
	if err != nil {
		return "", err
	}
	var source, gateway string
	for _, r := range routes {
		source = r.Source
		gateway = r.Gateway
		if source != "" && gateway != "" {
			break
		}
	}
	if source == "" || gateway == "" {
		return "", fmt.Errorf("Could not find source and gateway for %s", device)
	}
	klog.Infof("Using source %s and gateway %s for %s", source, gateway, device)
	table, err := getGcsFuseTableId(mgr, device)
	if err != nil {
		return "", err
	}
	tableRules, err := mgr.ListRulesForTable(table)
	if err != nil {
		return "", err
	}
	foundRule := false
	for _, rule := range tableRules {
		klog.Infof("checking existing rule %+v", rule)
		if rule.Source == source+"/32" && rule.Table == table {
			foundRule = true
			break
		}
	}
	if !foundRule {
		if err := mgr.AddRule(table, source); err != nil {
			return "", err
		}
	}
	tableRoutes, err := mgr.ListRoutesForTable(table)
	if err != nil {
		return "", err
	}
	foundRoute := false
	for _, route := range tableRoutes {
		if route.Device == device && route.Gateway == gateway {
			foundRoute = true
			break
		}
	}
	if !foundRoute {
		if err := mgr.AddRoute(table, gateway, device); err != nil {
			return "", err
		}
	}
	return source, nil
}

func findNonNumaDevice(devices []LinkDevice, node int) (string, string, error) {
	// A non-NUMA device is chosen only if no devices are associated with a node. This is
	// to prevent hard-to-debug performance problems if an invalid node is specfied.
	for _, dev := range devices {
		if dev.NumaNode >= 0 {
			return "", "", fmt.Errorf("Have NUMA devices, but not for node %d (%+v)", node, devices)
		}
	}
	if node >= 0 && node < len(devices) {
		return devices[node].Name, fmt.Sprintf("Used non-NUMA aligned %s for NUMA node %d", devices[node].Name, node), nil
	}
	return "", "", fmt.Errorf("No reasonable device found")
}

// getRtTables returns the kernel routing table list.
//
// As far as I can tell, these tables are informational only and used by the iproute2 tools. It is a
// file in /etc/iproute2/rt_tables that maps integer route table names to human-readable
// descriptions. We will create table names like gcsfusecsi_eth1 when setting up rules, to make
// debugging of a node easier.
func getRtTables(mgr NetworkManager) ([]int, []string, error) {
	tableString, err := mgr.GetRouteTable()
	if err != nil {
		return nil, nil, err
	}
	ids := []int{}
	names := []string{}
	for _, line := range strings.Split(tableString, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		parts := strings.Fields(trimmed)
		if len(parts) == 1 || (len(parts) > 2 && !strings.HasPrefix(parts[2], "#")) {
			klog.Errorf("Bad iproute table line ignored: %s", trimmed)
			continue
		}
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			klog.Errorf("Bad table id ignored: %s", trimmed)
			continue
		}
		ids = append(ids, id)
		names = append(names, parts[1])
	}
	return ids, names, nil
}

func getGcsFuseTableId(mgr NetworkManager, device string) (int, error) {
	tableName := fmt.Sprintf("%s_%s", gcsFuseTableName, device)
	ids, names, err := getRtTables(mgr)
	if err != nil {
		return -1, err
	}
	for idx, name := range names {
		if name == tableName {
			return ids[idx], nil
		}
	}

	currIds, err := mgr.ListTables()
	if err != nil {
		return -1, err
	}
	for _, id := range ids {
		currIds[id] = true
	}
	for id := startingTableId; id <= maxTableId; id++ {
		if _, found := currIds[id]; !found {
			if err := mgr.UpdateRouteTable(fmt.Sprintf("\n%d %s\n", id, tableName)); err != nil {
				return -1, fmt.Errorf("route table update error: %w", err)
			}
			return id, nil
		}
	}
	return -1, fmt.Errorf("Could not create ip route table %s: table ids exhausted", tableName)
}

type realNetworkManager struct {
	links   []netlink.Link
	devices []LinkDevice

	mutex sync.Mutex
}

var _ NetworkManager = &realNetworkManager{}

// NewNetworkManager returns a real network manager that manipulates routing tables.
func NewNetworkManager() NetworkManager {
	return &realNetworkManager{}
}

func (mgr *realNetworkManager) Lock() {
	mgr.mutex.Lock()
}

func (mgr *realNetworkManager) Unlock() {
	mgr.mutex.Unlock()
}

func (mgr *realNetworkManager) knownDevices() string {
	if mgr.links == nil {
		return ""
	}
	devices := []string{}
	for _, l := range mgr.links {
		devices = append(devices, l.Attrs().Name)
	}
	return strings.Join(devices, " ")
}

func (mgr *realNetworkManager) getDeviceLink(device string) (netlink.Link, error) {
	if _, err := mgr.ListDevices(); err != nil {
		return nil, err
	}

	var link netlink.Link
	for _, l := range mgr.links {
		if l.Attrs().Name == device {
			link = l
			break
		}
	}
	if link == nil {
		return nil, fmt.Errorf("%s not found for route list in %s", device, mgr.knownDevices())
	}
	return link, nil
}

func (mgr *realNetworkManager) getLinkByIndex(idx int) (netlink.Link, error) {
	if _, err := mgr.ListDevices(); err != nil {
		return nil, err
	}

	var link netlink.Link
	for _, l := range mgr.links {
		if l.Attrs().Index == idx {
			link = l
			break
		}
	}
	if link == nil {
		return nil, fmt.Errorf("Link index %d not found for route list in %s", idx, mgr.knownDevices())
	}
	return link, nil
}

func (_ *realNetworkManager) GetRouteTable() (string, error) {
	data, err := os.ReadFile(iprouteTableName)
	if err == nil {
		return string(data), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", err
}

func (m *realNetworkManager) UpdateRouteTable(line string) (err error) {
	f, err := os.OpenFile(iprouteTableName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			// If no other error occurred, return the Close error.
			err = cerr
		}
	}()
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	return nil
}

func (mgr *realNetworkManager) ListDevices() ([]LinkDevice, error) {
	if mgr.devices != nil {
		return mgr.devices, nil
	}

	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %w", err)
	}

	eth, err := ethtool.NewEthtool()
	if err != nil {
		return nil, fmt.Errorf("failed to create ethtool: %w", err)
	}
	defer eth.Close()

	mgr.devices = []LinkDevice{}
	mgr.links = []netlink.Link{}
	for _, link := range links {
		name := link.Attrs().Name
		if name == "lo" {
			// Ignore loopback device
			continue
		}
		driver, err := eth.DriverName(name)
		if err != nil {
			klog.Warningf("Failed to get driver for interface %s: %v", name, err)
			continue
		}

		if driver != "gve" && driver != "virtio_net" {
			continue
		}

		numaBytes, err := os.ReadFile(fmt.Sprintf(sysDevTemplate, name))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		numaString := strings.TrimSpace(string(numaBytes))
		node := -1
		if err == nil {
			node, err = strconv.Atoi(numaString)
			if err != nil {
				klog.Warningf("No numa assignment for %s (%s)", name, numaString)
				return nil, fmt.Errorf("Bad numa string %s: %v", numaString, err)
			}
		}
		mgr.devices = append(mgr.devices, LinkDevice{
			Name:     name,
			Driver:   driver,
			NumaNode: node,
		})
		mgr.links = append(mgr.links, link)
	}

	return mgr.devices, nil
}

func (mgr *realNetworkManager) ListRoutesForDevice(device string) ([]Route, error) {
	link, err := mgr.getDeviceLink(device)
	if err != nil {
		return nil, err
	}
	linkRoutes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	routes := []Route{}
	for _, r := range linkRoutes {
		routes = append(routes, Route{
			Device:  device,
			Gateway: r.Gw.String(),
			Source:  r.Src.String(),
			Table:   r.Table,
		})
	}
	return routes, nil
}

func (_ *realNetworkManager) ListTables() (map[int]bool, error) {
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	tables := map[int]bool{}
	for _, r := range rules {
		tables[r.Table] = true
	}
	return tables, nil
}

func (_ *realNetworkManager) ListRulesForTable(table int) ([]Rule, error) {
	linkRules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("ListRulesForTable: %w", err)
	}
	rules := []Rule{}
	for _, r := range linkRules {
		if r.Table != table {
			continue
		}
		// should we filter to rules that only are src -> table?
		rule := Rule{Table: r.Table}
		if r.Src != nil {
			rule.Source = r.Src.String()
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func (mgr *realNetworkManager) ListRoutesForTable(table int) ([]Route, error) {
	linkRoutes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, fmt.Errorf("ListRoutesForTable: %w", err)
	}

	routes := []Route{}
	for _, r := range linkRoutes {
		link, err := mgr.getLinkByIndex(r.LinkIndex)
		if err != nil {
			// It's a link type we don't care about, so silently skip.
			continue
		}
		// should we filter to rules we care about (default to gateway/device,
		// or including the source?)
		routes = append(routes, Route{
			Device:  link.Attrs().Name,
			Gateway: r.Gw.String(),
			Source:  r.Src.String(),
			Table:   r.Table,
		})
	}
	return routes, nil
}

func (_ *realNetworkManager) AddRule(table int, sourceIP string) error {
	_, srcCidr, err := net.ParseCIDR(sourceIP + "/32")
	if err != nil {
		return fmt.Errorf("AddRule (%s): %w", sourceIP, err)
	}
	rule := netlink.NewRule()
	rule.Priority = rulePriority
	rule.Table = table
	rule.Src = srcCidr

	if err = netlink.RuleAdd(rule); err != nil {
		return fmt.Errorf("AddRule (%+v): %w", *rule, err)
	}
	return nil
}

func (mgr *realNetworkManager) AddRoute(table int, gatewayIP, device string) error {
	link, err := mgr.getDeviceLink(device)
	if err != nil {
		return fmt.Errorf("AddRoute (%s): %w", device, err)
	}
	gwIP := net.ParseIP(gatewayIP)
	if gwIP == nil {
		return fmt.Errorf("AddRoute: bad gateway IP %s", gatewayIP)
	}
	route := netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        gwIP,
		Table:     table,
	}
	// RouteReplace is more reliable than RouteAdd?
	if err = netlink.RouteReplace(&route); err != nil {
		return fmt.Errorf("AddRoute (%+v/%+v): %w", route, link, err)
	}
	return nil
}
