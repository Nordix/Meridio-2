/*
Copyright (c) 2024 OpenInfra Foundation Europe

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

import "time"

const (
	ownfw          = 0
	nfqlbCmd       = "nfqlb"
	tableName      = "table-nfqlb"
	chainName      = "nfqlb"
	localChainName = "nfqlb-local"
	ipv4VIPSetName = "ipv4-vips"
	ipv6VIPSetName = "ipv6-vips"
	maxPortRange   = "0-65535"

	defaultQueue          = "0:3"
	defaultQLength        = 1024
	defaultStartingOffset = 5000
	defaultHealInterval   = 10 * time.Second
	defaultMaxTargets     = 100

	maglevMMultiplier = 100
)
