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
	"cmp"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
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
	TcpAo      string // rendered TCP-AO block, empty when unused
	Md5        string // rendered MD5 password line, empty when unused
}

type staticRouterData struct {
	Name      string
	Interface string
	Address   string
	BFD       string
	IPFamily  string
}

type tcpAoData struct {
	Keys      []tcpAoKeyData
	NextKeyId *uint8
}

type tcpAoKeyData struct {
	SendId uint8
	RecvId uint8
	// Secret is the raw key material; the template renders it as a BIRD "hex:"
	// bytestring with a human-readable comment via the birdBytestring and
	// birdSecretComment template functions.
	Secret    string
	Algorithm string
	Preferred bool
}

var tmplFuncs = template.FuncMap{
	"defaultRoute": func(ipFamily string) string {
		if ipFamily == ipFamilyIPv6 {
			return defaultRouteIPv6
		}
		return defaultRouteIPv4
	},
	// bgpTemplateName selects the per-family BGP template so a protocol only
	// declares the channel it actually uses. BIRD advertises one multiprotocol
	// AFI/SAFI capability per configured channel (see BIRD user guide, "Channel
	// configuration"), so a template declaring both ipv4{} and ipv6{} would
	// advertise both capabilities to the peer even for a single-family session.
	"bgpTemplateName": func(ipFamily string) string {
		if ipFamily == ipFamilyIPv6 {
			return "BGP_TEMPLATE_V6"
		}
		return "BGP_TEMPLATE_V4"
	},
}

// birdConfigTmpl renders the full bird.conf.
//
// Note the `timeformat protocol "%s"` directive: it makes 'show protocols' report protocol times as
// Unix epoch seconds, which the metrics collector subtracts from the current time to derive
// bgp_session_uptime_seconds. BIRD's default for protocol times is 'iso short ms', a shorthand for
// "%T" 72000 "%F" — a time of day for the last 20 hours and a bare date beyond that — so the
// representation changes with age and loses either the date or the time, which cannot be parsed
// reliably into a timestamp. Only protocol times are overridden; route, base and log formats keep
// their defaults. The trade-off is that 'birdc show protocols' is less human-readable (epoch
// instead of clock time). See docs/design/router-metrics-source-of-truth.md.
//
//nolint:lll
var birdConfigTmpl = template.Must(template.New("bird.conf").Funcs(tmplFuncs).Parse(`{{- range .LogParams}}
{{.FmtParams}}
{{- end}}

# Unix epoch seconds, for machine-readable 'show protocols' timestamps.
timeformat protocol "%s";

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

template bgp BGP_TEMPLATE_V4 {
	debug {events, states, interfaces};
	direct;
	bfd off;
	graceful restart off;
	setkey off;
	ipv4 {
		import none;
		export none;
		next hop self;
	};
}

template bgp BGP_TEMPLATE_V6 {
	debug {events, states, interfaces};
	direct;
	bfd off;
	graceful restart off;
	setkey off;
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

protocol bgp 'NBR-{{.Name}}' from {{bgpTemplateName .IPFamily}} {
	interface "{{.Interface}}";
	local port {{.LocalPort}} as {{.LocalASN}};
	neighbor {{.Address}} port {{.RemotePort}} as {{.RemoteASN}};
	{{.BFD}}
	hold time {{.HoldTime}};
	{{.IPFamily}} {
		import filter gateway_routes;
		export filter announced_routes;
	};
	{{- if .TcpAo}}
	{{.TcpAo}}
	{{- end}}
	{{- if .Md5}}
	{{.Md5}}
	{{- end}}
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

var tcpAoTmpl = template.Must(template.New("tcpao").Funcs(template.FuncMap{
	"deref":             func(p *uint8) uint8 { return *p },
	"birdBytestring":    birdBytestring,
	"birdSecretComment": birdSecretComment,
}).Parse(`authentication ao;
	keys {
{{- range .Keys}}
		key {
			send id {{.SendId}};
			recv id {{.RecvId}};
			secret {{birdBytestring .Secret}}; # {{birdSecretComment .Secret}}
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

func toBGPRouterData(router *meridio2v1alpha1.GatewayRouter, auth ResolvedAuth) (bgpRouterData, error) {
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
	// tcpAoConfig and md5Config tolerate a nil spec; guard only the
	// Authentication wrapper. The API's discriminated union guarantees at
	// most one of TcpAo/Md5 is set, so at most one rendered block is
	// non-empty.
	var tcpAoSpec *meridio2v1alpha1.BgpTcpAoSpec
	//nolint:staticcheck // SA1019: this code implements the deprecated MD5 mechanism.
	var md5Spec *meridio2v1alpha1.BgpMd5Spec
	if router.Spec.BGP.Authentication != nil {
		tcpAoSpec = router.Spec.BGP.Authentication.TcpAo
		//nolint:staticcheck // SA1019: this code implements the deprecated MD5 mechanism.
		md5Spec = router.Spec.BGP.Authentication.Md5
	}
	tcpAo := tcpAoConfig(tcpAoSpec, auth.TcpAoPasswords)
	md5 := md5Config(md5Spec, auth.Md5Password)

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
		TcpAo:      tcpAo,
		Md5:        md5,
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
			Secret:    password,
			Algorithm: key.Algorithm,
			Preferred: tcpAo.CurrentKeyId != nil && *tcpAo.CurrentKeyId == key.SendId,
		})
	}

	if len(keys) == 0 {
		return ""
	}

	slices.SortFunc(keys, func(a, b tcpAoKeyData) int {
		return cmp.Compare(a.SendId, b.SendId)
	})
	var buf strings.Builder
	if err := tcpAoTmpl.Execute(&buf, tcpAoData{Keys: keys, NextKeyId: tcpAo.NextKeyId}); err != nil {
		return ""
	}
	return buf.String()
}

//nolint:staticcheck // SA1019: this code implements the deprecated MD5 mechanism.
func md5Config(md5 *meridio2v1alpha1.BgpMd5Spec, password string) string {
	if md5 == nil || password == "" {
		return ""
	}
	// BGP's "password" option takes a quoted string, not a bytestring (BIRD
	// proto/bgp/config.Y: "PASSWORD text"), so unlike TCP-AO the key cannot be
	// hex-encoded. BIRD does no unescaping inside quoted strings, so the raw key
	// bytes are emitted verbatim between the quotes; the caller must have
	// rejected any key that is not QuotedStringRepresentable, otherwise the
	// emitted config would be corrupt or unparseable. No human-readable comment
	// is added (unlike TCP-AO): the quoted key is already human-readable, so a
	// strconv.Quote rendering would be redundant.
	return fmt.Sprintf("password \"%s\";", password)
}

// MaxKeyLength is the maximum BGP authentication key length in bytes, shared by
// TCP-AO and MD5. It matches Linux TCP_AO_MAXKEYLEN and TCP_MD5SIG_MAXKEYLEN
// (both 80, see <linux/tcp.h>) and BIRD's AO_MAX_KEY_LENGTH (lib/socket.h). The
// kernel measures this in bytes, not characters, so a multi-byte UTF-8 key can
// exceed the limit with fewer than 80 runes. Keys longer than this are rejected
// before reaching BIRD: an over-length TCP-AO secret is a BIRD config-load
// error, and an over-length MD5 key is silently accepted by BIRD but then
// rejected by the kernel setsockopt(TCP_MD5SIG), breaking the session.
const MaxKeyLength = 80

// birdBytestring encodes arbitrary key material as a BIRD "hex:" bytestring,
// for use with the TCP-AO "secret" option.
//
// BIRD's configuration lexer performs no escape processing inside double-quoted
// strings (see BIRD conf/cf-lex.l, QUOTED state): a double quote terminates the
// string, a backslash is stored literally, and a newline is a hard parse error.
// A Kubernetes Secret value is an arbitrary byte sequence, so it cannot be
// represented losslessly as a quoted string. The TCP-AO "secret" option accepts
// a bytestring (BIRD proto/bgp/config.Y: "SECRET bytestring_or_text"), so
// encoding the key as "hex:<hex>" makes BIRD see exactly the intended bytes and
// can never trip the parser. Note this does not apply to the MD5 "password"
// option, which takes a quoted string only; see md5Config.
func birdBytestring(s string) string {
	return "hex:" + hex.EncodeToString([]byte(s))
}

// QuotedStringRepresentable reports whether s can be carried by a BIRD quoted
// string and the kernel's TCP MD5 C-string API. BIRD does no unescaping inside
// quoted strings, so a double quote would terminate the string and a newline
// (or carriage return) is a parse error; the kernel measures the MD5 key with
// strlen (sysdep/linux/sysio.h), so an embedded NUL would truncate it. Such
// bytes cannot be expressed, so a key containing any of them must be rejected
// rather than silently corrupted. This constraint is specific to MD5; TCP-AO
// keys are hex-encoded (see birdBytestring) and may contain any byte.
func QuotedStringRepresentable(s string) bool {
	return !strings.ContainsAny(s, "\x00\"\n\r")
}

// birdSecretComment renders key material as a single-line, human-readable BIRD
// comment body, as an operator aid placed next to a TCP-AO key's "hex:"
// bytestring (which is otherwise unreadable). It uses strconv.Quote, which
// escapes quotes, backslashes, newlines and non-printable or non-ASCII bytes,
// guaranteeing the result stays on one line and therefore cannot prematurely
// terminate the "#" comment it is embedded in. The MD5 path does not use this:
// its quoted key is already human-readable.
func birdSecretComment(s string) string {
	return strconv.Quote(s)
}

func isIPv6(ipOrCIDR string) bool {
	ip, _, err := net.ParseCIDR(ipOrCIDR)
	if err != nil {
		ip = net.ParseIP(ipOrCIDR)
	}
	return ip != nil && ip.To4() == nil
}
