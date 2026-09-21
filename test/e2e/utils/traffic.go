//go:build e2e
// +build e2e

package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// E2EVPNGatewayExecEnv is the environment variable used to select how commands
// are executed against the VPN gateway. When unset, defaults to "docker exec
// vpn-gateway" (Kind suites, where the VPN gateway runs as a Docker container
// on the host). Set to "kubectl exec -n <namespace> vpn-gateway --" (or the
// "oc" equivalent) for suites where the VPN gateway runs as a Pod inside the
// cluster (e.g., the OpenShift CRC suite).
const E2EVPNGatewayExecEnv = "E2E_VPN_GATEWAY_EXEC"

// vpnGatewayExecPrefix returns the shell command prefix used to run a command
// against the VPN gateway (Docker container or in-cluster Pod), honoring
// E2EVPNGatewayExecEnv when set.
func vpnGatewayExecPrefix() string {
	if prefix := os.Getenv(E2EVPNGatewayExecEnv); prefix != "" {
		return prefix
	}
	return "docker exec vpn-gateway"
}

// SendTraffic sends traffic from the VPN gateway container to the given VIP:port.
// Returns a map of target hostname → connection count, and the number of lost connections.
func SendTraffic(vip string, port int, protocol string, nconn int) (map[string]int, int, error) {
	addr := fmt.Sprintf("%s:%d", vip, port)
	if strings.Contains(vip, ":") {
		addr = fmt.Sprintf("[%s]:%d", vip, port) // IPv6
	}

	protoFlag := ""
	if protocol == "udp" {
		protoFlag = "-udp"
	}

	cmdStr := fmt.Sprintf(
		"%s ctraffic %s -address %s -nconn %d -timeout 10s -stats all",
		vpnGatewayExecPrefix(), protoFlag, addr, nconn,
	)
	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, 0, fmt.Errorf("ctraffic failed: %w\noutput: %s", err, string(out))
	}

	return parseCtrafficOutput(out)
}

// TrafficHandle represents a ctraffic run launched in the background by
// StartTraffic. Call Wait to block until the run finishes and obtain its stats.
type TrafficHandle struct {
	cmd *exec.Cmd
	out *bytes.Buffer
}

// StartTraffic launches ctraffic in the background against the given VIP:port,
// holding nconn connections open for the given duration. Because it is
// non-blocking, the caller can trigger an action (e.g. scaling endpoints)
// while traffic is in flight, then call Wait to measure how the in-flight
// connections were affected.
//
// It uses ctraffic's continuous-traffic/monitor mode (v1.10.x), which reports
// lost connections and disturbances over the run window. The -retries option
// keeps a short scale-induced blip from aborting the run prematurely while
// still surfacing it in the stats.
func StartTraffic(vip string, port int, protocol string, nconn int, duration time.Duration, retries int) (*TrafficHandle, error) {
	addr := fmt.Sprintf("%s:%d", vip, port)
	if strings.Contains(vip, ":") {
		addr = fmt.Sprintf("[%s]:%d", vip, port) // IPv6
	}

	protoFlag := ""
	if protocol == "udp" {
		protoFlag = "-udp"
	}

	cmdStr := fmt.Sprintf(
		"%s ctraffic %s -address %s -nconn %d -timeout %s -monitor -stats all -retries %d",
		vpnGatewayExecPrefix(), protoFlag, addr, nconn, duration.String(), retries,
	)
	cmd := exec.Command("/bin/sh", "-c", cmdStr)

	out := &bytes.Buffer{}
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ctraffic: %w", err)
	}

	return &TrafficHandle{cmd: cmd, out: out}, nil
}

// Wait blocks until the background ctraffic run started by StartTraffic
// finishes, then returns a map of target hostname → connection count and the
// number of lost connections. A non-zero lost count does not by itself return
// an error; callers decide whether the loss is within an acceptable bound.
func (h *TrafficHandle) Wait() (map[string]int, int, error) {
	waitErr := h.cmd.Wait()

	// In -monitor mode ctraffic prints human-readable interval lines before the
	// final JSON stats object. Extract the JSON object (first '{' .. last '}')
	// so parseCtrafficOutput sees only valid JSON, matching non-monitor output.
	raw := h.out.Bytes()
	jsonBytes := extractJSONObject(raw)

	// ctraffic exits non-zero on "too many reconnects" but still prints the
	// final JSON stats block; parse first and only surface the wait error if
	// the output could not be parsed.
	hosts, lost, parseErr := parseCtrafficOutput(jsonBytes)
	if parseErr != nil {
		if waitErr != nil {
			return nil, 0, fmt.Errorf("ctraffic failed: %w\noutput: %s", waitErr, h.out.String())
		}
		return nil, 0, parseErr
	}

	return hosts, lost, nil
}

// extractJSONObject returns the substring from the first '{' to the last '}'
// (inclusive) in b, or b unchanged if no such bounds are found. This strips
// any leading -monitor interval lines that precede the JSON stats object.
func extractJSONObject(b []byte) []byte {
	start := bytes.IndexByte(b, '{')
	end := bytes.LastIndexByte(b, '}')
	if start < 0 || end < 0 || end < start {
		return b
	}
	return b[start : end+1]
}

// Ping sends ICMP echo from the VPN gateway to the given VIP.
func Ping(vip string) error {
	pingCmd := "ping"
	if strings.Contains(vip, ":") {
		pingCmd = "ping6"
	}
	cmdStr := fmt.Sprintf("%s %s -c 3 -W 2 %s", vpnGatewayExecPrefix(), pingCmd, vip)
	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w\noutput: %s", pingCmd, err, string(out))
	}
	return nil
}

// PingLargePacket sends a large ICMP echo with DF bit set from the VPN gateway.
// Used to test PMTU discovery: if the packet exceeds the internal network MTU,
// the LB should return an ICMP Frag Needed / Packet Too Big with the VIP as source.
// size is the ICMP payload size in bytes (total packet = size + IP/ICMP headers).
func PingLargePacket(vip string, size int) error {
	pingCmd := "ping"
	// -M do = set DF bit (prohibit fragmentation)
	// -s = payload size
	sizeFlag := fmt.Sprintf("-s %d -M do", size)
	if strings.Contains(vip, ":") {
		pingCmd = "ping6"
		// IPv6 always has DF equivalent (no fragmentation by routers)
		sizeFlag = fmt.Sprintf("-s %d", size)
	}
	cmdStr := fmt.Sprintf("%s %s %s -c 3 -W 5 %s", vpnGatewayExecPrefix(), pingCmd, sizeFlag, vip)
	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s (size=%d) failed: %w\noutput: %s", pingCmd, size, err, string(out))
	}
	return nil
}

// VerifyPMTU sends an oversized ICMP echo (DF set) and verifies that:
// 1. The ping fails (packet exceeds internal MTU)
// 2. An ICMP "need to frag" reply is received with the VIP as source address
//
// Uses tcpdump to capture the ICMP error and verify the source address.
func VerifyPMTU(vip string, size int) error {
	isIPv6 := strings.Contains(vip, ":")

	// Flush PMTU cache so the oversized packet is actually sent on the wire
	// (otherwise the kernel rejects it locally from a previous PMTU discovery)
	flushCmd := fmt.Sprintf("%s ip route flush cache %s", vpnGatewayExecPrefix(), vip)
	_ = exec.Command("/bin/sh", "-c", flushCmd).Run()

	// Build tcpdump filter for ICMP unreachable (type 3) from the VIP
	var tcpdumpFilter string
	if isIPv6 {
		tcpdumpFilter = fmt.Sprintf("icmp6 and icmp6[icmp6type] == 2 and src host %s", vip)
	} else {
		tcpdumpFilter = fmt.Sprintf("icmp and icmp[icmptype] == 3 and src host %s", vip)
	}

	// Start tcpdump in background, capture for up to 10 seconds
	tcpdumpCmd := fmt.Sprintf(
		"%s timeout 10 tcpdump -c 1 -nn -l '%s' 2>/dev/null",
		vpnGatewayExecPrefix(), tcpdumpFilter,
	)
	tcpdump := exec.Command("/bin/sh", "-c", tcpdumpCmd)
	tcpdumpOut := &strings.Builder{}
	tcpdump.Stdout = tcpdumpOut
	tcpdump.Stderr = tcpdumpOut
	if err := tcpdump.Start(); err != nil {
		return fmt.Errorf("failed to start tcpdump: %w", err)
	}

	// Give tcpdump time to start capturing
	time.Sleep(500 * time.Millisecond)

	// Send oversized ping (expect failure)
	pingCmd := "ping"
	sizeFlag := fmt.Sprintf("-s %d -M do", size)
	if isIPv6 {
		pingCmd = "ping6"
		sizeFlag = fmt.Sprintf("-s %d", size)
	}
	pingCmdStr := fmt.Sprintf("%s %s %s -c 3 -W 3 %s", vpnGatewayExecPrefix(), pingCmd, sizeFlag, vip)
	cmd := exec.Command("/bin/sh", "-c", pingCmdStr)
	pingOut, pingErr := cmd.CombinedOutput()

	// Ping must fail (packet exceeds internal MTU, DF set)
	if pingErr == nil {
		return fmt.Errorf("expected ping to fail (packet %d bytes exceeds internal MTU), but it succeeded: %s",
			size, string(pingOut))
	}

	// Wait for tcpdump to capture the ICMP error
	tcpdumpErr := tcpdump.Wait()
	captured := tcpdumpOut.String()

	// tcpdump exits 0 when it captures the requested packet count (-c 1),
	// or non-zero on timeout. Check if we got a capture.
	if tcpdumpErr != nil && !strings.Contains(captured, vip) {
		return fmt.Errorf("no ICMP Frag Needed from VIP %s captured (tcpdump: %s)", vip, captured)
	}

	// Verify the captured packet contains the VIP as source
	if !strings.Contains(captured, vip) {
		return fmt.Errorf("ICMP error source is not VIP %s, tcpdump output: %s", vip, captured)
	}

	return nil
}

// TrafficExpectation describes one protocol's expected traffic behavior
// against a VIP — the common shape shared by TCP/UDP checks across nearly
// every suite: send N connections, expect zero loss, and expect the
// connections to land on a known set of targets.
//
// Exactly one of ExpectedTargets or ExpectedHosts should be set:
//   - ExpectedTargets: only the count of distinct hosts reached matters
//     (the common case: "every one of my N targets got traffic").
//   - ExpectedHosts: the exact set of hosts reached matters (e.g.
//     pod-cache-label's negative case, where only the labeled Pod should
//     receive traffic and no others).
type TrafficExpectation struct {
	VIP             string
	Protocol        string // "tcp" or "udp"
	Port            int
	Connections     int
	ExpectedTargets int
	ExpectedHosts   []string
}

// VerifyTraffic sends traffic per exp and asserts zero loss plus the
// expected target spread. ICMP reachability is always checked first since a
// failed connection storm is a less useful signal than a failed ping.
func VerifyTraffic(exp TrafficExpectation) error {
	if err := Ping(exp.VIP); err != nil {
		return fmt.Errorf("VIP %s not reachable via ICMP: %w", exp.VIP, err)
	}

	lastingConn, lostConn, err := SendTraffic(exp.VIP, exp.Port, exp.Protocol, exp.Connections)
	if err != nil {
		return err
	}
	if lostConn != 0 {
		return fmt.Errorf("%d %s connections lost to %s:%d", lostConn, exp.Protocol, exp.VIP, exp.Port)
	}

	if exp.ExpectedHosts != nil {
		if len(lastingConn) != len(exp.ExpectedHosts) {
			return fmt.Errorf("expected exactly %d hosts reached, got %d: %v",
				len(exp.ExpectedHosts), len(lastingConn), lastingConn)
		}
		for _, h := range exp.ExpectedHosts {
			if _, ok := lastingConn[h]; !ok {
				return fmt.Errorf("expected host %s to receive traffic, got: %v", h, lastingConn)
			}
		}
	} else if len(lastingConn) != exp.ExpectedTargets {
		return fmt.Errorf("expected %d targets reached for %s:%d, got %d: %v",
			exp.ExpectedTargets, exp.VIP, exp.Port, len(lastingConn), lastingConn)
	}

	return nil
}

// ctrafficResult represents the relevant fields from ctraffic JSON output.
type ctrafficResult struct {
	FailedConnects int `json:"FailedConnects"`
	ConnStats      []struct {
		Host string `json:"Host"`
	} `json:"ConnStats"`
}

// parseCtrafficOutput parses ctraffic JSON stats output.
// Returns map[hostname]connectionCount and lostConnections.
func parseCtrafficOutput(output []byte) (map[string]int, int, error) {
	var result ctrafficResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, 0, fmt.Errorf("failed to parse ctraffic output: %w\nraw: %s", err, string(output))
	}

	hostCounts := make(map[string]int)
	for _, cs := range result.ConnStats {
		if cs.Host != "" {
			hostCounts[cs.Host]++
		}
	}

	return hostCounts, result.FailedConnects, nil
}

// NetPerfMeterConfig holds configuration for NetPerfMeter client.
type NetPerfMeterConfig struct {
	Target         string   // "VIP:port"
	LocalAddrs     []string // Client local addresses for multihoming
	Protocol       string   // "sctp", "tcp", "udp"
	Duration       int      // Test duration in seconds
	FrameRate      string   // e.g., "const0" (saturated), "const25"
	FrameSize      string   // e.g., "const1400"
	ControlOverTCP bool     // Use TCP for control channel instead of SCTP
}

// NetPerfMeterResult holds parsed NetPerfMeter output.
type NetPerfMeterResult struct {
	RawOutput        string
	TransmittedBytes int64
	ReceivedBytes    int64
	PacketLoss       int
	FrameLoss        int
}

// RunNetPerfMeterClient runs NetPerfMeter client from vpn-gateway container.
func RunNetPerfMeterClient(cfg NetPerfMeterConfig) (*NetPerfMeterResult, error) {
	localAddrsArg := ""
	if len(cfg.LocalAddrs) > 0 {
		localAddrsArg = fmt.Sprintf("--local=%s", strings.Join(cfg.LocalAddrs, ","))
	}

	controlOverTCPArg := ""
	if cfg.ControlOverTCP {
		controlOverTCPArg = "--control-over-tcp"
	}

	trafficSpec := fmt.Sprintf("%s:%s:%s:%s", cfg.FrameRate, cfg.FrameSize, cfg.FrameRate, cfg.FrameSize)

	cmdStr := fmt.Sprintf(
		"docker exec vpn-gateway netperfmeter %s %s %s -runtime=%d -%s %s",
		cfg.Target, localAddrsArg, controlOverTCPArg, cfg.Duration, cfg.Protocol, trafficSpec,
	)

	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("netperfmeter failed: %w\noutput: %s", err, string(out))
	}

	result := &NetPerfMeterResult{RawOutput: string(out)}
	if parseErr := parseNetPerfMeterOutput(result); parseErr != nil {
		return result, fmt.Errorf("failed to parse output: %w", parseErr)
	}

	return result, nil
}

// parseNetPerfMeterOutput extracts key metrics from NetPerfMeter output.
func parseNetPerfMeterOutput(result *NetPerfMeterResult) error {
	lines := strings.Split(result.RawOutput, "\n")

	// Parse "Bytes: X B" (may have trailing content like "-> Y B/s")
	bytesRe := regexp.MustCompile(`^\s*\*\s+Bytes:\s+(\d+)\s+B`)
	// Parse "Packet Loss: X packets" or "Packets: X packets"
	pktLossRe := regexp.MustCompile(`^\s*\*\s+Packets:\s+(\d+)\s+packets`)
	// Parse "Frame Loss: X frames" or "Frames: X frames"
	frameLossRe := regexp.MustCompile(`^\s*\*\s+Frames:\s+(\d+)\s+frames`)

	inTransmission := false
	inReception := false
	inLoss := false

	for _, line := range lines {
		if strings.Contains(line, "- Transmission:") {
			inTransmission = true
			inReception = false
			inLoss = false
			continue
		}
		if strings.Contains(line, "- Reception:") {
			inTransmission = false
			inReception = true
			inLoss = false
			continue
		}
		if strings.Contains(line, "- Loss:") {
			inTransmission = false
			inReception = false
			inLoss = true
			continue
		}

		if inTransmission {
			if match := bytesRe.FindStringSubmatch(line); match != nil {
				val, _ := strconv.ParseInt(match[1], 10, 64)
				result.TransmittedBytes = val
			}
		}

		if inReception {
			if match := bytesRe.FindStringSubmatch(line); match != nil {
				val, _ := strconv.ParseInt(match[1], 10, 64)
				result.ReceivedBytes = val
			}
		}

		if inLoss {
			if match := pktLossRe.FindStringSubmatch(line); match != nil {
				val, _ := strconv.Atoi(match[1])
				result.PacketLoss = val
			}
			if match := frameLossRe.FindStringSubmatch(line); match != nil {
				val, _ := strconv.Atoi(match[1])
				result.FrameLoss = val
			}
		}
	}

	return nil
}

// CheckSCTPAssociation checks if an SCTP association exists on vpn-gateway for the given port and local addresses.
// Returns true if found, along with the association details.
func CheckSCTPAssociation(port int, localAddrs []string) (bool, string, error) {
	cmdStr := "docker exec vpn-gateway cat /proc/net/sctp/assocs"
	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, "", fmt.Errorf("failed to read SCTP associations: %w\noutput: %s", err, string(out))
	}

	lines := strings.Split(string(out), "\n")
	portStr := fmt.Sprintf(" %d ", port) // Space-padded to match column format

	for _, line := range lines {
		// Skip header line
		if strings.Contains(line, "ASSOC") && strings.Contains(line, "SOCK") {
			continue
		}

		// Check if line contains the remote port (RPORT column)
		if !strings.Contains(line, portStr) {
			continue
		}

		// Check if ALL local addresses appear in LADDRS
		allLocalAddrsFound := true
		for _, addr := range localAddrs {
			if !strings.Contains(line, addr) {
				allLocalAddrsFound = false
				break
			}
		}

		if allLocalAddrsFound {
			return true, line, nil
		}
	}

	return false, "", nil
}

// CheckSCTPAssociationWithVIPs checks SCTP association and verifies both local addresses and VIPs are present.
func CheckSCTPAssociationWithVIPs(port int, localAddrs []string, vips []string) (bool, string, error) {
	cmdStr := "docker exec vpn-gateway cat /proc/net/sctp/assocs"
	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, "", fmt.Errorf("failed to read SCTP associations: %w\noutput: %s", err, string(out))
	}

	lines := strings.Split(string(out), "\n")
	portStr := fmt.Sprintf(" %d ", port)

	for _, line := range lines {
		// Skip header line
		if strings.Contains(line, "ASSOC") && strings.Contains(line, "SOCK") {
			continue
		}

		// Check remote port
		if !strings.Contains(line, portStr) {
			continue
		}

		// Check ALL local addresses are present
		allLocalAddrsFound := true
		for _, addr := range localAddrs {
			if !strings.Contains(line, addr) {
				allLocalAddrsFound = false
				break
			}
		}

		// Check ALL VIPs are present
		allVIPsFound := true
		for _, vip := range vips {
			if !strings.Contains(line, vip) {
				allVIPsFound = false
				break
			}
		}

		if allLocalAddrsFound && allVIPsFound {
			return true, line, nil
		}
	}

	return false, "", nil
}

// runBirdcOnVPNGateway runs a birdc command on the VPN gateway (the simulated
// DCGW) and returns its combined output. The gateway's BIRD uses the default
// control socket (/run/bird/bird.ctl), so no -s flag is needed. Works for both
// Kind (docker exec) and in-cluster Pod (kubectl/oc exec) suites via
// vpnGatewayExecPrefix().
func runBirdcOnVPNGateway(args string) (string, error) {
	cmdStr := fmt.Sprintf("%s birdc %s", vpnGatewayExecPrefix(), args)
	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("birdc %q failed: %w\noutput: %s", args, err, string(out))
	}
	return string(out), nil
}

// VPNGatewayHasRoute checks, from the DCGW/VPN gateway side, whether a route for
// the exact given prefix (e.g. a VIP "10.0.0.1/32") has been received via BGP.
//
// It inspects `birdc show route for <prefix> all`. Note that BIRD's `show route
// for` returns the longest-matching route, so for an absent prefix it falls back
// to the default route (0.0.0.0/0 blackhole). This helper therefore requires the
// output to contain a route line for the *exact* prefix AND that route to be
// sourced from BGP ("source: BGP"), avoiding false positives from the default
// fallback or a locally-originated static/blackhole route.
//
// This is the end-to-end proof that the LB advertised the VIP and the DCGW
// installed it.
func VPNGatewayHasRoute(prefix string) (bool, error) {
	out, err := runBirdcOnVPNGateway(fmt.Sprintf("show route for %s all", prefix))
	if err != nil {
		return false, err
	}
	return routeIsBGPSourced(out, prefix), nil
}

// routeIsBGPSourced reports whether the exact prefix is present in `birdc show
// route ... all` output AND that route was learned via BGP (not a local
// static/blackhole route). This is the pure decision behind VPNGatewayHasRoute,
// factored out so it can be unit-tested without shelling out to the gateway.
func routeIsBGPSourced(out, prefix string) bool {
	block, found := exactPrefixBlock(out, prefix)
	if !found {
		return false
	}
	// BIRD 3.x prints "source: BGP" for BGP-learned routes in `all` mode.
	return strings.Contains(block, "source: BGP")
}

// VPNGatewayRouteNextHops returns the next-hop addresses the DCGW has installed
// for the exact given prefix, parsed from `birdc show route for <prefix> all`.
// Useful for asserting ECMP fan-out across multiple LB Pods. Returns an empty
// slice if the exact prefix is not present (e.g. the query matched only the
// default fallback route).
func VPNGatewayRouteNextHops(prefix string) ([]string, error) {
	out, err := runBirdcOnVPNGateway(fmt.Sprintf("show route for %s all", prefix))
	if err != nil {
		return nil, err
	}
	block, found := exactPrefixBlock(out, prefix)
	if !found {
		return nil, nil
	}
	var nextHops []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		// Next-hop lines look like: "via 169.254.10.1 on vlan1"
		if !strings.HasPrefix(line, "via ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			nextHops = append(nextHops, fields[1])
		}
	}
	return nextHops, nil
}

// exactPrefixBlock extracts the portion of `birdc show route ... all` output
// that describes the route for the exact prefix given. A route entry begins with
// a line whose first field is the network prefix (e.g. "10.0.0.1/32 unicast ..."),
// followed by indented attribute/next-hop lines and possibly additional
// continuation lines (further paths) whose network column is empty (they start
// with whitespace).
//
// It returns the matching block and whether the exact prefix was found. Lines for
// other prefixes (such as the 0.0.0.0/0 default fallback) are excluded.
func exactPrefixBlock(out, prefix string) (string, bool) {
	var block []string
	capturing := false
	for _, line := range strings.Split(out, "\n") {
		// A new route entry starts on a non-indented line whose first field is
		// a network prefix (contains "/"). Continuation paths for the same
		// prefix start with whitespace (empty network column) and must not
		// reset capture state.
		isNewEntry := len(line) > 0 && line[0] != ' ' && line[0] != '\t'
		if isNewEntry {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.Contains(fields[0], "/") {
				// Starting a new route entry: capture only if it's ours.
				capturing = fields[0] == prefix
			}
			// Non-prefix header lines (e.g. "Table master4:",
			// "BIRD ... ready.") don't change capture state.
		}
		if capturing {
			block = append(block, line)
		}
	}
	return strings.Join(block, "\n"), len(block) > 0
}
