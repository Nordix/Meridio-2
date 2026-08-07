/*
Copyright (c) 2024-2026 OpenInfra Foundation Europe

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

package nfqlb

const (
	ownfw             = 0
	nfqlbCmd          = "nfqlb"
	tableName         = "table-nfqlb"
	chainName         = "nfqlb"
	localChainName    = "nfqlb-local"
	ipv4VIPSetName    = "ipv4-vips"
	ipv6VIPSetName    = "ipv6-vips"
	maxPortRange      = "0-65535"
	maglevMMultiplier = 100
	defaultQLength    = 1024
	defaultMaxTargets = 100
	rulePriority      = 32000
)

// DefaultFwmarkBase is the default base fwmark value.
// Layout: +0=nolb, +1=notargets, +2..=NFQLB instance offsets.
const DefaultFwmarkBase = 5000

// MaxOffset is the upper bound for fwmark/routing table IDs to prevent
// unbounded allocation. Supports ~950 DGs with maxTargets=100.
const MaxOffset = 100000

// DefaultQueue is the default NFQUEUE range used by NFQLB.
const DefaultQueue = "0:3"
