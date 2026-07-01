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

package bird

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

var testConfig = Config{
	SocketPath:     "/var/run/bird/bird.ctl",
	ConfigFile:     "/etc/bird/bird.conf",
	TableID:        4096,
	RulePriority:   100,
	KernelScanTime: 10,
}

var testBGPSpec = meridio2v1alpha1.BgpSpec{
	RemoteASN:  65000,
	LocalASN:   65001,
	HoldTime:   "240s",
	LocalPort:  uint16Ptr(179),
	RemotePort: uint16Ptr(179),
}

func TestGenerateConfig(t *testing.T) {
	b, err := New(testConfig)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("empty config", func(t *testing.T) {
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{}, nil)
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, "protocol device") {
			t.Error("missing base config")
		}
	})

	t.Run("with vips", func(t *testing.T) {
		vips := []string{"20.0.0.1/32", "2001:db8::1/128"}
		conf, err := b.generateConfig(vips, []*meridio2v1alpha1.GatewayRouter{}, nil)
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, "protocol static VIP4") {
			t.Error("missing VIP4 config")
		}
		if !strings.Contains(conf, "protocol static VIP6") {
			t.Error("missing VIP6 config")
		}
		if !strings.Contains(conf, "20.0.0.1/32") {
			t.Error("missing IPv4 VIP")
		}
		if !strings.Contains(conf, "2001:db8::1/128") {
			t.Error("missing IPv6 VIP")
		}
	})

	t.Run("with router", func(t *testing.T) {
		router := &meridio2v1alpha1.GatewayRouter{
			ObjectMeta: metav1.ObjectMeta{Name: "test-router"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Address: "192.168.1.1",
				BGP:     testBGPSpec,
			},
		}
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{router}, nil)
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, "protocol bgp 'NBR-test-router'") {
			t.Error("missing BGP protocol with NBR- prefix")
		}
		if !strings.Contains(conf, "neighbor 192.168.1.1") {
			t.Error("missing neighbor address")
		}
	})

	t.Run("matches reference config", func(t *testing.T) {
		logConfig := testConfig
		logConfig.LogParams = BirdLogParams{
			{Type: "stderr", Classes: []string{"info", "warning", "error", "fatal"}},
			{Type: "file", Path: "/var/log/bird.log", Size: 1048576, BackupPath: "/var/log/bird.log.1", Classes: []string{"all"}},
		}
		bWithLogs, err := New(logConfig)
		if err != nil {
			t.Fatal(err)
		}
		router := &meridio2v1alpha1.GatewayRouter{
			ObjectMeta: metav1.ObjectMeta{Name: "gatewayrouter-sample"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "vlan-100",
				Address:   "169.254.100.150",
				BGP: meridio2v1alpha1.BgpSpec{
					RemoteASN:  4200000000,
					LocalASN:   64512,
					LocalPort:  uint16Ptr(10179),
					RemotePort: uint16Ptr(10179),
					HoldTime:   "3s",
					BFD: &meridio2v1alpha1.BfdSpec{
						Switch:     boolPtr(true),
						MinRx:      "300ms",
						MinTx:      "300ms",
						Multiplier: uint16Ptr(3),
					},
				},
			},
		}
		vips := []string{"20.0.0.1/32"}

		got, err := bWithLogs.generateConfig(vips, []*meridio2v1alpha1.GatewayRouter{router}, nil)
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}

		want := `
log stderr { info, warning, error, fatal };
log "/var/log/bird.log" 1048576 "/var/log/bird.log.1" all;

protocol device {}

filter gateway_routes {
	if ( net ~ [ 0.0.0.0/0 ] ) then accept;
	if ( net ~ [ 0::/0 ] ) then accept;
	if source = RTS_BGP then accept;
	else reject;
}

filter announced_routes {
	if ( net ~ [ 0.0.0.0/0 ] ) then reject;
	if ( net ~ [ 0::/0 ] ) then reject;
	if source = RTS_STATIC && dest != RTD_BLACKHOLE then accept;
	else reject;
}

template bgp BGP_TEMPLATE {
	debug {events, states};
	direct;
	bfd off;
	graceful restart off;
	ipv4 {
		import none;
		export none;
		next hop self;
	};
	ipv6 {
		import none;
		export none;
		next hop self;
	};
}

protocol kernel {
	ipv4 {
		import none;
		export filter gateway_routes;
	};
	scan time 10;
	kernel table 4096;
	merge paths on;
}

protocol kernel {
	ipv6 {
		import none;
		export filter gateway_routes;
	};
	scan time 10;
	kernel table 4096;
	merge paths on;
}

protocol bfd {
	accept direct;
	interface "vlan-100" {};
}

protocol static VIP4 {
	ipv4 { preference 110; };
	route 20.0.0.1/32 via "lo";
}

protocol bgp 'NBR-gatewayrouter-sample' from BGP_TEMPLATE {
	interface "vlan-100";
	local port 10179 as 64512;
	neighbor 169.254.100.150 port 10179 as 4200000000;
	bfd {
		min rx interval 300ms;
		min tx interval 300ms;
		multiplier 3;
	};
	hold time 3;
	ipv4 {
		import filter gateway_routes;
		export filter announced_routes;
	};
}`

		if normalizeWhitespace(got) != normalizeWhitespace(want) {
			t.Errorf("config mismatch\nGot:\n%s\n\nWant:\n%s", got, want)
		}
	})

	t.Run("duplicate interface dedup", func(t *testing.T) {
		routers := []*meridio2v1alpha1.GatewayRouter{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "gw-v4"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "net1", Address: "192.168.1.1",
					BGP: testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "gw-v6"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "net1", Address: "fd00::1",
					BGP: testBGPSpec,
				},
			},
		}

		conf, err := b.generateConfig([]string{}, routers, nil)
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}

		count := strings.Count(conf, `interface "net1" {}`)
		if count != 1 {
			t.Errorf("expected 1 BFD interface entry, got %d\n%s", count, conf)
		}
	})

	t.Run("sorted by name with 4 routers", func(t *testing.T) {
		routers := []*meridio2v1alpha1.GatewayRouter{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "D"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "if_D", Address: "192.168.4.1",
					BGP: testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "B"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "if_B", Address: "192.168.2.1",
					BGP: testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "C"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "if_C", Address: "192.168.3.1",
					BGP: testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "A"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "if_A", Address: "192.168.1.1",
					BGP: testBGPSpec,
				},
			},
		}

		conf, err := b.generateConfig([]string{}, routers, nil)
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}

		// Verify BFD interface ordering is sorted alphabetically
		wantIfOrder := []string{`interface "if_A"`, `interface "if_B"`, `interface "if_C"`, `interface "if_D"`}
		prev := 0
		for _, s := range wantIfOrder {
			idx := strings.Index(conf[prev:], s)
			if idx < 0 {
				t.Fatalf("BFD interface %q not found after position %d", s, prev)
			}
			prev += idx + len(s)
		}

		// Verify BGP protocol ordering is sorted by router name
		wantBGPOrder := []string{"NBR-A", "NBR-B", "NBR-C", "NBR-D"}
		prev = 0
		for _, s := range wantBGPOrder {
			idx := strings.Index(conf[prev:], s)
			if idx < 0 {
				t.Fatalf("BGP protocol %q not found after position %d", s, prev)
			}
			prev += idx + len(s)
		}
	})
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func uint16Ptr(i uint16) *uint16 {
	return &i
}

func boolPtr(b bool) *bool {
	return &b
}

func TestConvertAlgorithm(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hmac-md5", "hmac md5"},
		{"hmac-sha-1", "hmac sha1"},
		{"hmac-sha-224", "hmac sha224"},
		{"hmac-sha-256", "hmac sha256"},
		{"hmac-sha-384", "hmac sha384"},
		{"hmac-sha-512", "hmac sha512"},
		{"cmac-aes-128", "cmac aes128"},
		{"unknown-algo", "unknown-algo"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := convertAlgorithm(tt.input); got != tt.want {
				t.Errorf("convertAlgorithm(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTcpAoConfig(t *testing.T) {
	t.Run("nil spec returns empty", func(t *testing.T) {
		if got := tcpAoConfig(nil, nil); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("empty keychain returns empty", func(t *testing.T) {
		spec := &meridio2v1alpha1.BgpTcpAoSpec{Keychain: []meridio2v1alpha1.TcpAoKeyChain{}}
		if got := tcpAoConfig(spec, nil); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("missing password skips key", func(t *testing.T) {
		spec := &meridio2v1alpha1.BgpTcpAoSpec{
			Keychain: []meridio2v1alpha1.TcpAoKeyChain{
				{SendId: 1, Algorithm: "hmac-sha-256", SecretName: "s", SecretKey: "k"},
			},
		}
		if got := tcpAoConfig(spec, map[uint8]string{}); got != "" {
			t.Errorf("expected empty when password missing, got %q", got)
		}
	})

	t.Run("single key", func(t *testing.T) {
		spec := &meridio2v1alpha1.BgpTcpAoSpec{
			Keychain: []meridio2v1alpha1.TcpAoKeyChain{
				{SendId: 1, RecvId: 10, Algorithm: "hmac-sha-256", SecretName: "s", SecretKey: "k"},
			},
		}
		passwords := map[uint8]string{1: "secret123"}
		got := tcpAoConfig(spec, passwords)

		if !strings.Contains(got, "authentication ao;") {
			t.Error("missing 'authentication ao;'")
		}
		if !strings.Contains(got, "send id 1;") {
			t.Error("missing send id")
		}
		if !strings.Contains(got, "recv id 10;") {
			t.Error("missing recv id")
		}
		if !strings.Contains(got, `secret "secret123";`) {
			t.Error("missing secret")
		}
		if !strings.Contains(got, "algorithm hmac sha256;") {
			t.Error("missing converted algorithm")
		}
	})

	t.Run("multiple keys", func(t *testing.T) {
		spec := &meridio2v1alpha1.BgpTcpAoSpec{
			Keychain: []meridio2v1alpha1.TcpAoKeyChain{
				{SendId: 1, RecvId: 11, Algorithm: "hmac-sha-1", SecretName: "s1", SecretKey: "k1"},
				{SendId: 2, RecvId: 12, Algorithm: "cmac-aes-128", SecretName: "s2", SecretKey: "k2"},
			},
		}
		passwords := map[uint8]string{1: "pass1", 2: "pass2"}
		got := tcpAoConfig(spec, passwords)

		if !strings.Contains(got, "send id 1;") || !strings.Contains(got, "send id 2;") {
			t.Error("missing one of the send ids")
		}
		if !strings.Contains(got, "recv id 11;") || !strings.Contains(got, "recv id 12;") {
			t.Error("missing one of the recv ids")
		}
		if !strings.Contains(got, "algorithm hmac sha1;") {
			t.Error("missing hmac sha1 algorithm")
		}
		if !strings.Contains(got, "algorithm cmac aes128;") {
			t.Error("missing cmac aes128 algorithm")
		}
	})

	t.Run("CurrentKeyId selects preferred", func(t *testing.T) {
		currentKey := uint8(2)
		spec := &meridio2v1alpha1.BgpTcpAoSpec{
			CurrentKeyId: &currentKey,
			Keychain: []meridio2v1alpha1.TcpAoKeyChain{
				{SendId: 1, RecvId: 11, Algorithm: "hmac-sha-1", SecretName: "s1", SecretKey: "k1"},
				{SendId: 2, RecvId: 12, Algorithm: "hmac-sha-256", SecretName: "s2", SecretKey: "k2"},
			},
		}
		passwords := map[uint8]string{1: "pass1", 2: "pass2"}
		got := tcpAoConfig(spec, passwords)

		// Key 2 should have preferred, key 1 should not
		parts := strings.Split(got, "key {")
		if len(parts) != 3 {
			t.Fatalf("expected 2 key blocks, got %d", len(parts)-1)
		}
		if strings.Contains(parts[1], "preferred;") {
			t.Error("key 1 should NOT have preferred")
		}
		if !strings.Contains(parts[2], "preferred;") {
			t.Error("key 2 should have preferred")
		}
	})

	t.Run("NextKeyId emits rnext id", func(t *testing.T) {
		nextKey := uint8(3)
		spec := &meridio2v1alpha1.BgpTcpAoSpec{
			NextKeyId: &nextKey,
			Keychain: []meridio2v1alpha1.TcpAoKeyChain{
				{SendId: 1, RecvId: 10, Algorithm: "hmac-sha-256", SecretName: "s", SecretKey: "k"},
			},
		}
		passwords := map[uint8]string{1: "pass1"}
		got := tcpAoConfig(spec, passwords)

		if !strings.Contains(got, "rnext id 3;") {
			t.Error("missing rnext id")
		}
	})

	t.Run("no NextKeyId omits rnext id", func(t *testing.T) {
		spec := &meridio2v1alpha1.BgpTcpAoSpec{
			Keychain: []meridio2v1alpha1.TcpAoKeyChain{
				{SendId: 1, RecvId: 10, Algorithm: "hmac-sha-256", SecretName: "s", SecretKey: "k"},
			},
		}
		passwords := map[uint8]string{1: "pass1"}
		got := tcpAoConfig(spec, passwords)

		if strings.Contains(got, "rnext id") {
			t.Error("rnext id should not be present when NextKeyId is nil")
		}
	})
}

func TestGenerateConfigWithTcpAo(t *testing.T) {
	b, err := New(testConfig)
	if err != nil {
		t.Fatal(err)
	}
	router := &meridio2v1alpha1.GatewayRouter{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-ao"},
		Spec: meridio2v1alpha1.GatewayRouterSpec{
			Interface: "eth0",
			Address:   "10.0.0.1",
			BGP: meridio2v1alpha1.BgpSpec{
				RemoteASN:  65000,
				LocalASN:   65001,
				HoldTime:   "240s",
				LocalPort:  uint16Ptr(179),
				RemotePort: uint16Ptr(179),
				Authentication: &meridio2v1alpha1.BgpTcpAoSpec{
					Keychain: []meridio2v1alpha1.TcpAoKeyChain{
						{SendId: 5, RecvId: 7, Algorithm: "hmac-sha-256", SecretName: "bgp-secret", SecretKey: "key"},
					},
				},
			},
		},
	}
	passwords := map[string]map[uint8]string{"gw-ao": {5: "mypassword"}}

	conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{router}, passwords)
	if err != nil {
		t.Fatalf("generateConfig() error = %v", err)
	}

	if !strings.Contains(conf, "authentication ao;") {
		t.Error("missing 'authentication ao;' in generated config")
	}
	if !strings.Contains(conf, `secret "mypassword";`) {
		t.Error("missing secret in generated config")
	}
	if !strings.Contains(conf, "send id 5;") {
		t.Error("missing send id in generated config")
	}
	if !strings.Contains(conf, "recv id 7;") {
		t.Error("missing recv id in generated config")
	}
	if !strings.Contains(conf, "algorithm hmac sha256;") {
		t.Error("missing algorithm in generated config")
	}
}
