//go:build e2e
// +build e2e

/*
Copyright (c) 2026 OpenInfra Foundation Europe. All rights reserved.

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

package utils

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nordix/meridio-2/test/utils"
)

// GetPodNames returns the names of Running Pods matching label in namespace.
// Returns nil (not an error) if none are found or the query fails, since
// callers typically want to assert on the result themselves.
func GetPodNames(namespace, label string) []string {
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", label, "--field-selector=status.phase=Running",
		"-o", "jsonpath={.items[*].metadata.name}")
	out, err := utils.Run(cmd)
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(out))
}

// GetPodName returns the first Running Pod name matching label in namespace,
// or "" if none found.
func GetPodName(namespace, label string) string {
	names := GetPodNames(namespace, label)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// IsPodReady reports whether the named Pod has condition Ready == True.
func IsPodReady(namespace, podName string) bool {
	cmd := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
		"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
	out, err := utils.Run(cmd)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "True"
}

// GetContainerRestarts returns the restart count of containerName inside
// podName, or -1 if the Pod/container cannot be found or the count cannot be
// parsed.
func GetContainerRestarts(namespace, podName, containerName string) int {
	cmd := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.status.containerStatuses[?(@.name=='%s')].restartCount}", containerName))
	out, err := utils.Run(cmd)
	if err != nil {
		return -1
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return -1
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return -1
	}
	return n
}
