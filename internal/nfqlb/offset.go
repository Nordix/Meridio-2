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
	"math"
)

var errIdentifierOffset = errors.New("unable to generate identifier offset")

func getOffset(startingOffset int, services map[string]*Service, maxTarget int) (int, error) {
	offset := startingOffset

search:
	for {
		if offset >= (math.MaxInt - maxTarget + 1) {
			return 0, errIdentifierOffset
		}

		for _, service := range services {
			serviceStart := service.offset
			serviceEnd := serviceStart + service.maxTargets - 1
			currentSearchStart := offset
			currentSearchEnd := offset + maxTarget - 1

			if currentSearchStart <= serviceEnd && currentSearchEnd >= serviceStart {
				offset = serviceStart + service.maxTargets

				continue search
			}
		}

		break
	}

	return offset, nil
}
