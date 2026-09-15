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
