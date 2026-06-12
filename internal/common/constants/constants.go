// Package constants defines shared constants used across controllers.
package constants

const (
	// ReadinessGateIPv4 is the Pod readiness gate condition type for IPv4 external connectivity.
	ReadinessGateIPv4 = "meridio-2.nordix.org/ipv4-connectivity"
	// ReadinessGateIPv6 is the Pod readiness gate condition type for IPv6 external connectivity.
	ReadinessGateIPv6 = "meridio-2.nordix.org/ipv6-connectivity"
)
