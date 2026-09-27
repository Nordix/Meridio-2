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
	"strings"

	"github.com/nordix/meridio-2/test/utils"
)

// DataPlaneDiagnostics captures a snapshot of the load-balancer data plane and
// the target endpoints for a namespace, to make an intermittent traffic-loss
// failure (e.g. fresh connections reported lost) actionable after the fact.
//
// It is best-effort and read-only: every command's output is collected into a
// single string; individual failures are recorded inline rather than aborting
// the dump. Call it from a test only when an assertion has already failed (so
// it never affects passing runs), and print the returned string to the run log.
//
// For each gateway it dumps, per DistributionGroup shm, the nfqlb Maglev table
// (`nfqlb show --shm=<dg>`), which reveals how many of the expected endpoints
// are actually programmed into the data plane. It also dumps the ENC Ready
// state for the target selector and the pod->node placement, so control-plane
// "Ready" can be compared against data-plane "programmed/routable" — the exact
// distinction between a slow-but-correct convergence and a genuine black hole.
type DataPlaneDiagnostics struct {
	Namespace string
	// Gateways maps a gateway name to the DistributionGroup shm names backing it
	// (one nfqlb instance per DG), e.g. "gw-bds1" -> {"dg-bds1"}.
	Gateways map[string][]string
	// TargetLabel selects the target endpoint pods, e.g. "app=target-bds".
	TargetLabel string
}

// Collect gathers the diagnostics and returns them as a formatted, log-friendly
// string. It never returns an error: any command failure is embedded in the
// output so the snapshot is as complete as possible.
func (d DataPlaneDiagnostics) Collect() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n===== data-plane diagnostics (ns=%s) =====\n", d.Namespace)

	// ENC Ready state for the targets: how many endpoints the control plane
	// currently considers Ready.
	fmt.Fprintf(&b, "\n--- ENC Ready state for %q ---\n", d.TargetLabel)
	b.WriteString(run("kubectl", "get", "enc", "-n", d.Namespace,
		"-o", "custom-columns=ENC:.metadata.name,READY:.status.conditions[?(@.type=='Ready')].status"))

	// Target pod placement, so eviction/reschedule and per-node spread is visible.
	fmt.Fprintf(&b, "\n--- target pods (%s) ---\n", d.TargetLabel)
	b.WriteString(run("kubectl", "get", "pods", "-n", d.Namespace, "-l", d.TargetLabel,
		"-o", "wide"))

	// nfqlb Maglev table per gateway/DG: how many endpoints are actually
	// programmed into the data plane on each LB pod.
	for gw, shms := range d.Gateways {
		lbPods := GetPodNames(d.Namespace, "gateway.networking.k8s.io/gateway-name="+gw)
		fmt.Fprintf(&b, "\n--- gateway %s LB pods: %v ---\n", gw, lbPods)
		for _, pod := range lbPods {
			for _, shm := range shms {
				fmt.Fprintf(&b, "\n[%s] nfqlb show --shm=%s:\n", pod, shm)
				b.WriteString(run("kubectl", "exec", "-n", d.Namespace, pod,
					"-c", "loadbalancer", "--", "nfqlb", "show", "--shm="+shm))
			}
		}
	}
	fmt.Fprintf(&b, "\n===== end data-plane diagnostics =====\n")
	return b.String()
}

// run executes a kubectl (or other) command and returns its combined output,
// or an inline "<error: ...>" marker on failure. Never panics; suitable for
// best-effort diagnostics gathering.
func run(name string, args ...string) string {
	out, err := utils.Run(exec.Command(name, args...))
	if err != nil {
		return fmt.Sprintf("<error running %s %s: %v>\n%s\n",
			name, strings.Join(args, " "), err, out)
	}
	if strings.TrimSpace(out) == "" {
		return "<no output>\n"
	}
	return out
}
