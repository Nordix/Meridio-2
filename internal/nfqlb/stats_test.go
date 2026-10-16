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

package nfqlb

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("parseActiveTargets", func() {
	It("counts the tokens on the Active line", func() {
		out := []byte(`Shm: dg-a
  Fw: own=0
  Maglev: M=997, N=32
   Lookup: 68 68 68 72 43 61...
   Active: 5044(43) 5062(61) 5069(68) 5073(72)
`)
		n, err := parseActiveTargets(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(4))
	})

	It("returns 0 for an empty Active line", func() {
		out := []byte("Shm: dg-a\n   Active:\n")
		n, err := parseActiveTargets(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(0))
	})

	It("returns 0 for an Active line with trailing spaces only", func() {
		out := []byte("   Active:   \n")
		n, err := parseActiveTargets(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(0))
	})

	It("errors when there is no Active line", func() {
		out := []byte("Shm: dg-a\n  Maglev: M=997, N=32\n")
		_, err := parseActiveTargets(out)
		Expect(err).To(HaveOccurred())
	})
})

// scriptedExec returns a canned output (and optional error) per invocation, recording the args.
// Successive calls return successive entries in outputs; if fewer outputs than calls, the last
// is reused.
type scriptedExec struct {
	outputs [][]byte
	err     error
	calls   [][]string
}

func (s *scriptedExec) run(_ context.Context, args ...string) ([]byte, error) {
	s.calls = append(s.calls, args)
	if s.err != nil {
		return nil, s.err
	}
	idx := len(s.calls) - 1
	if idx >= len(s.outputs) {
		idx = len(s.outputs) - 1
	}
	return s.outputs[idx], nil
}

var _ = Describe("NFQueueLoadBalancer.ActiveTargets", func() {
	var ctx context.Context

	BeforeEach(func() { ctx = context.Background() })

	It("returns the active-target count per instance (DG) name", func() {
		exec := &scriptedExec{outputs: [][]byte{
			[]byte("Shm: dg-a\n   Active: 5002(0) 5003(1) 5004(2)\n"),
			[]byte("Shm: dg-b\n   Active: 5102(0)\n"),
		}}
		nfqlb := &NFQueueLoadBalancer{
			nfqlbConfig: newNFQLBConfig(),
			instances: map[string]*Instance{
				"dg-a": {name: "dg-a", nfqlbPath: "nfqlb", execCmd: exec.run},
				"dg-b": {name: "dg-b", nfqlbPath: "nfqlb", execCmd: exec.run},
			},
		}

		got, err := nfqlb.ActiveTargets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(2))
		// Both instances share the scriptedExec; map iteration order is nondeterministic, so
		// assert the multiset of counts rather than per-key values.
		counts := []int{got["dg-a"], got["dg-b"]}
		Expect(counts).To(ConsistOf(3, 1))
	})

	It("returns an empty map when there are no instances", func() {
		nfqlb := &NFQueueLoadBalancer{nfqlbConfig: newNFQLBConfig(), instances: map[string]*Instance{}}
		got, err := nfqlb.ActiveTargets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("skips an instance (no error) when its nfqlb show output is unparseable", func() {
		exec := &scriptedExec{outputs: [][]byte{
			[]byte("Shm: dg-a\n  Maglev: M=997, N=32\n"), // no "Active:" line
		}}
		nfqlb := &NFQueueLoadBalancer{
			nfqlbConfig: newNFQLBConfig(),
			instances: map[string]*Instance{
				"dg-a": {name: "dg-a", nfqlbPath: "nfqlb", execCmd: exec.run},
			},
		}
		got, err := nfqlb.ActiveTargets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).NotTo(HaveKey("dg-a")) // unparseable instance skipped, not errored
	})

	It("skips an instance (no error) when its nfqlb show fails", func() {
		exec := &scriptedExec{err: fmt.Errorf("nfqlb show failed"), outputs: [][]byte{nil}}
		nfqlb := &NFQueueLoadBalancer{
			nfqlbConfig: newNFQLBConfig(),
			instances: map[string]*Instance{
				"dg-a": {name: "dg-a", nfqlbPath: "nfqlb", execCmd: exec.run},
			},
		}
		got, err := nfqlb.ActiveTargets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty()) // the only instance failed show → skipped, empty result
	})

	It("one failing instance does not sink the others (best-effort per instance)", func() {
		// Per-instance execCmd closures (deterministic regardless of map iteration order):
		// dg-ok returns a valid Active line; dg-bad fails `show`.
		okExec := func(_ context.Context, _ ...string) ([]byte, error) {
			return []byte("Shm: dg-ok\n   Active: 5002(0) 5003(1)\n"), nil
		}
		badExec := func(_ context.Context, _ ...string) ([]byte, error) {
			return nil, fmt.Errorf("nfqlb show failed")
		}
		nfqlb := &NFQueueLoadBalancer{
			nfqlbConfig: newNFQLBConfig(),
			instances: map[string]*Instance{
				"dg-ok":  {name: "dg-ok", nfqlbPath: "nfqlb", execCmd: okExec},
				"dg-bad": {name: "dg-bad", nfqlbPath: "nfqlb", execCmd: badExec},
			},
		}
		got, err := nfqlb.ActiveTargets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveKeyWithValue("dg-ok", 2)) // healthy instance still counted
		Expect(got).NotTo(HaveKey("dg-bad"))         // failing instance skipped
	})
})
