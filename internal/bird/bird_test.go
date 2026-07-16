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
	"context"
	"os"
	"strings"
	"testing"
	"time"

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

	t.Run("empty config", testEmptyConfig(b))
	t.Run("with vips", testWithVips(b))
	t.Run("with router", testWithRouter(b))
	t.Run("matches reference config", testMatchesReferenceConfig())
	t.Run("duplicate interface dedup", testDuplicateInterfaceDedup(b))
	t.Run("static router with bfd", testStaticRouterWithBFD(b))
	t.Run("static router bfd params on interface", testStaticRouterBFDParamsOnInterface(b))
	t.Run("static bfd params first alphabetically wins", testStaticBFDParamsFirstAlphabeticallyWins(b))
	t.Run("static router without bfd", testStaticRouterWithoutBFD(b))
	t.Run("static router ipv6 without bfd", testStaticRouterIPv6WithoutBFD(b))
	t.Run("mixed bgp and static routers", testMixedBGPAndStaticRouters(b))
	t.Run("sorted by name with 4 routers", testSortedByName(b))
}

func testEmptyConfig(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{})
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, "protocol device") {
			t.Error("missing base config")
		}
	}
}

func testWithVips(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
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
	}
}

func testWithRouter(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		router := &meridio2v1alpha1.GatewayRouter{
			ObjectMeta: metav1.ObjectMeta{Name: "test-router"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Address: "192.168.1.1",
				BGP:     &testBGPSpec,
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
	}
}

func testMatchesReferenceConfig() func(t *testing.T) {
	return func(t *testing.T) {
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
				BGP: &meridio2v1alpha1.BgpSpec{
					RemoteASN:  4200000000,
					LocalASN:   64512,
					LocalPort:  uint16Ptr(10179),
					RemotePort: uint16Ptr(10179),
					HoldTime:   "3s",
					BFD: &meridio2v1alpha1.BfdSpec{
						MinRx:      "300ms",
						MinTx:      "300ms",
						Multiplier: 3,
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

filter default_rt {
	if ( net ~ [ 0.0.0.0/0 ] ) then accept;
	if ( net ~ [ 0::/0 ] ) then accept;
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
	bfd { min rx interval 300ms; min tx interval 300ms; multiplier 3; };
	hold time 3;
	ipv4 {
		import filter gateway_routes;
		export filter announced_routes;
	};
}`

		if normalizeWhitespace(got) != normalizeWhitespace(want) {
			t.Errorf("config mismatch\nGot:\n%s\n\nWant:\n%s", got, want)
		}
	}
}

func testDuplicateInterfaceDedup(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		routers := []*meridio2v1alpha1.GatewayRouter{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "gw-v4"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "net1", Address: "192.168.1.1",
					BGP: &testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "gw-v6"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "net1", Address: "fd00::1",
					BGP: &testBGPSpec,
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
	}
}

func testStaticRouterWithBFD(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		router := &meridio2v1alpha1.GatewayRouter{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-static"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "ext-vlan",
				Address:   "169.254.100.254",
				Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
				Static: &meridio2v1alpha1.StaticSpec{
					BFD: &meridio2v1alpha1.BfdSpec{
						MinTx:      "300ms",
						MinRx:      "300ms",
						Multiplier: 3,
					},
				},
			},
		}
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{router})
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, "protocol static 'NBR-gw-static'") {
			t.Error("missing static protocol")
		}
		if !strings.Contains(conf, "route 0.0.0.0/0 via 169.254.100.254%'ext-vlan' bfd;") {
			t.Errorf("missing static route with bfd, got:\n%s", conf)
		}
	}
}

func testStaticRouterBFDParamsOnInterface(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		router := &meridio2v1alpha1.GatewayRouter{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-static"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "ext-vlan",
				Address:   "169.254.100.254",
				Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
				Static: &meridio2v1alpha1.StaticSpec{
					BFD: &meridio2v1alpha1.BfdSpec{
						MinTx:      "300ms",
						MinRx:      "300ms",
						Multiplier: 3,
					},
				},
			},
		}
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{router})
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, `"ext-vlan" {min rx interval 300ms; min tx interval 300ms; multiplier 3;}`) {
			t.Errorf("missing BFD params on interface block, got:\n%s", conf)
		}
	}
}

func testStaticBFDParamsFirstAlphabeticallyWins(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		routers := []*meridio2v1alpha1.GatewayRouter{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "m-gateway"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "ext-vlan", Address: "169.254.100.4",
					Protocol: meridio2v1alpha1.RoutingProtocolStatic,
					Static: &meridio2v1alpha1.StaticSpec{
						BFD: &meridio2v1alpha1.BfdSpec{Multiplier: 40},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "a-gateway"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "ext-vlan", Address: "169.254.100.1",
					Protocol: meridio2v1alpha1.RoutingProtocolStatic,
					Static: &meridio2v1alpha1.StaticSpec{
						BFD: &meridio2v1alpha1.BfdSpec{Multiplier: 10},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "z-gateway"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "ext-vlan", Address: "169.254.100.5",
					Protocol: meridio2v1alpha1.RoutingProtocolStatic,
					Static: &meridio2v1alpha1.StaticSpec{
						BFD: &meridio2v1alpha1.BfdSpec{Multiplier: 50},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "c-gateway"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "ext-vlan", Address: "169.254.100.3",
					Protocol: meridio2v1alpha1.RoutingProtocolStatic,
					Static: &meridio2v1alpha1.StaticSpec{
						BFD: &meridio2v1alpha1.BfdSpec{Multiplier: 30},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "b-gateway"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "ext-vlan", Address: "169.254.100.2",
					Protocol: meridio2v1alpha1.RoutingProtocolStatic,
					Static: &meridio2v1alpha1.StaticSpec{
						BFD: &meridio2v1alpha1.BfdSpec{Multiplier: 20},
					},
				},
			},
		}
		conf, err := b.generateConfig([]string{}, routers)
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		// "a-gateway" is first alphabetically, so its multiplier (10) wins
		if !strings.Contains(conf, "multiplier 10;") {
			t.Errorf("expected first alphabetical router's BFD params, got:\n%s", conf)
		}
	}
}

func testStaticRouterWithoutBFD(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		router := &meridio2v1alpha1.GatewayRouter{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-static-no-bfd"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "ext-vlan",
				Address:   "169.254.100.254",
				Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
				Static:    &meridio2v1alpha1.StaticSpec{},
			},
		}
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{router})
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if strings.Contains(conf, "route 0.0.0.0/0 via 169.254.100.254%'ext-vlan' bfd") {
			t.Errorf("bfd should not be on route line when BFD is not configured, got:\n%s", conf)
		}
		if !strings.Contains(conf, "route 0.0.0.0/0 via 169.254.100.254%'ext-vlan';") {
			t.Errorf("missing static route without bfd, got:\n%s", conf)
		}
	}
}

func testStaticRouterIPv6WithoutBFD(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		router := &meridio2v1alpha1.GatewayRouter{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-static-v6"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "ext-vlan",
				Address:   "100:100::254",
				Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
				Static:    &meridio2v1alpha1.StaticSpec{},
			},
		}
		conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{router})
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, "protocol static 'NBR-gw-static-v6'") {
			t.Error("missing static protocol")
		}
		if !strings.Contains(conf, "route 0::/0 via 100:100::254%'ext-vlan';") {
			t.Errorf("missing static route without bfd, got:\n%s", conf)
		}
	}
}

func testMixedBGPAndStaticRouters(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		routers := []*meridio2v1alpha1.GatewayRouter{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bgp-gw"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "net1", Address: "192.168.1.1",
					Protocol: meridio2v1alpha1.RoutingProtocolBGP,
					BGP:      &testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "static-gw"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "net1", Address: "192.168.1.2",
					Protocol: meridio2v1alpha1.RoutingProtocolStatic,
					Static: &meridio2v1alpha1.StaticSpec{
						BFD: &meridio2v1alpha1.BfdSpec{},
					},
				},
			},
		}
		conf, err := b.generateConfig([]string{}, routers)
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}
		if !strings.Contains(conf, "protocol bgp 'NBR-bgp-gw'") {
			t.Error("missing BGP protocol")
		}
		if !strings.Contains(conf, "protocol static 'NBR-static-gw'") {
			t.Error("missing static protocol")
		}
	}
}

func testSortedByName(b *Bird) func(t *testing.T) {
	return func(t *testing.T) {
		routers := []*meridio2v1alpha1.GatewayRouter{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "D"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "if_D", Address: "192.168.4.1",
					BGP: &testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "B"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "if_B", Address: "192.168.2.1",
					BGP: &testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "C"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "if_C", Address: "192.168.3.1",
					BGP: &testBGPSpec,
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "A"},
				Spec: meridio2v1alpha1.GatewayRouterSpec{
					Interface: "if_A", Address: "192.168.1.1",
					BGP: &testBGPSpec,
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
	}
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func uint16Ptr(i uint16) *uint16 {
	return &i
}

func TestGenerateConfig_Deterministic(t *testing.T) {
	b, err := New(testConfig)
	if err != nil {
		t.Fatal(err)
	}

	routers := []*meridio2v1alpha1.GatewayRouter{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "router-a"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "ext1", Address: "169.254.100.1",
				BGP: &testBGPSpec,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "router-b"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "ext2", Address: "169.254.100.2",
				BGP: &testBGPSpec,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "router-c"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "ext3", Address: "169.254.100.3",
				BGP: &testBGPSpec,
			},
		},
	}

	// Different input orderings must produce identical config output
	vipPermutations := [][]string{
		{"10.0.0.1/32", "10.0.0.2/32", "10.0.0.3/32", "2001:db8::1/128", "2001:db8::2/128", "2001:db8::3/128"},
		{"2001:db8::3/128", "10.0.0.3/32", "2001:db8::1/128", "10.0.0.1/32", "2001:db8::2/128", "10.0.0.2/32"},
		{"10.0.0.3/32", "10.0.0.2/32", "10.0.0.1/32", "2001:db8::3/128", "2001:db8::2/128", "2001:db8::1/128"},
	}

	routerPermutations := [][]*meridio2v1alpha1.GatewayRouter{
		{routers[0], routers[1], routers[2]},
		{routers[2], routers[0], routers[1]},
		{routers[1], routers[2], routers[0]},
	}

	var reference string
	for i, vips := range vipPermutations {
		for j, rts := range routerPermutations {
			conf, err := b.generateConfig(vips, rts)
			if err != nil {
				t.Fatalf("vips[%d] routers[%d]: generateConfig() error = %v", i, j, err)
			}
			if reference == "" {
				reference = conf
			} else if conf != reference {
				t.Fatalf("vips[%d] routers[%d]: config differs from reference (input ordering affected output)", i, j)
			}
		}
	}
}

func TestConfigure_SkipsRewriteWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		SocketPath:     dir + "/bird.ctl",
		ConfigFile:     dir + "/bird.conf",
		TableID:        4096,
		RulePriority:   100,
		KernelScanTime: 10,
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.nl = &netlinkMock{} // avoid real netlink calls

	vips := []string{"20.0.0.1", "2001:db8::1"}
	routers := []*meridio2v1alpha1.GatewayRouter{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "router-a"},
			Spec: meridio2v1alpha1.GatewayRouterSpec{
				Interface: "ext1", Address: "169.254.100.1",
				BGP: &testBGPSpec,
			},
		},
	}

	// First Configure writes the config file
	if err := b.Configure(context.Background(), vips, routers); err != nil {
		t.Fatal(err)
	}
	info1, err := os.Stat(cfg.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}

	// Ensure mtime would differ if file is rewritten
	time.Sleep(10 * time.Millisecond)

	// Second Configure with same inputs should skip the write
	if err := b.Configure(context.Background(), vips, routers); err != nil {
		t.Fatal(err)
	}
	info2, err := os.Stat(cfg.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}

	if info2.ModTime() != info1.ModTime() {
		t.Error("config file was rewritten despite no change (skip-if-unchanged guard failed)")
	}
}
