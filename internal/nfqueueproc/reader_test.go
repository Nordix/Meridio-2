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

package nfqueueproc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Captured verbatim from a running LB pod (queue range 0:3) with no traffic. Backlog and both
// drop columns are 0; id_sequence (col 8) is 0.
const procIdle = `    0     21     0 2  1500     0     0        0  1
    1 4112809925     0 2  1500     0     0        0  1
    2 3667197178     0 2  1500     0     0        0  1
    3 4009965203     0 2  1500     0     0        0  1
`

// Captured verbatim from the same pod after running tests: queue 0 has processed 625 packets
// (col 8), but the backlog (col 3) and drops (cols 6/7) are still 0 — the LB kept up.
const procAfterTraffic = `    0     21     0 2  1500     0     0      625  1
    1 4112809925     0 2  1500     0     0        0  1
    2 3667197178     0 2  1500     0     0        0  1
    3 4009965203     0 2  1500     0     0        0  1
`

// Synthesized from the real column layout (hand-edited cols 3/6/7) to exercise the sum/filter
// arithmetic, since a real backlog/drop snapshot needs sustained overload. Queue 0: backlog 5,
// Qdrop 2, Udrop 1. Queue 1: backlog 3, Qdrop 0, Udrop 4. Queues 2/3 idle.
const procSynthLoaded = `    0     21     5 2  1500     2     1      900  1
    1 4112809925     3 2  1500     0     4      450  1
    2 3667197178     0 2  1500     0     0        0  1
    3 4009965203     0 2  1500     0     0        0  1
`

func TestParse_RealIdle_AllZero(t *testing.T) {
	r := NewReader(0, 3)
	s := r.parse(procIdle)
	assert.Equal(t, uint64(0), s.Items)
	assert.Equal(t, uint64(0), s.Dropped)
}

func TestParse_RealAfterTraffic_StillZero(t *testing.T) {
	// id_sequence moving (col 8) must NOT affect items/dropped — only cols 3/6/7 matter.
	r := NewReader(0, 3)
	s := r.parse(procAfterTraffic)
	assert.Equal(t, uint64(0), s.Items)
	assert.Equal(t, uint64(0), s.Dropped)
}

func TestParse_SynthLoaded_SumsAcrossRange(t *testing.T) {
	r := NewReader(0, 3)
	s := r.parse(procSynthLoaded)
	// Items = 5 + 3 + 0 + 0.
	assert.Equal(t, uint64(8), s.Items)
	// Dropped = (2+1) + (0+4) + 0 + 0.
	assert.Equal(t, uint64(7), s.Dropped)
}

func TestParse_RangeFilter_SingleQueue(t *testing.T) {
	// Only queue 0 is in range; queue 1's backlog/drops are excluded.
	r := NewReader(0, 0)
	s := r.parse(procSynthLoaded)
	assert.Equal(t, uint64(5), s.Items)
	assert.Equal(t, uint64(3), s.Dropped) // 2 + 1
}

func TestParse_RangeFilter_Subset(t *testing.T) {
	// Queues 1..2 only: queue 1 has backlog 3 / drops 4, queue 2 idle.
	r := NewReader(1, 2)
	s := r.parse(procSynthLoaded)
	assert.Equal(t, uint64(3), s.Items)
	assert.Equal(t, uint64(4), s.Dropped)
}

func TestParse_RangeOutsideBoundQueues_Empty(t *testing.T) {
	// No bound queue falls in [10,12].
	r := NewReader(10, 12)
	s := r.parse(procSynthLoaded)
	assert.Equal(t, uint64(0), s.Items)
	assert.Equal(t, uint64(0), s.Dropped)
}

func TestParse_MalformedLines_Skipped(t *testing.T) {
	content := "" +
		"garbage line with too few fields\n" +
		"    0     21     5 2  1500     2     1      900  1\n" + // valid: items 5, drops 3
		"a b c d e f g h i\n" + // 9 fields but non-numeric where counts expected — skipped
		"\n" + // blank — skipped
		"    1 4112809925     3 2  1500     0     4      450  1 EXTRA\n" // 10 fields — skipped
	r := NewReader(0, 3)
	s := r.parse(content)
	assert.Equal(t, uint64(5), s.Items)
	assert.Equal(t, uint64(3), s.Dropped)
}

func TestParse_Empty(t *testing.T) {
	r := NewReader(0, 3)
	s := r.parse("")
	assert.Equal(t, uint64(0), s.Items)
	assert.Equal(t, uint64(0), s.Dropped)
}

func TestRead_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nfnetlink_queue")
	require.NoError(t, os.WriteFile(path, []byte(procSynthLoaded), 0o600))

	r := NewReader(0, 3)
	r.path = path // override the proc path for the test
	s, err := r.Read()
	require.NoError(t, err)
	assert.Equal(t, uint64(8), s.Items)
	assert.Equal(t, uint64(7), s.Dropped)
}

func TestRead_MissingFile_IsZeroNotError(t *testing.T) {
	// A missing proc file means the nfnetlink_queue module is not loaded / no queue is bound yet
	// (normal at startup) — report zero stats, not a read failure, so the collector does not count
	// a spurious error.
	r := NewReader(0, 3)
	r.path = filepath.Join(t.TempDir(), "does-not-exist")
	s, err := r.Read()
	require.NoError(t, err)
	assert.Equal(t, uint64(0), s.Items)
	assert.Equal(t, uint64(0), s.Dropped)
}
