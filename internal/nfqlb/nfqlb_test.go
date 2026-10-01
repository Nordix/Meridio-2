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
	"os"
	"path/filepath"

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

// Regression coverage for the ENP2 nVIP SLLBR incident: a clean (status 0)
// exit of the nfqlb daemon must be treated the same as a crash. The caller
// (cmd/stateless-load-balancer/cmd/run.go) tears down the manager process
// whenever Start() returns — regardless of error — so that a dead dataplane
// never continues reporting Ready. Recovery relies on that crash-and-restart:
// once the process exits, nothing in this package needs to independently
// detect staleness in already-tracked Instances, because no further calls
// into this package happen until a fresh process starts clean.
var _ = Describe("Start", func() {
	It("surfaces a clean daemon exit as an error so the caller can crash the process", func() {
		ctx := context.Background()

		// /bin/true stands in for a `nfqlb flowlb` that exits with status 0.
		lb, err := New(WithNFQLBPath("/bin/true"))
		Expect(err).ToNot(HaveOccurred())

		err = lb.Start(ctx)
		Expect(err).To(HaveOccurred(),
			"a terminated nfqlb process (even clean exit) must be reported as a failure")
	})

	It("does not report an error when ctx is cancelled (expected shutdown)", func() {
		ctx, cancel := context.WithCancel(context.Background())

		// nfqlbPath is invoked as `<path> flowlb --promiscuous_ping ...`; a
		// wrapper script lets us ignore those fixed args and just block until
		// killed, standing in for a healthy nfqlb that runs until shutdown.
		scriptPath := filepath.Join(GinkgoT().TempDir(), "block.sh")
		Expect(os.WriteFile(scriptPath, []byte("#!/bin/sh\nexec sleep 100\n"), 0o755)).To(Succeed())

		lb, err := New(WithNFQLBPath(scriptPath))
		Expect(err).ToNot(HaveOccurred())

		done := make(chan error, 1)
		go func() { done <- lb.Start(ctx) }()

		Eventually(lb.running.Load).Should(BeTrue(), "process should be running before we cancel")
		cancel()

		var startErr error
		Eventually(done).Should(Receive(&startErr))
		Expect(startErr).ToNot(HaveOccurred(),
			"a deliberate shutdown via context cancellation is not a failure")
	})
})

// Regression coverage: maxEndpoints is accepted by the API with no upper
// bound today (minimum=1, no maximum), so a single DistributionGroup can
// exhaust the fwmark/offset space and permanently wedge AddInstance for
// that DG. The fix should reject this earlier (CRD maximum and/or webhook
// validation against the LB's remaining offset budget) rather than let the
// reconciler retry an unwinnable request forever. This spec currently FAILS
// because AddInstance has no upper validation on maxTargets.
var _ = Describe("AddInstance", func() {
	It("rejects a maxEndpoints value that cannot fit instead of looping forever", func() {
		ctx := context.Background()

		lb, err := New(WithNFQLBPath("/bin/true"))
		Expect(err).ToNot(HaveOccurred())
		lb.running.Store(true)

		_, err = lb.AddInstance(ctx, "dg-too-big", WithMaxTargets(99000))
		Expect(err).To(HaveOccurred())
		Expect(err).ToNot(MatchError(errIdentifierOffset),
			"an oversized maxEndpoints should be rejected with a clear, "+
				"actionable validation error — not the generic offset-exhaustion "+
				"error that also fires for legitimate capacity exhaustion across "+
				"many DGs, and should ideally be caught before reaching this layer")
	})

	// Regression coverage: DeleteInstance currently removes the instance
	// from the tracking map before target/route cleanup is attempted and
	// before confirming `nfqlb delete --shm=` succeeded. If that command
	// fails, routes and flows for the instance's fwmark range are never
	// cleaned up, yet the offset is immediately eligible for reuse by the
	// next AddInstance call. The next DG then inherits a fwmark range with
	// live leftover routes pointing at the previous DG's target IPs. This
	// spec currently FAILS: DeleteInstance returns an error, but the offset
	// is already reused by the time it does.
	It("does not recycle a freed offset onto routes it failed to clean up", func() {
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
})
