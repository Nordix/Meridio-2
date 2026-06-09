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
	"text/template"
	"time"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

const (
	defaultKernelTableID = 4096
	defaultLocalPort     = 179
	defaultRemotePort    = 179
)

type birdConfigData struct {
	KernelTableID  int
	KernelScanTime int
	LogParams      BirdLogParams
	IPv4VIPs       []string
	IPv6VIPs       []string
	Routers        []routerData
	BGPInterfaces  []string
	BFDInterfaces  []string
}

type routerData struct {
	Name         string
	Interface    string
	Address      string
	LocalPort    int
	RemotePort   int
	LocalASN     uint32
	RemoteASN    uint32
	BFD          string
	HoldTime     string
	IPFamily     string
	Protocol     string
	DefaultRoute string
	BFDEnabled   bool
}

//nolint:lll
var birdConfigTmpl = template.Must(template.New("bird.conf").Parse(`{{- range .LogParams}}
{{.FmtParams}}
{{- end}}

protocol device {}

filter default_rt {
	if ( net ~ [ 0.0.0.0/0 ] ) then accept;
	if ( net ~ [ 0::/0 ] ) then accept;
	else reject;
}

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
{{.}}
{{- end}}
{{- else if .BGPInterfaces}}
{{- range .BGPInterfaces}}
	interface "{{.}}" {};
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
{{- range .Routers}}
{{- if eq .Protocol "Static"}}

protocol static 'NBR-{{.Name}}' {
	{{.IPFamily}} {
		import filter default_rt;
	};
	route {{.DefaultRoute}} via {{.Address}}%'{{.Interface}}'{{if .BFDEnabled}} bfd{{end}};
}
{{- else}}

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
{{- end}}
`))

func toRouterData(router *meridio2v1alpha1.GatewayRouter) (routerData, error) {
	ipFamily := "ipv4"
	if isIPv6(router.Spec.Address) {
		ipFamily = "ipv6"
	}

	rd := routerData{
		Name:      router.Name,
		Interface: router.Spec.Interface,
		Address:   router.Spec.Address,
		IPFamily:  ipFamily,
		Protocol:  string(router.Spec.Protocol),
	}

	if router.Spec.Protocol == meridio2v1alpha1.RoutingProtocolStatic {
		// Static
		rd.DefaultRoute = defaultRoute(ipFamily)
		rd.BFDEnabled = router.Spec.Static != nil &&
			router.Spec.Static.BFD != nil &&
			router.Spec.Static.BFD.Switch != nil &&
			*router.Spec.Static.BFD.Switch
	} else {
		// BGP
		rd.LocalPort = defaultLocalPort
		if router.Spec.BGP.LocalPort != nil {
			rd.LocalPort = int(*router.Spec.BGP.LocalPort)
		}
		rd.RemotePort = defaultRemotePort
		if router.Spec.BGP.RemotePort != nil {
			rd.RemotePort = int(*router.Spec.BGP.RemotePort)
		}
		rd.LocalASN = router.Spec.BGP.LocalASN
		rd.RemoteASN = router.Spec.BGP.RemoteASN

		rd.HoldTime = "90"
		if router.Spec.BGP.HoldTime != "" {
			t, err := time.ParseDuration(router.Spec.BGP.HoldTime)
			if err != nil {
				return routerData{}, fmt.Errorf("couldn't parse holdTime: %w", err)
			}
			rd.HoldTime = strconv.Itoa(int(t.Seconds()))
		}

		rd.BFD = "bfd off;"
		if router.Spec.BGP.BFD != nil && router.Spec.BGP.BFD.Switch != nil && *router.Spec.BGP.BFD.Switch {
			bfdConf := ""
			if router.Spec.BGP.BFD.MinRx != "" {
				bfdConf += fmt.Sprintf("\t\tmin rx interval %s;\n", router.Spec.BGP.BFD.MinRx)
			}
			if router.Spec.BGP.BFD.MinTx != "" {
				bfdConf += fmt.Sprintf("\t\tmin tx interval %s;\n", router.Spec.BGP.BFD.MinTx)
			}
			if router.Spec.BGP.BFD.Multiplier != nil {
				bfdConf += fmt.Sprintf("\t\tmultiplier %d;\n", *router.Spec.BGP.BFD.Multiplier)
			}
			if bfdConf != "" {
				rd.BFD = fmt.Sprintf("bfd {\n%s\t};", bfdConf)
			} else {
				rd.BFD = "bfd on;"
			}
		}
	}

	return rd, nil
}

func defaultRoute(ipFamily string) string {
	if ipFamily == "ipv6" {
		return "0::/0"
	}
	return "0.0.0.0/0"
}

// bfdInterfaceConfig returns the BFD interface block content
// for the protocol bfd section. For static routers with BFD timers,
// it returns the interface with timer parameters.
func bfdInterfaceConfig(router *meridio2v1alpha1.GatewayRouter) string {
	if router.Spec.Protocol == meridio2v1alpha1.RoutingProtocolStatic &&
		router.Spec.Static != nil && router.Spec.Static.BFD != nil &&
		router.Spec.Static.BFD.Switch != nil && *router.Spec.Static.BFD.Switch {
		bfd := router.Spec.Static.BFD
		conf := ""
		if bfd.MinRx != "" {
			conf += fmt.Sprintf("\t\tmin rx interval %s;\n", bfd.MinRx)
		}
		if bfd.MinTx != "" {
			conf += fmt.Sprintf("\t\tmin tx interval %s;\n", bfd.MinTx)
		}
		if bfd.Multiplier != nil {
			conf += fmt.Sprintf("\t\tmultiplier %d;\n", *bfd.Multiplier)
		}
		if conf != "" {
			return fmt.Sprintf("\tinterface \"%s\" {\n%s\t};", router.Spec.Interface, conf)
		}
	}
	return ""
}

func isIPv6(ipOrCIDR string) bool {
	ip, _, err := net.ParseCIDR(ipOrCIDR)
	if err != nil {
		ip = net.ParseIP(ipOrCIDR)
	}
	return ip != nil && ip.To4() == nil
}
