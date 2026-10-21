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

package loadbalancer

import (
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nordix/meridio-2/internal/nfqueueproc"
)

// fakeNfqueue is an NfqueueReader test double.
type fakeNfqueue struct {
	stats nfqueueproc.Stats
	err   error
}

func (f *fakeNfqueue) Read() (nfqueueproc.Stats, error) {
	return f.stats, f.err
}

func newTestNfqueueCollector(r NfqueueReader, errs errorRecorder) *NfqueueCollector {
	return NewNfqueueCollector(r, testGateway, testPrefix, errs)
}

func TestNfqueueCollector_Values(t *testing.T) {
	c := newTestNfqueueCollector(
		&fakeNfqueue{stats: nfqueueproc.Stats{Items: 8, QueueDropped: 2, UserDropped: 5}},
		newFakeCollectorErrors(),
	)

	expected := `
# HELP meridio_2_lb_nfqueue_items Packets currently waiting in the LB's netfilter queues for a userspace verdict (instantaneous backlog, summed over the configured queue range). A gauge: it is normally 0 and rises only when userspace cannot keep up with the packet rate.
# TYPE meridio_2_lb_nfqueue_items gauge
meridio_2_lb_nfqueue_items{gateway="gw-a"} 8
# HELP meridio_2_lb_nfqueue_dropped_total Packets dropped by the LB's netfilter queues, summed over the configured queue range, by reason. reason=queue: the queue was full when a packet arrived (a saturation signal — pair it with _lb_nfqueue_items and consider raising --qlength). reason=user: the kernel could not hand a packet to userspace (a delivery error, independent of queue fullness). Cumulative since the queues were bound.
# TYPE meridio_2_lb_nfqueue_dropped_total counter
meridio_2_lb_nfqueue_dropped_total{gateway="gw-a",reason="queue"} 2
meridio_2_lb_nfqueue_dropped_total{gateway="gw-a",reason="user"} 5
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_nfqueue_items", "meridio_2_lb_nfqueue_dropped_total"))
}

// The idle case (0 backlog, 0 drops) still emits all series — a 0 gauge and 0 counters are
// meaningful (and let consumers distinguish "healthy, nothing queued" from "no data").
func TestNfqueueCollector_Zero_StillEmits(t *testing.T) {
	c := newTestNfqueueCollector(
		&fakeNfqueue{stats: nfqueueproc.Stats{Items: 0, QueueDropped: 0, UserDropped: 0}},
		newFakeCollectorErrors(),
	)

	expected := `
# HELP meridio_2_lb_nfqueue_items Packets currently waiting in the LB's netfilter queues for a userspace verdict (instantaneous backlog, summed over the configured queue range). A gauge: it is normally 0 and rises only when userspace cannot keep up with the packet rate.
# TYPE meridio_2_lb_nfqueue_items gauge
meridio_2_lb_nfqueue_items{gateway="gw-a"} 0
# HELP meridio_2_lb_nfqueue_dropped_total Packets dropped by the LB's netfilter queues, summed over the configured queue range, by reason. reason=queue: the queue was full when a packet arrived (a saturation signal — pair it with _lb_nfqueue_items and consider raising --qlength). reason=user: the kernel could not hand a packet to userspace (a delivery error, independent of queue fullness). Cumulative since the queues were bound.
# TYPE meridio_2_lb_nfqueue_dropped_total counter
meridio_2_lb_nfqueue_dropped_total{gateway="gw-a",reason="queue"} 0
meridio_2_lb_nfqueue_dropped_total{gateway="gw-a",reason="user"} 0
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_nfqueue_items", "meridio_2_lb_nfqueue_dropped_total"))
}

// On a read error the collector SKIPS both series and counts the failure (no invalid metric).
func TestNfqueueCollector_Error(t *testing.T) {
	errs := newFakeCollectorErrors()
	c := newTestNfqueueCollector(&fakeNfqueue{err: errors.New("proc read failed")}, errs)

	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(""),
		"meridio_2_lb_nfqueue_items", "meridio_2_lb_nfqueue_dropped_total"))
	require.Equal(t, 1, errs.count(CollectorNfqueue))
}
