/*
Copyright (c) 2026 OpenInfra Foundation Europe. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sidecar

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// mockLink implements netlink.Link for testing.
type mockLink struct {
	name  string
	index int
}

func (m *mockLink) Attrs() *netlink.LinkAttrs {
	return &netlink.LinkAttrs{Name: m.name, Index: m.index}
}

func (m *mockLink) Type() string { return "mock" }

// mockNetlink implements netlinkOps for testing.
// Tracks interfaces with addresses, and records rule/route state.
type mockNetlink struct {
	links  map[string]*mockLink       // name → link
	addrs  map[int][]netlink.Addr     // link index → addresses
	rules  []netlink.Rule             // all rules
	routes map[routeKey]netlink.Route // table+family → route

	addrAddErr      error // inject error for AddrAdd
	addrDelErr      error // inject error for AddrDel
	ruleAddErr      error
	routeReplaceErr error
}

type routeKey struct {
	table  int
	family int
}

func newMockNetlink() *mockNetlink {
	return &mockNetlink{
		links:  make(map[string]*mockLink),
		addrs:  make(map[int][]netlink.Addr),
		routes: make(map[routeKey]netlink.Route),
	}
}

// addLink adds a mock interface with the given addresses.
func (m *mockNetlink) addLink(name string, index int, ips ...string) {
	link := &mockLink{name: name, index: index}
	m.links[name] = link
	for _, ipStr := range ips {
		ip, ipNet, err := net.ParseCIDR(ipStr)
		if err != nil {
			ip = net.ParseIP(ipStr)
			if ip == nil {
				continue
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			ipNet = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
		}
		m.addrs[index] = append(m.addrs[index], netlink.Addr{
			IPNet: &net.IPNet{IP: ip, Mask: ipNet.Mask},
		})
	}
}

func (m *mockNetlink) LinkByName(name string) (netlink.Link, error) {
	link, ok := m.links[name]
	if !ok {
		return nil, fmt.Errorf("link not found: %s", name)
	}
	return link, nil
}

func (m *mockNetlink) LinkList() ([]netlink.Link, error) {
	links := make([]netlink.Link, 0, len(m.links))
	for _, l := range m.links {
		links = append(links, l)
	}
	return links, nil
}

func (m *mockNetlink) AddrList(link netlink.Link, _ int) ([]netlink.Addr, error) {
	return m.addrs[link.Attrs().Index], nil
}

func (m *mockNetlink) AddrAdd(link netlink.Link, addr *netlink.Addr) error {
	if m.addrAddErr != nil {
		return m.addrAddErr
	}
	m.addrs[link.Attrs().Index] = append(m.addrs[link.Attrs().Index], *addr)
	return nil
}

func (m *mockNetlink) AddrDel(link netlink.Link, addr *netlink.Addr) error {
	if m.addrDelErr != nil {
		return m.addrDelErr
	}
	idx := link.Attrs().Index
	filtered := m.addrs[idx][:0]
	for _, a := range m.addrs[idx] {
		if a.IPNet.String() != addr.IPNet.String() {
			filtered = append(filtered, a)
		}
	}
	m.addrs[idx] = filtered
	return nil
}

func (m *mockNetlink) RuleList(_ int) ([]netlink.Rule, error) {
	return m.rules, nil
}

func (m *mockNetlink) RuleAdd(rule *netlink.Rule) error {
	if m.ruleAddErr != nil {
		return m.ruleAddErr
	}
	m.rules = append(m.rules, *rule)
	return nil
}

func (m *mockNetlink) RuleDel(rule *netlink.Rule) error {
	filtered := m.rules[:0]
	for _, r := range m.rules {
		if r.Table == rule.Table && r.Src != nil && rule.Src != nil && r.Src.String() == rule.Src.String() {
			continue // remove match
		}
		filtered = append(filtered, r)
	}
	m.rules = filtered
	return nil
}

func (m *mockNetlink) RouteListFiltered(family int, filter *netlink.Route, _ uint64) ([]netlink.Route, error) {
	key := routeKey{table: filter.Table, family: family}
	if r, ok := m.routes[key]; ok {
		return []netlink.Route{r}, nil
	}
	return nil, nil
}

func (m *mockNetlink) RouteReplace(route *netlink.Route) error {
	if m.routeReplaceErr != nil {
		return m.routeReplaceErr
	}
	m.routes[routeKey{table: route.Table, family: route.Family}] = *route
	return nil
}

func (m *mockNetlink) RouteDel(route *netlink.Route) error {
	delete(m.routes, routeKey{table: route.Table, family: route.Family})
	return nil
}

// helper: count rules for a given table
func (m *mockNetlink) rulesForTable(tableID int) []netlink.Rule {
	var result []netlink.Rule
	for _, r := range m.rules {
		if r.Table == tableID {
			result = append(result, r)
		}
	}
	return result
}
