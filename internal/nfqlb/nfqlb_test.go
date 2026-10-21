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

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("parseFlows", func() {
	It("should parse valid JSON flow list", func() {
		input := `[{
			"Name": "flow-1",
			"user_ref": "svc-a",
			"matches_count": 5,
			"srcs": ["10.0.0.0/8"],
			"dests": ["20.0.0.1/32"],
			"sports": ["1024-65535"],
			"dports": ["80"],
			"protocols": ["TCP"],
			"priority": 100,
			"match": ["0x06/0xff+9"]
		}]`
		flows, err := parseFlows(input)
		Expect(err).ToNot(HaveOccurred())
		Expect(flows).To(HaveLen(1))
		Expect(flows[0].GetName()).To(Equal("flow-1"))
		Expect(flows[0].ServerName).To(Equal("svc-a"))
		Expect(flows[0].GetDestinationCIDRs()).To(ConsistOf("20.0.0.1/32"))
		Expect(flows[0].GetSourceCIDRs()).To(ConsistOf("10.0.0.0/8"))
		Expect(flows[0].GetProtocols()).To(ConsistOf("TCP"))
		Expect(flows[0].GetPriority()).To(Equal(int32(100)))
		Expect(flows[0].GetDestinationPortRanges()).To(ConsistOf("80"))
		Expect(flows[0].GetSourcePortRanges()).To(ConsistOf("1024-65535"))
		Expect(flows[0].GetByteMatches()).To(ConsistOf("0x06/0xff+9"))
	})

	It("should parse empty list", func() {
		flows, err := parseFlows("[]")
		Expect(err).ToNot(HaveOccurred())
		Expect(flows).To(BeEmpty())
	})

	It("should parse multiple flows", func() {
		input := `[{"Name":"f1"},{"Name":"f2"}]`
		flows, err := parseFlows(input)
		Expect(err).ToNot(HaveOccurred())
		Expect(flows).To(HaveLen(2))
		Expect(flows[0].GetName()).To(Equal("f1"))
		Expect(flows[1].GetName()).To(Equal("f2"))
	})

	It("should return error for invalid JSON", func() {
		_, err := parseFlows("not json")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("nfqlbInstanceConfig", func() {
	It("should calculate M as maxTargets * 100", func() {
		cfg := &nfqlbInstanceConfig{maxTargets: 32}
		Expect(cfg.getM()).To(Equal(3200))
	})

	It("should use default maxTargets", func() {
		cfg := newNFQLBInstanceConfig()
		Expect(cfg.maxTargets).To(Equal(defaultMaxTargets))
		Expect(cfg.getM()).To(Equal(defaultMaxTargets * maglevMMultiplier))
	})
})

var _ = Describe("Options", func() {
	It("should apply WithQueue", func() {
		cfg := newNFQLBConfig()
		WithQueue("1:4")(cfg)
		Expect(cfg.queue).To(Equal("1:4"))
	})

	It("should apply WithQLength", func() {
		cfg := newNFQLBConfig()
		WithQLength(2048)(cfg)
		Expect(cfg.qlength).To(Equal(uint(2048)))
	})

	It("should apply WithFwmarkBase", func() {
		cfg := newNFQLBConfig()
		WithFwmarkBase(10000)(cfg)
		Expect(cfg.fwmarkBase).To(Equal(10000))
	})

	It("should apply WithNFQLBPath", func() {
		cfg := newNFQLBConfig()
		WithNFQLBPath("/usr/bin/nfqlb")(cfg)
		Expect(cfg.nfqlbPath).To(Equal("/usr/bin/nfqlb"))
	})

	It("should apply WithMaxTargets", func() {
		cfg := newNFQLBInstanceConfig()
		WithMaxTargets(64)(cfg)
		Expect(cfg.maxTargets).To(Equal(64))
	})
})

var _ = Describe("NoLBFwmark / NoTargetsFwmark", func() {
	It("should derive from fwmarkBase with default config", func() {
		lb, err := New()
		Expect(err).ToNot(HaveOccurred())
		// Contract: NoLBFwmark = fwmarkBase, NoTargetsFwmark = fwmarkBase + 1
		Expect(lb.NoLBFwmark()).To(Equal(DefaultFwmarkBase))
		Expect(lb.NoTargetsFwmark()).To(Equal(DefaultFwmarkBase + 1))
	})

	It("should derive from custom fwmarkBase", func() {
		lb, err := New(WithFwmarkBase(100))
		Expect(err).ToNot(HaveOccurred())
		Expect(lb.NoLBFwmark()).To(Equal(100))
		Expect(lb.NoTargetsFwmark()).To(Equal(101))
	})

	It("should always place NoLBFwmark below NoTargetsFwmark", func() {
		lb, err := New(WithFwmarkBase(1))
		Expect(err).ToNot(HaveOccurred())
		Expect(lb.NoLBFwmark()).To(Equal(1))
		Expect(lb.NoTargetsFwmark()).To(Equal(2))
		Expect(lb.NoLBFwmark()).To(BeNumerically("<", lb.NoTargetsFwmark()))
	})

	It("should never return fwmark 0 (reserved)", func() {
		lb, err := New(WithFwmarkBase(1))
		Expect(err).ToNot(HaveOccurred())
		Expect(lb.NoLBFwmark()).To(BeNumerically(">", 0))
		Expect(lb.NoTargetsFwmark()).To(BeNumerically(">", 0))
	})

	It("should reject fwmarkBase < 1 (would produce fwmark 0)", func() {
		_, err := New(WithFwmarkBase(0))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("fwmarkBase must be >= 1"))
	})

	It("should reject fwmarkBase >= MaxOffset", func() {
		_, err := New(WithFwmarkBase(MaxOffset))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("fwmarkBase must be <="))
	})

	It("should reject fwmarkBase above MaxOffset", func() {
		_, err := New(WithFwmarkBase(MaxOffset + 1))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("fwmarkBase must be <="))
	})

	It("should accept fwmarkBase just below MaxOffset", func() {
		lb, err := New(WithFwmarkBase(MaxOffset - 1))
		Expect(err).ToNot(HaveOccurred())
		Expect(lb.fwmarkBase).To(Equal(MaxOffset - 1))
		Expect(lb.NoLBFwmark()).To(Equal(MaxOffset - 1))
		Expect(lb.NoTargetsFwmark()).To(Equal(MaxOffset))
	})
})

var _ = Describe("New", func() {
	It("should create with defaults", func() {
		lb, err := New()
		Expect(err).ToNot(HaveOccurred())
		Expect(lb).ToNot(BeNil())
		Expect(lb.queue).To(Equal(DefaultQueue))
		Expect(lb.qlength).To(Equal(uint(defaultQLength)))
		Expect(lb.fwmarkBase).To(Equal(DefaultFwmarkBase))
		Expect(lb.NoLBFwmark()).To(Equal(DefaultFwmarkBase))
		Expect(lb.NoTargetsFwmark()).To(Equal(DefaultFwmarkBase + 1))
		Expect(lb.startingOffset()).To(Equal(DefaultFwmarkBase + 2))
		Expect(lb.instances).To(BeEmpty())
	})

	It("should apply options", func() {
		lb, err := New(WithQueue("2:5"), WithQLength(512), WithFwmarkBase(8000))
		Expect(err).ToNot(HaveOccurred())
		Expect(lb.queue).To(Equal("2:5"))
		Expect(lb.qlength).To(Equal(uint(512)))
		Expect(lb.fwmarkBase).To(Equal(8000))
		Expect(lb.NoLBFwmark()).To(Equal(8000))
		Expect(lb.NoTargetsFwmark()).To(Equal(8001))
		Expect(lb.startingOffset()).To(Equal(8002))
	})

	It("should reject invalid queue format", func() {
		_, err := New(WithQueue("bad"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid queue"))
	})
})

var _ = Describe("QueueRange", func() {
	It("returns (N, N) for a single queue", func() {
		lb, err := New(WithQueue("2"))
		Expect(err).ToNot(HaveOccurred())
		start, end := lb.QueueRange()
		Expect(start).To(Equal(2))
		Expect(end).To(Equal(2))
	})

	It("returns (N, M) for a range", func() {
		lb, err := New(WithQueue("0:3"))
		Expect(err).ToNot(HaveOccurred())
		start, end := lb.QueueRange()
		Expect(start).To(Equal(0))
		Expect(end).To(Equal(3))
	})

	It("returns a range that matches nothing on an unparseable queue string", func() {
		// New rejects a bad queue string, so the error path is only reachable with a directly
		// constructed instance. The fallback must be start > end (matches no queue number), not
		// (0,0) which would plausibly count queue 0.
		lb := &NFQueueLoadBalancer{nfqlbConfig: &nfqlbConfig{queue: "bad"}}
		start, end := lb.QueueRange()
		Expect(start > end).To(BeTrue(), "fallback range must match nothing")
	})
})

// DeleteInstance must clean up targets (and their policy routes) and flows
// before unlinking the shm segment, and must only release the instance's
// fwmark offset once all of that succeeded.
//
// `nfqlb deactivate` has to mmap the shm segment, so unlinking it first makes
// every deactivate call fail with "FAILED mapSharedData". deleteTargetNoLock
// returns early on that error, before its policy-route cleanup — which leaked
// an ip rule and routing table per target on every deletion of a
// DistributionGroup that had active targets. Releasing the offset at the same
// time compounds it: the next DistributionGroup can be allocated the same
// fwmark range and inherit live routes pointing at the previous DG's Pod IPs.
var _ = Describe("DeleteInstance", func() {
	It("cleans up policy routes and keeps the offset reserved when the shm unlink fails", func() {
		ctx := context.Background()

		lb, err := New(WithNFQLBPath("/bin/true"))
		Expect(err).ToNot(HaveOccurred())
		lb.running.Store(true)

		inst, err := lb.AddInstance(ctx, "dg-1", WithMaxTargets(4))
		Expect(err).ToNot(HaveOccurred())

		var routesDeleted []int
		inst.routeCreate = func(int, string) error { return nil }
		inst.routeDelete = func(fwmark int, _ string) error {
			routesDeleted = append(routesDeleted, fwmark)
			return nil
		}
		inst.execCmd = func(context.Context, ...string) ([]byte, error) { return nil, nil }
		Expect(inst.AddTarget(ctx, []string{"10.0.0.1"}, 0)).To(Succeed())

		firstOffset := inst.offset

		// `nfqlb delete --shm=` fails (shm busy, EACCES, binary error, ...)
		lb.nfqlbPath = "/bin/false"
		err = lb.DeleteInstance(ctx, "dg-1")
		Expect(err).To(HaveOccurred())

		Expect(routesDeleted).ToNot(BeEmpty(),
			"policy routes/rules must be cleaned up even when the shm unlink fails")

		lb.nfqlbPath = "/bin/true"
		inst2, addErr := lb.AddInstance(ctx, "dg-2", WithMaxTargets(4))
		Expect(addErr).ToNot(HaveOccurred())
		Expect(inst2.offset).ToNot(Equal(firstOffset),
			"a failed deletion must not free its offset for reuse while "+
				"leftover routes for it still exist")
	})

	It("unlinks the shm segment only after targets and flows are cleaned up", func() {
		ctx := context.Background()

		// Record the real call order. The shm unlink and flowList both shell out
		// via exec.CommandContext (neither is routed through the injectable
		// execCmd), so the only way to observe the unlink's position relative to
		// the deactivate is to have the fake binary log its own invocations.
		// execCmd is deliberately NOT injected here so `deactivate` also goes
		// through the script and lands in the same ordered log.
		dir := GinkgoT().TempDir()
		callLog := filepath.Join(dir, "calls")
		scriptPath := filepath.Join(dir, "nfqlb-stub.sh")
		script := fmt.Sprintf("#!/bin/sh\necho \"$1\" >> %s\necho '[]'\n", callLog)
		Expect(os.WriteFile(scriptPath, []byte(script), 0o755)).To(Succeed())

		readCalls := func() []string {
			data, err := os.ReadFile(callLog)
			if err != nil {
				return nil
			}
			var out []string
			for l := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
				if l != "" {
					out = append(out, l)
				}
			}
			return out
		}

		lb, err := New(WithNFQLBPath(scriptPath))
		Expect(err).ToNot(HaveOccurred())
		lb.running.Store(true)

		inst, err := lb.AddInstance(ctx, "dg-order", WithMaxTargets(4))
		Expect(err).ToNot(HaveOccurred())

		inst.routeCreate = func(int, string) error { return nil }
		inst.routeDelete = func(int, string) error {
			f, e := os.OpenFile(callLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			Expect(e).ToNot(HaveOccurred())
			defer f.Close() //nolint:errcheck
			_, e = f.WriteString("route-delete\n")
			Expect(e).ToNot(HaveOccurred())
			return nil
		}
		Expect(inst.AddTarget(ctx, []string{"10.0.0.1"}, 0)).To(Succeed())

		// Drop everything logged during setup (init, activate).
		Expect(os.WriteFile(callLog, nil, 0o600)).To(Succeed())

		Expect(lb.DeleteInstance(ctx, "dg-order")).To(Succeed())

		calls := readCalls()
		Expect(calls).To(ContainElement("deactivate"))
		Expect(calls).To(ContainElement("delete"))
		Expect(calls).To(ContainElement("route-delete"))

		// The unlink ("delete") must come last. On the previous ordering it ran
		// first, which made `nfqlb deactivate` fail with "FAILED mapSharedData"
		// and caused deleteTargetNoLock to return before its route cleanup.
		Expect(indexOf(calls, "delete")).To(BeNumerically(">", indexOf(calls, "deactivate")),
			"the shm segment must be unlinked only after its targets are deactivated; got %v", calls)
		Expect(indexOf(calls, "delete")).To(BeNumerically(">", indexOf(calls, "route-delete")),
			"the shm segment must be unlinked only after policy routes are removed; got %v", calls)

		Expect(lb.instances).ToNot(HaveKey("dg-order"),
			"a fully successful cleanup must release the instance and its offset")
	})
})

// indexOf returns the position of want in calls, or -1.
func indexOf(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}

// maxTargets sizes the contiguous fwmark range an instance occupies, and is
// passed straight through to `nfqlb init --N=`. It had no validation at any
// layer — not in WithMaxTargets, not in newNFQLBInstanceConfig, not in
// AddInstance — so two unrelated failures collapsed into the same
// errIdentifierOffset ("fwmark range exhausted"):
//
//   - an unsatisfiable request, larger than any single instance can ever hold
//   - genuine capacity exhaustion, where a validly-sized instance cannot be
//     placed because existing instances filled the range
//
// Same error text, different causes, different fixes. These specs pin the
// distinction so a "Failed to reconcile NFQLB instance" loop is diagnosable
// from the logs alone.
var _ = Describe("AddInstance maxTargets validation", func() {
	var lb *NFQueueLoadBalancer

	BeforeEach(func() {
		var err error
		lb, err = New(WithNFQLBPath("/bin/true"))
		Expect(err).ToNot(HaveOccurred())
		lb.running.Store(true)
	})

	It("rejects a maxTargets value that can never fit, distinguishably from range exhaustion", func() {
		ctx := context.Background()

		// Budget for one instance: MaxOffset(100000) - startingOffset(5002).
		available := MaxOffset - lb.startingOffset()

		_, err := lb.AddInstance(ctx, "dg-too-big", WithMaxTargets(available+1))
		Expect(err).To(HaveOccurred())
		Expect(err).ToNot(MatchError(errIdentifierOffset),
			"an unsatisfiable maxTargets must not be reported as generic offset "+
				"exhaustion — that error also fires for real capacity exhaustion "+
				"across many DistributionGroups and would be misdiagnosed")
		Expect(err.Error()).To(ContainSubstring("cannot fit"))
		Expect(err.Error()).To(ContainSubstring("dg-too-big"),
			"the error should name the offending instance for log diagnosis")

		// The largest value that does fit must still be accepted, so the bound
		// is off-by-one correct rather than merely conservative.
		_, err = lb.AddInstance(ctx, "dg-at-limit", WithMaxTargets(available))
		Expect(err).ToNot(HaveOccurred())
	})

	It("rejects a non-positive maxTargets", func() {
		ctx := context.Background()

		// Previously accepted outright, which left every later AddTarget failing
		// its `identifier >= s.maxTargets` range check instead of failing here.
		for _, bad := range []int{0, -1} {
			_, err := lb.AddInstance(ctx, "dg-bad", WithMaxTargets(bad))
			Expect(err).To(HaveOccurred(), "maxTargets=%d must be rejected", bad)
			Expect(err.Error()).To(ContainSubstring("must be >= 1"))
		}
	})

	It("still reports genuine fwmark range exhaustion as errIdentifierOffset", func() {
		ctx := context.Background()

		available := MaxOffset - lb.startingOffset()

		// Each instance is individually valid, but together they exceed the
		// range — this must remain errIdentifierOffset, not a validation error.
		_, err := lb.AddInstance(ctx, "dg-fills-range", WithMaxTargets(available))
		Expect(err).ToNot(HaveOccurred())

		_, err = lb.AddInstance(ctx, "dg-no-room-left", WithMaxTargets(1))
		Expect(err).To(MatchError(errIdentifierOffset),
			"a validly-sized instance that cannot be placed is capacity "+
				"exhaustion, not an invalid request")
	})
})
