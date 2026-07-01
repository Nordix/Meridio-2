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

type birdConfigData struct {
	KernelTableID  int
	KernelScanTime int
	LogParams      BirdLogParams
	IPv4VIPs       []string
	IPv6VIPs       []string
	Routers        []routerData
	BGPInterfaces  []string
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
	TcpAo      string
}

//nolint:lll
var birdConfigTmpl = template.Must(template.New("bird.conf").Parse(`{{- range .LogParams}}
{{.FmtParams}}
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
{{- if .BGPInterfaces}}
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
	{{.TcpAo}}
}
{{- end}}
`))

func toRouterData(router *meridio2v1alpha1.GatewayRouter, passwords map[uint8]string) (routerData, error) {
	if router.Spec.BGP.LocalPort == nil {
		return routerData{}, fmt.Errorf("router %q: LocalPort is required", router.Name)
	}
	if router.Spec.BGP.RemotePort == nil {
		return routerData{}, fmt.Errorf("router %q: RemotePort is required", router.Name)
	}
	localPort := int(*router.Spec.BGP.LocalPort)
	remotePort := int(*router.Spec.BGP.RemotePort)

	t, err := time.ParseDuration(router.Spec.BGP.HoldTime)
	if err != nil {
		return routerData{}, fmt.Errorf("couldn't parse holdTime: %w", err)
	}
	holdTime := strconv.Itoa(int(t.Seconds()))

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
	tcpAo := tcpAoConfig(router.Spec.BGP.Authentication, passwords)

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
		TcpAo:      tcpAo,
	}, nil
}

type tcpAoData struct {
	Keys      []tcpAoKeyData
	NextKeyId *uint8
}

type tcpAoKeyData struct {
	SendId    uint8
	RecvId    uint8
	Secret    string
	Algorithm string
	Preferred bool
}

var tcpAoTmpl = template.Must(template.New("tcpao").Funcs(template.FuncMap{
	"deref": func(p *uint8) uint8 { return *p },
}).Parse(`authentication ao;
	keys {
{{- range .Keys}}
		key {
			send id {{.SendId}};
			recv id {{.RecvId}};
			secret "{{.Secret}}";
			algorithm {{.Algorithm}};
{{- if .Preferred}}
			preferred;
{{- end}}
		};
{{- end}}
	};
{{- if .NextKeyId}}
	rnext id {{deref .NextKeyId}};
{{- end}}`))

func tcpAoConfig(tcpAo *meridio2v1alpha1.BgpTcpAoSpec, passwords map[uint8]string) string {
	if tcpAo == nil || len(tcpAo.Keychain) == 0 {
		return ""
	}

	keys := make([]tcpAoKeyData, 0, len(tcpAo.Keychain))
	for _, key := range tcpAo.Keychain {
		password, ok := passwords[key.SendId]
		if !ok {
			continue
		}
		keys = append(keys, tcpAoKeyData{
			SendId:    key.SendId,
			RecvId:    key.RecvId,
			Secret:    escapeBirdString(password),
			Algorithm: convertAlgorithm(key.Algorithm),
			Preferred: tcpAo.CurrentKeyId == nil || *tcpAo.CurrentKeyId == key.SendId,
		})
	}

	if len(keys) == 0 {
		return ""
	}

	var buf strings.Builder
	if err := tcpAoTmpl.Execute(&buf, tcpAoData{Keys: keys, NextKeyId: tcpAo.NextKeyId}); err != nil {
		return ""
	}
	return buf.String()
}

func escapeBirdString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return s
}

func convertAlgorithm(algo string) string {
	// Convert from CRD format to BIRD format
	switch algo {
	case "hmac-md5":
		return "hmac md5"
	case "hmac-sha-1":
		return "hmac sha1"
	case "hmac-sha-224":
		return "hmac sha224"
	case "hmac-sha-256":
		return "hmac sha256"
	case "hmac-sha-384":
		return "hmac sha384"
	case "hmac-sha-512":
		return "hmac sha512"
	case "cmac-aes-128":
		return "cmac aes128"
	case "cmac-aes-256":
		return "cmac aes256"
	case "umac-64":
		return "umac64"
	case "umac-128":
		return "umac128"
	default:
		return algo
	}
}

func isIPv6(ipOrCIDR string) bool {
	ip, _, err := net.ParseCIDR(ipOrCIDR)
	if err != nil {
		ip = net.ParseIP(ipOrCIDR)
	}
	return ip != nil && ip.To4() == nil
}
