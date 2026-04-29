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

package e2e

import (
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestE2E runs the e2e test suite to validate the solution in an isolated environment.
// Prerequisites:
// - Kind cluster with Multus, Whereabouts, cert-manager, Gateway API CRDs
// - VPN gateway container running
// - Suites deployed via Makefile (make deploy-<suite>)
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting meridio-2 e2e test suite\n")
	RunSpecs(t, "e2e suite")
}
