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

func TestGenerateConfig(t *testing.T) {
	b := New()

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
				BGP: meridio2v1alpha1.BgpSpec{
					RemoteASN: 65000,
					LocalASN:  65001,
				},
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

		got, err := b.generateConfig(vips, []*meridio2v1alpha1.GatewayRouter{router})
		if err != nil {
			t.Fatalf("generateConfig() error = %v", err)
		}

		want := `log stderr all;

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
	hold time 90;
	bfd on;
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
	kernel table 4096;
	merge paths on;
}

protocol kernel {
	ipv6 {
		import none;
		export filter gateway_routes;
	};
	kernel table 4096;
	merge paths on;
}

protocol bfd {
	interface "*" {};
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
