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
	"fmt"
	"net"
	"syscall"

	"github.com/google/nftables"
)

const (
	ipv4 = syscall.AF_INET
	ipv6 = syscall.AF_INET6

	setElementRangeLength = 2
)

var errWrongIPFamily = errors.New("wrong ip family")

func updateSet(set *nftables.Set, currentElements [][]nftables.SetElement, newElements [][]nftables.SetElement) error {
	var errFinal error

	conn := &nftables.Conn{}
	newElementsMap := map[string][]nftables.SetElement{}

	for _, newElement := range newElements {
		newElementsMap[setElementToString(newElement)] = newElement
	}

	// to remove
	for _, currentElement := range currentElements {
		setElementString := setElementToString(currentElement)

		_, exists := newElementsMap[setElementString]
		if exists {
			delete(newElementsMap, setElementString)

			continue
		}

		err := conn.SetDeleteElements(set, currentElement)
		if err != nil {
			errFinal = fmt.Errorf("updateSet SetDeleteElements: %w; %w", err, errFinal)
		}
	}

	// to add
	for _, newElement := range newElementsMap {
		err := conn.SetAddElements(set, newElement)
		if err != nil {
			errFinal = fmt.Errorf("updateSet SetAddElements: %w; %w", err, errFinal)
		}
	}

	err := conn.Flush()
	if err != nil {
		errFinal = fmt.Errorf("updateSet flush: %w; %w", err, errFinal)
	}

	return errFinal
}

func getCurrentElements(set *nftables.Set) ([][]nftables.SetElement, error) {
	conn := &nftables.Conn{}

	elements, err := conn.GetSetElements(set)
	if err != nil {
		return nil, fmt.Errorf("failed to GetSetElements: %w", err)
	}

	res := [][]nftables.SetElement{}

	var previousSetElement *nftables.SetElement

	for _, element := range elements {
		if element.IntervalEnd {
			currentElement := element
			previousSetElement = &currentElement
		} else {
			if previousSetElement != nil && previousSetElement.IntervalEnd {
				res = append(res, []nftables.SetElement{
					element,
					*previousSetElement,
				})
			}

			previousSetElement = nil
		}
	}

	return res, nil
}

func getIPv4AndIPv6(cidrList []string) ([]*net.IPNet, []*net.IPNet) {
	ipv4 := []*net.IPNet{}
	ipv6 := []*net.IPNet{}
	usedCidrs := map[string]struct{}{}

	for _, cidr := range cidrList {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}

		if cidrLength, ipLength := ipNet.Mask.Size(); cidrLength != ipLength { // Ipv4 must be /32 and ipv6 /128
			continue
		}

		// remove duplicates
		_, exists := usedCidrs[ipNet.String()]
		if exists {
			continue
		}

		usedCidrs[ipNet.String()] = struct{}{}

		if ipNet.IP.To4() == nil {
			ipv6 = append(ipv6, ipNet)

			continue
		}

		ipv4 = append(ipv4, ipNet)
	}

	return ipv4, ipv6
}

func createSets(table *nftables.Table, family int) (*nftables.Set, error) {
	conn := &nftables.Conn{}

	set, err := getSet(table, family)
	if err != nil {
		return nil, err
	}

	err = conn.AddSet(set, []nftables.SetElement{})
	if err != nil {
		return nil, fmt.Errorf("failed to AddSet: %w", err)
	}

	err = conn.Flush()
	if err != nil {
		return nil, fmt.Errorf("failed to flush (createSets): %w", err)
	}

	return set, nil
}

func getSet(table *nftables.Table, family int) (*nftables.Set, error) {
	if family != ipv4 && family != ipv6 {
		return nil, errWrongIPFamily
	}

	return &nftables.Set{
		Table: table,
		Name: func() string {
			switch family {
			case ipv4:
				return ipv4VIPSetName

			case ipv6:
				return ipv6VIPSetName
			}

			return ""
		}(),
		Interval: true,
		KeyType: func() nftables.SetDatatype {
			switch family {
			case ipv4:
				return nftables.TypeIPAddr

			case ipv6:
				return nftables.TypeIP6Addr
			}

			return nftables.TypeInvalid
		}(),
	}, nil
}

func ipNetsToSetElements(ipNets []*net.IPNet) [][]nftables.SetElement {
	elements := [][]nftables.SetElement{}
	for _, ipNet := range ipNets {
		elements = append(elements, ipNetToSetElement(ipNet))
	}

	return elements
}

func ipNetToSetElement(ipNet *net.IPNet) []nftables.SetElement {
	start := ipNet.IP
	end := nextIP(broadcastFromIPNet(ipNet))
	startV4 := start.To4()
	endV4 := end.To4()

	// Required for the set element to be added correctly to the nftables set
	if startV4 != nil && endV4 != nil {
		start = startV4
		end = endV4
	}

	return []nftables.SetElement{
		{
			Key:         start,
			IntervalEnd: false,
		},
		{
			Key:         end,
			IntervalEnd: true,
		},
	}
}

func setElementToString(setElement []nftables.SetElement) string {
	if len(setElement) != setElementRangeLength {
		return ""
	}

	var ipStart net.IP

	var ipEnd net.IP

	ipStart = setElement[0].Key
	ipEnd = setElement[1].Key

	return fmt.Sprintf("%v-%v", ipStart, ipEnd)
}

func broadcastFromIPNet(ipNet *net.IPNet) net.IP {
	broadcast := make([]byte, len(ipNet.IP))
	copy(broadcast, ipNet.IP)
	for i := range ipNet.IP {
		broadcast[i] = ipNet.IP[i] | ^ipNet.Mask[i]
	}
	return broadcast
}

func nextIP(ip net.IP) net.IP {
	next := make([]byte, len(ip))
	copy(next, ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}
