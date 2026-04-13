#!/bin/sh

sysctl -w net.ipv4.fib_multipath_hash_policy=1
sysctl -w net.ipv4.conf.all.forwarding=1

# VLAN 100 — ns-a gw-a1
ip link add link eth0 name vlan1 type vlan id 100
ip link set vlan1 up
ip addr add 169.254.100.150/24 dev vlan1
ip addr add 200.100.0.100/32 dev vlan1

# VLAN 200 — ns-a gw-a2
ip link add link eth0 name vlan2 type vlan id 200
ip link set vlan2 up
ip addr add 169.254.200.150/24 dev vlan2
ip addr add 200.200.0.100/32 dev vlan2

# VLAN 300 — ns-b gw-b1
ip link add link eth0 name vlan3 type vlan id 300
ip link set vlan3 up
ip addr add 169.254.101.150/24 dev vlan3
ip addr add 200.100.0.101/32 dev vlan3

# VLAN 400 — ns-b gw-b2
ip link add link eth0 name vlan4 type vlan id 400
ip link set vlan4 up
ip addr add 169.254.201.150/24 dev vlan4
ip addr add 200.200.0.101/32 dev vlan4

ethtool -K eth0 tx off

echo "VPN Gateway ready on VLAN 100, 200, 300, 400"

/usr/sbin/bird -d -c /etc/bird/bird-gw.conf
