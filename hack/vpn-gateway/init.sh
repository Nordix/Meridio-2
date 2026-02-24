#!/bin/bash

sysctl -w net.ipv4.conf.all.forwarding=1
sysctl -w net.ipv6.conf.all.forwarding=1
sysctl -w net.ipv6.conf.all.accept_dad=0
sysctl -w net.ipv6.conf.all.disable_ipv6=0

ip link add link eth0 name vlan1 type vlan id $1
ip link set vlan1 up
ip addr add 169.254.100.150/24 dev vlan1
ip addr add fd00:100::150/64 dev vlan1

echo "VPN Gateway ready on VLAN $1"

/usr/sbin/bird -d -c /etc/bird/bird.conf
