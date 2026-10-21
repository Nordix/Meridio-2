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

// Package nfqueueproc reads the kernel's per-netfilter-queue statistics from
// /proc/net/netfilter/nfnetlink_queue and reduces them to the totals the load balancer exposes
// as metrics: the current backlog (packets waiting for a userspace verdict) and the cumulative
// drop count.
//
// The file is a header-less, space-separated table with one row per bound queue. Each row has
// nine fields (kernel source: net/netfilter/nfnetlink_queue.c, seq_show):
//
//	queue_number  peer_portid  queue_total  copy_mode  copy_range  queue_dropped  user_dropped  id_sequence  1
//
// Only three are used here:
//
//   - queue_total   (field 3) — packets currently queued, i.e. the instantaneous backlog. This is
//     a gauge, not a counter: a load balancer that keeps up drains the queue immediately, so it
//     reads 0 almost always and only rises under backpressure.
//   - queue_dropped (field 6) — packets the kernel dropped because the queue was full ("Qdrop").
//     A saturation signal; pairs with queue_total to show how close the queue is to overflowing.
//   - user_dropped  (field 7) — packets dropped while delivering the verdict to userspace ("Udrop").
//     An error signal, independent of queue fullness.
//
// queue_dropped and user_dropped are kept as separate totals (not summed): one is saturation, the
// other is a delivery error, and they drive different operator actions. The remaining fields
// (peer_portid, copy_mode, copy_range, id_sequence, and the trailing constant 1) are read past but
// not exposed.
package nfqueueproc

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procPath is the kernel file listing per-queue netfilter statistics. It is net-namespace scoped,
// so a process reads the queues bound in its own network namespace.
const procPath = "/proc/net/netfilter/nfnetlink_queue"

// fieldCount is the number of space-separated columns in each row of the proc file.
const fieldCount = 9

// Column indices (0-based) of the fields read from a row.
const (
	colQueueNumber  = 0
	colQueueTotal   = 2
	colQueueDropped = 5
	colUserDropped  = 6
)

// Stats is the reduced per-netns view the collector consumes. All fields are summed over the
// queues in the configured range.
type Stats struct {
	// Items is the backlog (queue_total): packets currently waiting for a userspace verdict. A
	// gauge.
	Items uint64
	// QueueDropped is the queue-full drop count (queue_dropped, "Qdrop") — a saturation signal.
	// Cumulative since the queues were bound.
	QueueDropped uint64
	// UserDropped is the verdict-delivery drop count (user_dropped, "Udrop") — an error signal.
	// Cumulative since the queues were bound.
	UserDropped uint64
}

// Reader reads and reduces the nfqueue proc file for a fixed, inclusive queue-number range. The
// range is the load balancer's configured NFQUEUE range, so queues bound by anything else in the
// same network namespace are not counted.
type Reader struct {
	queueStart int
	queueEnd   int // inclusive
	// path is the file to read; overridable in tests. Empty means procPath.
	path string
}

// NewReader builds a Reader for the inclusive queue-number range [queueStart, queueEnd].
func NewReader(queueStart, queueEnd int) *Reader {
	return &Reader{queueStart: queueStart, queueEnd: queueEnd}
}

// Read reads the proc file and returns the summed backlog and drop totals across the queues whose
// number is within the reader's configured range. A missing/garbled row is skipped; a read error
// on the file itself is returned.
func (r *Reader) Read() (Stats, error) {
	path := r.path
	if path == "" {
		path = procPath
	}
	data, err := os.ReadFile(path) //nolint:gosec // fixed kernel proc path, not user input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Defensive: the file is absent only when the nfnetlink_queue module is not loaded.
			// Supported platforms always have it (Kind auto-loads; OpenShift persists it via
			// /etc/modules-load.d), so this is not a normal state there — report zero rather than
			// a collection error for an unsupported/misconfigured kernel. (Queues merely "not
			// bound yet" show up as an empty file, which parse handles as zero.)
			return Stats{}, nil
		}
		return Stats{}, fmt.Errorf("read %s: %w", path, err)
	}
	return r.parse(string(data)), nil
}

// parse reduces the raw proc-file contents to Stats. It is a pure function (no I/O) so it is fully
// unit-testable from a fixture. Rows that do not have nine fields, whose queue number is not an
// integer or falls outside the configured range, or whose count columns are not integers, are
// skipped.
func (r *Reader) parse(content string) Stats {
	var s Stats
	for line := range strings.SplitSeq(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) != fieldCount {
			continue // blank line or an unexpected layout — skip rather than guess
		}
		qnum, err := strconv.Atoi(fields[colQueueNumber])
		if err != nil || qnum < r.queueStart || qnum > r.queueEnd {
			continue
		}
		total, errT := strconv.ParseUint(fields[colQueueTotal], 10, 64)
		qdrop, errQ := strconv.ParseUint(fields[colQueueDropped], 10, 64)
		udrop, errU := strconv.ParseUint(fields[colUserDropped], 10, 64)
		if errT != nil || errQ != nil || errU != nil {
			continue // a non-numeric where a count is expected — skip the row
		}
		s.Items += total
		s.QueueDropped += qdrop
		s.UserDropped += udrop
	}
	return s
}
