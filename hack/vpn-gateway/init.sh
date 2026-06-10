#!/bin/sh

sysctl -w net.ipv4.fib_multipath_hash_policy=1
sysctl -w net.ipv4.conf.all.forwarding=1

# VLAN 100 — separate-appnetwork gw-a1
ip link add link eth0 name vlan1 type vlan id 100
ip link set vlan1 up
ip addr add 169.254.100.150/24 dev vlan1
ip addr add 200.100.0.100/32 dev vlan1

# VLAN 200 — separate-appnetwork gw-a2
ip link add link eth0 name vlan2 type vlan id 200
ip link set vlan2 up
ip addr add 169.254.200.150/24 dev vlan2
ip addr add 200.200.0.100/32 dev vlan2

# VLAN 300 — common-appnetwork gw-b1
ip link add link eth0 name vlan3 type vlan id 300
ip link set vlan3 up
ip addr add 169.254.101.150/24 dev vlan3
ip addr add 200.100.0.101/32 dev vlan3

# VLAN 400 — common-appnetwork gw-b2
ip link add link eth0 name vlan4 type vlan id 400
ip link set vlan4 up
ip addr add 169.254.201.150/24 dev vlan4
ip addr add 200.200.0.101/32 dev vlan4

# VLAN 500 — sctp-multihoming path 1
ip link add link eth0 name vlan5 type vlan id 500
ip link set vlan5 up
ip addr add 169.254.102.150/24 dev vlan5
ip addr add 200.100.0.100/32 dev vlan5

# VLAN 600 — sctp-multihoming path 2
ip link add link eth0 name vlan6 type vlan id 600
ip link set vlan6 up
ip addr add 169.254.202.150/24 dev vlan6
ip addr add 200.100.1.100/32 dev vlan6

# VLAN 700 — low-mtu gw-m1
ip link add link eth0 name vlan7 type vlan id 700
ip link set vlan7 up
ip addr add 169.254.50.150/24 dev vlan7
ip addr add 200.50.0.100/32 dev vlan7

# VLAN 800 — tcp-ao gw-b1
ip link add link eth0 name vlan8 type vlan id 800
ip link set vlan8 up
ip addr add 169.254.103.150/24 dev vlan8

# VLAN 900 — tcp-ao gw-b2
ip link add link eth0 name vlan9 type vlan id 900
ip link set vlan9 up
ip addr add 169.254.203.150/24 dev vlan9

ethtool -K eth0 tx off

echo "VPN Gateway ready on VLAN 100, 200, 300, 400, 500, 600, 700, 800, 900"

/usr/sbin/bird -d -c /etc/bird/bird-gw.conf
