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

import "github.com/vishvananda/netlink"

// netlinkOps abstracts netlink syscalls for testing.
// *netlink.Handle satisfies this interface directly.
type netlinkOps interface {
	LinkByName(name string) (netlink.Link, error)
	LinkList() ([]netlink.Link, error)
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
	AddrAdd(link netlink.Link, addr *netlink.Addr) error
	AddrDel(link netlink.Link, addr *netlink.Addr) error
	RuleList(family int) ([]netlink.Rule, error)
	RuleAdd(rule *netlink.Rule) error
	RuleDel(rule *netlink.Rule) error
	RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error)
	RouteReplace(route *netlink.Route) error
	RouteDel(route *netlink.Route) error
}
