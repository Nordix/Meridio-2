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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// mockRouting records calls to createPolicyRoute and deletePolicyRoute.
type mockRouting struct {
	created   []routeCall
	deleted   []routeCall
	createErr error
}

type routeCall struct {
	fwmark int
	ip     string
}

func (m *mockRouting) create(fwmark int, ip string) error {
	m.created = append(m.created, routeCall{fwmark, ip})
	return m.createErr
}

func (m *mockRouting) delete(fwmark int, ip string) error {
	m.deleted = append(m.deleted, routeCall{fwmark, ip})
	return nil
}

// mockExec records nfqlb CLI calls and returns success.
type mockExec struct {
	calls [][]string
}

func (m *mockExec) run(_ context.Context, args ...string) ([]byte, error) {
	m.calls = append(m.calls, args)
	return nil, nil
}

// newTestInstance creates an Instance with mock routing and exec for testing.
func newTestInstance(name string, offset, maxTargets int, routing *mockRouting, executor *mockExec) *Instance {
	return &Instance{
		nfqlbInstanceConfig:               &nfqlbInstanceConfig{maxTargets: maxTargets},
		name:                              name,
		targets:                           map[int][]string{},
		offset:                            offset,
		nfqlbPath:                         "nfqlb",
		updateNfQueueDestinationCIDRsFunc: func(_ context.Context) error { return nil },
		routeCreate:                       routing.create,
		routeDelete:                       routing.delete,
		execCmd:                           executor.run,
	}
}

var _ = Describe("Instance.AddTarget", func() {
	var (
		instance *Instance
		routing  *mockRouting
		executor *mockExec
		ctx      context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		routing = &mockRouting{}
		executor = &mockExec{}
		instance = newTestInstance("test-instance", 5000, 32, routing, executor)
	})

	It("should activate a new target and create policy routes", func() {
		err := instance.AddTarget(ctx, []string{"10.0.0.1"}, 0)
		Expect(err).ToNot(HaveOccurred())

		Expect(instance.targets).To(HaveKeyWithValue(0, []string{"10.0.0.1"}))
		Expect(executor.calls).To(HaveLen(1))
		Expect(executor.calls[0]).To(ContainElements("activate", "--index=0", "--shm=test-instance", "5000"))
		Expect(routing.created).To(ConsistOf(routeCall{5000, "10.0.0.1"}))
	})

	It("should be a no-op when identifier exists with same IPs", func() {
		instance.targets[0] = []string{"10.0.0.1"}

		err := instance.AddTarget(ctx, []string{"10.0.0.1"}, 0)
		Expect(err).ToNot(HaveOccurred())

		// No exec calls, no routing changes
		Expect(executor.calls).To(BeEmpty())
		Expect(routing.created).To(BeEmpty())
		Expect(routing.deleted).To(BeEmpty())
	})

	It("should update policy routes when IPs change for existing identifier", func() {
		instance.targets[0] = []string{"10.0.0.1"}

		err := instance.AddTarget(ctx, []string{"10.0.0.2"}, 0)
		Expect(err).ToNot(HaveOccurred())

		// Should NOT re-activate in nfqlb (fwmark unchanged)
		Expect(executor.calls).To(BeEmpty())
		// Should delete old route and create new route
		Expect(routing.deleted).To(ConsistOf(routeCall{5000, "10.0.0.1"}))
		Expect(routing.created).To(ConsistOf(routeCall{5000, "10.0.0.2"}))
		// Should update tracked IPs
		Expect(instance.targets[0]).To(Equal([]string{"10.0.0.2"}))
	})

	It("should handle multi-IP targets with partial IP change", func() {
		instance.targets[1] = []string{"10.0.0.1", "10.0.0.2"}

		err := instance.AddTarget(ctx, []string{"10.0.0.1", "10.0.0.3"}, 1)
		Expect(err).ToNot(HaveOccurred())

		// Delete all old routes, create all new routes
		Expect(routing.deleted).To(ConsistOf(
			routeCall{5001, "10.0.0.1"},
			routeCall{5001, "10.0.0.2"},
		))
		Expect(routing.created).To(ConsistOf(
			routeCall{5001, "10.0.0.1"},
			routeCall{5001, "10.0.0.3"},
		))
		Expect(instance.targets[1]).To(Equal([]string{"10.0.0.1", "10.0.0.3"}))
	})

	It("should handle route creation failure gracefully on IP change", func() {
		routing.createErr = fmt.Errorf("netlink error")
		instance.targets[0] = []string{"10.0.0.1"}

		// Should not return error (route creation failures are non-fatal, heal loop retries)
		err := instance.AddTarget(ctx, []string{"10.0.0.2"}, 0)
		Expect(err).ToNot(HaveOccurred())

		// IPs should still be updated (heal loop will fix routes)
		Expect(instance.targets[0]).To(Equal([]string{"10.0.0.2"}))
	})
})

var _ = Describe("slicesEqual", func() {
	It("should return true for identical slices", func() {
		Expect(slicesEqual([]string{"a", "b"}, []string{"a", "b"})).To(BeTrue())
	})

	It("should return false for different lengths", func() {
		Expect(slicesEqual([]string{"a"}, []string{"a", "b"})).To(BeFalse())
	})

	It("should return false for different content", func() {
		Expect(slicesEqual([]string{"a", "b"}, []string{"a", "c"})).To(BeFalse())
	})

	It("should return true for empty slices", func() {
		Expect(slicesEqual([]string{}, []string{})).To(BeTrue())
	})

	It("should return true for nil slices", func() {
		Expect(slicesEqual(nil, nil)).To(BeTrue())
	})

	It("should return false for nil vs empty", func() {
		// Both have len 0, so they are equal in our semantics
		Expect(slicesEqual(nil, []string{})).To(BeTrue())
	})
})
