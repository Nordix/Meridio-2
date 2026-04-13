//go:build e2e
// +build e2e

package utils

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
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
