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
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{})
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, "protocol device") {
			t.Error("missing base config")
		}
	})

	t.Run("with vips", func(t *testing.T) {
		vips := []string{"20.0.0.1/32", "2001:db8::1/128"}
		conf, err := b.generateConfig(vips, []*meridio2v1alpha1.GatewayRouter{})
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
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{router})
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

		got, err := bWithLogs.generateConfig(vips, []*meridio2v1alpha1.GatewayRouter{router})
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
	setkey off;
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

		conf, err := b.generateConfig([]string{}, routers)
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

		conf, err := b.generateConfig([]string{}, routers)
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
