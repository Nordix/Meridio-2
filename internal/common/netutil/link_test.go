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

package netutil

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

type fakeAddrLister struct {
	addrs []netlink.Addr
	err   error
}

func (f *fakeAddrLister) AddrList(_ netlink.Link, _ int) ([]netlink.Addr, error) {
	return f.addrs, f.err
}

func addr(t *testing.T, cidr string, linkIndex int) netlink.Addr {
	t.Helper()
	ip, ipNet, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	ipNet.IP = ip
	return netlink.Addr{IPNet: ipNet, LinkIndex: linkIndex}
}

func TestLinkIndexByIP(t *testing.T) {
	nl := &fakeAddrLister{addrs: []netlink.Addr{
		{LinkIndex: 9}, // no IPNet: must be skipped, not dereferenced
		addr(t, "127.0.0.1/8", 1),
		addr(t, "10.244.1.5/24", 2),
		addr(t, "fd00:10:244:1::5/64", 2),
		addr(t, "169.254.100.10/24", 3),
	}}

	tests := []struct {
		name    string
		ip      string
		want    int
		wantErr bool
	}{
		{"ipv4", "10.244.1.5", 2, false},
		{"ipv6", "fd00:10:244:1::5", 2, false},
		{"other link", "169.254.100.10", 3, false},
		{"same subnet, different host", "10.244.1.6", 0, true},
		{"unknown", "192.0.2.1", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LinkIndexByIP(nl, net.ParseIP(tt.ip))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLinkIndexByIP_NoAddresses(t *testing.T) {
	_, err := LinkIndexByIP(&fakeAddrLister{}, net.ParseIP("10.244.1.5"))
	assert.Error(t, err)
}

func TestLinkIndexByIP_AddrListError(t *testing.T) {
	listErr := errors.New("netlink failure")
	_, err := LinkIndexByIP(&fakeAddrLister{err: listErr}, net.ParseIP("10.244.1.5"))
	assert.ErrorIs(t, err, listErr)
}

func TestLinkIndexByIP_NilIP(t *testing.T) {
	_, err := LinkIndexByIP(&fakeAddrLister{}, nil)
	assert.Error(t, err)
}
