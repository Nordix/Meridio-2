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

import (
	"errors"
	"strconv"
	"strings"
)

var errQueueFormat = errors.New("nfqlb queue must be an integer or in format integer:integer")

const queueRange = 2

// getQueue secures gosec: G204: Subprocess launched with a potential tainted input or cmd arguments.
func getQueue(queue string) (start int, end int, err error) {
	nfqueues := strings.Split(queue, ":")
	if len(nfqueues) > queueRange {
		return 0, 0, errQueueFormat
	}

	start, err = strconv.Atoi(nfqueues[0])
	if err != nil {
		return 0, 0, errQueueFormat
	}

	end = start

	if len(nfqueues) == queueRange {
		end, err = strconv.Atoi(nfqueues[1])
		if err != nil {
			return 0, 0, errQueueFormat
		}
	}

	return start, end, nil
}

// anyIPRange returns true if ALL the IP ranges are /0.
//
// Note:
// IPv4 and IPv6 ranges can be mixed in both Flows and nfqlb Flows.
// When specified, nfqlb Flow's srcs/dsts selectors will NOT match IP version
// for whom no IP range is set.
func anyIPRange(ips []string) bool {
	for _, ip := range ips {
		s := strings.Split(ip, "/")
		if len(s) == 1 { // should never not happen, nfqlb expects subnet mask...
			return false
		}

		mask, err := strconv.Atoi(s[1])
		if err != nil {
			// resort to stating input IP ranges are not 'any' (worst case the flow rule won't get simplified)
			return false
		}

		if mask != 0 {
			// non zero subnet mask i.e. not 'any' range
			return false
		}
	}

	return true
}

// anyPortRange returns true if ANY of the possible input port ranges cover all the possible ports (0-65535).
func anyPortRange(ports []string) bool {
	for _, port := range ports {
		if port == maxPortRange {
			return true
		}
	}

	return false
}
