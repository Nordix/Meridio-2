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

// Package netutil holds generic helpers for inspecting the local network namespace.
package netutil

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// AddrLister abstracts the netlink address listing used by LinkIndexByIP.
// *netlink.Handle satisfies this interface directly.
type AddrLister interface {
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
}

// LinkIndexByIP returns the index of the link holding ip.
func LinkIndexByIP(nl AddrLister, ip net.IP) (int, error) {
	if ip == nil {
		return 0, fmt.Errorf("IP is nil")
	}

	addrs, err := nl.AddrList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return 0, fmt.Errorf("failed to list addresses: %w", err)
	}

	for _, a := range addrs {
		if a.IPNet != nil && a.IP.Equal(ip) {
			return a.LinkIndex, nil
		}
	}
	return 0, fmt.Errorf("no link holds IP %s", ip)
}
