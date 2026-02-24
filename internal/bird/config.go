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
	KernelTableID int
	LogFile       string
	IPv4VIPs      []string
	IPv6VIPs      []string
	Routers       []routerData
}

type routerData struct {
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

//nolint:lll
var birdConfigTmpl = template.Must(template.New("bird.conf").Parse(`log stderr all;
{{- if .LogFile}}
log "{{.LogFile}}" all;
{{- end}}

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
	kernel table {{.KernelTableID}};
	merge paths on;
}

protocol kernel {
	ipv6 {
		import none;
		export filter gateway_routes;
	};
	kernel table {{.KernelTableID}};
	merge paths on;
}

protocol bfd {
	interface "*" {};
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
`))

func toRouterData(router *meridio2v1alpha1.GatewayRouter) (routerData, error) {
	localPort := defaultLocalPort
	if router.Spec.BGP.LocalPort != nil {
		localPort = int(*router.Spec.BGP.LocalPort)
	}
	remotePort := defaultRemotePort
	if router.Spec.BGP.RemotePort != nil {
		remotePort = int(*router.Spec.BGP.RemotePort)
	}

	holdTime := "90"
	if router.Spec.BGP.HoldTime != "" {
		t, err := time.ParseDuration(router.Spec.BGP.HoldTime)
		if err != nil {
			return routerData{}, fmt.Errorf("couldn't parse holdTime: %w", err)
		}
		holdTime = strconv.Itoa(int(t.Seconds()))
	}

	bfd := "bfd off;"
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
			bfd = fmt.Sprintf("bfd {\n%s\t};", bfdConf)
		} else {
			bfd = "bfd on;"
		}
	}

	ipFamily := "ipv4"
	if isIPv6(router.Spec.Address) {
		ipFamily = "ipv6"
	}

	return routerData{
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

func isIPv6(ipOrCIDR string) bool {
	ip, _, err := net.ParseCIDR(ipOrCIDR)
	if err != nil {
		ip = net.ParseIP(ipOrCIDR)
	}
	return ip != nil && ip.To4() == nil
}
