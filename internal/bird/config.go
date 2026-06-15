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
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/template"
	"time"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

const (
	defaultRouteIPv4 = "0.0.0.0/0"
	defaultRouteIPv6 = "0::/0"

	ipFamilyIPv4 = "ipv4"
	ipFamilyIPv6 = "ipv6"
)

type birdConfigData struct {
	KernelTableID  int
	KernelScanTime int
	LogParams      BirdLogParams
	IPv4VIPs       []string
	IPv6VIPs       []string
	BGPRouters     []bgpRouterData
	StaticRouters  []staticRouterData
	BFDInterfaces  []bfdInterfaceData
}

type bfdInterfaceData struct {
	Name   string
	Params string
}

type bgpRouterData struct {
	Name       string
	Interface  string
	Address    string
	LocalPort  int
	RemotePort int
	LocalASN   uint32
	RemoteASN  uint32
	BFD        string
	HoldTime   string
	IPFamily   string
}

type staticRouterData struct {
	Name      string
	Interface string
	Address   string
	BFD       string
	IPFamily  string
}

var tmplFuncs = template.FuncMap{
	"defaultRoute": func(ipFamily string) string {
		if ipFamily == ipFamilyIPv6 {
			return defaultRouteIPv6
		}
		return defaultRouteIPv4
	},
}

//nolint:lll
var birdConfigTmpl = template.Must(template.New("bird.conf").Funcs(tmplFuncs).Parse(`{{- range .LogParams}}
{{.FmtParams}}
{{- end}}

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
	scan time {{.KernelScanTime}};
	kernel table {{.KernelTableID}};
	merge paths on;
}

protocol kernel {
	ipv6 {
		import none;
		export filter gateway_routes;
	};
	scan time {{.KernelScanTime}};
	kernel table {{.KernelTableID}};
	merge paths on;
}

protocol bfd {
	accept direct;
{{- if .BFDInterfaces}}
{{- range .BFDInterfaces}}
	interface "{{.Name}}" {{"{"}}{{.Params}}{{"}"}};
{{- end}}
{{- else}}
	interface "*" {};
{{- end}}
}
{{- if .IPv4VIPs}}

protocol static VIP4 {
	ipv4 { preference 110; };
{{- range .IPv4VIPs}}
	route {{.}} via "lo";
{{- end}}
}
{{- end}}
{{- if .IPv6VIPs}}

protocol static VIP6 {
	ipv6 { preference 110; };
{{- range .IPv6VIPs}}
	route {{.}} via "lo";
{{- end}}
}
{{- end}}
{{- range .BGPRouters}}

protocol bgp 'NBR-{{.Name}}' from BGP_TEMPLATE {
	interface "{{.Interface}}";
	local port {{.LocalPort}} as {{.LocalASN}};
	neighbor {{.Address}} port {{.RemotePort}} as {{.RemoteASN}};
	{{.BFD}}
	hold time {{.HoldTime}};
	{{.IPFamily}} {
		import filter gateway_routes;
		export filter announced_routes;
	};
}
{{- end}}
{{- range .StaticRouters}}

protocol static 'NBR-{{.Name}}' {
	{{.IPFamily}} {
		import filter default_rt;
	};
	route {{defaultRoute .IPFamily}} via {{.Address}}%'{{.Interface}}'{{.BFD}};
}
{{- end}}
`))

func toBGPRouterData(router *meridio2v1alpha1.GatewayRouter) (bgpRouterData, error) {
	if router.Spec.BGP.LocalPort == nil {
		return bgpRouterData{}, fmt.Errorf("router %q: LocalPort is required", router.Name)
	}
	if router.Spec.BGP.RemotePort == nil {
		return bgpRouterData{}, fmt.Errorf("router %q: RemotePort is required", router.Name)
	}
	localPort := int(*router.Spec.BGP.LocalPort)
	remotePort := int(*router.Spec.BGP.RemotePort)

	t, err := time.ParseDuration(router.Spec.BGP.HoldTime)
	if err != nil {
		return bgpRouterData{}, fmt.Errorf("couldn't parse holdTime: %w", err)
	}
	holdTime := strconv.Itoa(int(t.Seconds()))

	bfd := "bfd off;"
	if router.Spec.BGP.BFD != nil {
		bfd = formatBFD(router.Spec.BGP.BFD)
	}

	ipFamily := ipFamilyIPv4
	if isIPv6(router.Spec.Address) {
		ipFamily = ipFamilyIPv6
	}

	return bgpRouterData{
		Name:       router.Name,
		Interface:  router.Spec.Interface,
		Address:    router.Spec.Address,
		LocalPort:  localPort,
		RemotePort: remotePort,
		LocalASN:   router.Spec.BGP.LocalASN,
		RemoteASN:  router.Spec.BGP.RemoteASN,
		BFD:        bfd,
		HoldTime:   holdTime,
		IPFamily:   ipFamily,
	}, nil
}

func toStaticRouterData(router *meridio2v1alpha1.GatewayRouter) staticRouterData {
	bfd := ""
	if isStaticBFDOn(router) {
		bfd = " bfd"
	}

	ipFamily := ipFamilyIPv4
	if isIPv6(router.Spec.Address) {
		ipFamily = ipFamilyIPv6
	}

	return staticRouterData{
		Name:      router.Name,
		Interface: router.Spec.Interface,
		Address:   router.Spec.Address,
		BFD:       bfd,
		IPFamily:  ipFamily,
	}
}

func isStaticBFDOn(router *meridio2v1alpha1.GatewayRouter) bool {
	return router.Spec.Static != nil && router.Spec.Static.BFD != nil
}

func formatBFD(spec *meridio2v1alpha1.BfdSpec) string {
	params := formatBFDInterfaceParams(*spec)
	if params != "" {
		return fmt.Sprintf("bfd { %s };", params)
	}
	return "bfd on;"
}

// formatBFDInterfaceParams formats BFD parameters for the protocol bfd interface block.
// All fields are required by the BfdSpec API, so no empty checks are needed.
func formatBFDInterfaceParams(spec meridio2v1alpha1.BfdSpec) string {
	return strings.Join([]string{
		fmt.Sprintf("min rx interval %s;", spec.MinRx),
		fmt.Sprintf("min tx interval %s;", spec.MinTx),
		fmt.Sprintf("multiplier %d;", spec.Multiplier),
	}, " ")
}

func isIPv6(ipOrCIDR string) bool {
	ip, _, err := net.ParseCIDR(ipOrCIDR)
	if err != nil {
		ip = net.ParseIP(ipOrCIDR)
	}
	return ip != nil && ip.To4() == nil
}
