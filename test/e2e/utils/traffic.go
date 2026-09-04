//go:build e2e
// +build e2e

package utils

import (
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

// VPNGatewayBGPEstablished checks, from the DCGW/VPN gateway side, whether a BGP
// session with an LB router is Established. It inspects `birdc show protocols`.
//
// protoMatch is a substring matched against the protocol Name column. On the
// gateway, peers are created dynamically from a `dynamic name "GW4_A1_"` prefix,
// so the actual protocol names look like "GW4_A1_1", "GW4_A1_2", etc. Passing
// "GW4_A1_" matches all of them; at least one must be Established for this to
// return true.
func VPNGatewayBGPEstablished(protoMatch string) (bool, error) {
	out, err := runBirdcOnVPNGateway("show protocols")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// Columns: Name Proto Table State Since Info...
		if len(fields) < 6 {
			continue
		}
		if fields[1] != "BGP" || !strings.Contains(fields[0], protoMatch) {
			continue
		}
		if strings.Contains(line, "Established") {
			return true, nil
		}
	}
	return false, nil
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
	block, found := exactPrefixBlock(out, prefix)
	if !found {
		return false, nil
	}
	// BIRD 3.x prints "source: BGP" for BGP-learned routes in `all` mode.
	return strings.Contains(block, "source: BGP"), nil
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
