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
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nordix/meridio-2/internal/nfqueueproc"
)

// NfqueueReader is the subset the NfqueueCollector reads at scrape time: the reduced per-netns
// netfilter-queue totals (current backlog and cumulative drops) for the LB's configured queue
// range. internal/nfqueueproc.Reader implements it; a fake is used in tests.
type NfqueueReader interface {
	Read() (nfqueueproc.Stats, error)
}

// NfqueueCollector is a prometheus.Collector exposing the netfilter-queue backlog and drop totals
// for the single Gateway this LB Pod serves, read lazily at scrape time from
// /proc/net/netfilter/nfnetlink_queue.
//
//   - <prefix>_lb_nfqueue_items        (gauge;   gateway) — packets currently queued for a verdict
//   - <prefix>_lb_nfqueue_dropped_total (counter; gateway) — queue-full + verdict-delivery drops
//
// Per external-state safety (#236) Collect serializes with a mutex; on a read failure it skips
// both series and increments the collector-errors counter (option-3), rather than emitting an
// invalid metric which would fail the whole scrape under controller-runtime's HTTPErrorOnError.
type NfqueueCollector struct {
	reader      NfqueueReader
	gatewayName string
	errs        errorRecorder

	mu sync.Mutex

	itemsDesc   *prometheus.Desc
	droppedDesc *prometheus.Desc
}

// NewNfqueueCollector creates an NfqueueCollector. prefix must already be validated (see
// internal/common/metrics.ValidatePrefix). gatewayName is the bare Gateway name (constant
// "gateway" label). errs records read failures (nil-safe).
func NewNfqueueCollector(reader NfqueueReader, gatewayName, prefix string, errs errorRecorder) *NfqueueCollector {
	return &NfqueueCollector{
		reader:      reader,
		gatewayName: gatewayName,
		errs:        errs,
		itemsDesc: prometheus.NewDesc(
			prefix+"_lb_nfqueue_items",
			"Packets currently waiting in the LB's netfilter queues for a userspace verdict "+
				"(instantaneous backlog, summed over the configured queue range). A gauge: it is "+
				"normally 0 and rises only when userspace cannot keep up with the packet rate.",
			[]string{"gateway"}, nil,
		),
		droppedDesc: prometheus.NewDesc(
			prefix+"_lb_nfqueue_dropped_total",
			"Packets dropped by the LB's netfilter queues, summed over the configured queue range. "+
				"Includes both kernel queue-full drops (the queue was full when a packet arrived) "+
				"and verdict-delivery drops (the kernel could not hand a packet to userspace). "+
				"Cumulative since the queues were bound.",
			[]string{"gateway"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *NfqueueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.itemsDesc
	ch <- c.droppedDesc
}

// Collect implements prometheus.Collector. Reads the proc file fresh on every scrape, serialized
// against concurrent scrapes by mu (the file read itself needs no lock; mu only prevents two
// scrapes from emitting at once). On a read failure it skips both series and counts the failure
// (option-3).
func (c *NfqueueCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stats, err := c.reader.Read()
	if err != nil {
		c.errs.Inc(CollectorNfqueue)
		return
	}
	ch <- prometheus.MustNewConstMetric(
		c.itemsDesc, prometheus.GaugeValue, float64(stats.Items), c.gatewayName,
	)
	ch <- prometheus.MustNewConstMetric(
		c.droppedDesc, prometheus.CounterValue, float64(stats.Dropped), c.gatewayName,
	)
}
