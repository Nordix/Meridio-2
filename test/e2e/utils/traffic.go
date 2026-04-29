//go:build e2e
// +build e2e

package utils

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

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
		"docker exec vpn-gateway /opt/ctraffic %s -address %s -nconn %d -timeout 10s -stats all",
		protoFlag, addr, nconn,
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
	cmdStr := fmt.Sprintf("docker exec vpn-gateway %s -c 3 -W 2 %s", pingCmd, vip)
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
	cmdStr := fmt.Sprintf("docker exec vpn-gateway %s %s -c 3 -W 5 %s", pingCmd, sizeFlag, vip)
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
	flushCmd := fmt.Sprintf("docker exec vpn-gateway ip route flush cache %s", vip)
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
		"docker exec vpn-gateway timeout 10 tcpdump -c 1 -nn -l '%s' 2>/dev/null",
		tcpdumpFilter,
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
	pingCmdStr := fmt.Sprintf("docker exec vpn-gateway %s %s -c 3 -W 3 %s", pingCmd, sizeFlag, vip)
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
