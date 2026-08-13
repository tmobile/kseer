package outputs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kseer/kseer-agent/pkg/config"
	"github.com/kseer/kseer-agent/pkg/types"
	"go.uber.org/zap"
)

// recordingOutput captures every batch it receives. Used to assert that
// FlushP1 / Flush only return after the events have been pushed downstream.
type recordingOutput struct {
	name    string
	mu      sync.Mutex
	batches [][]types.Event
	delay   time.Duration
	// inflight tracks concurrent Send calls — used by tests asserting that
	// the worker pool actually runs sends in parallel.
	inflight    atomic.Int32
	maxInflight atomic.Int32
}

func (r *recordingOutput) Send(_ context.Context, events []types.Event) error {
	cur := r.inflight.Add(1)
	for {
		max := r.maxInflight.Load()
		if cur <= max || r.maxInflight.CompareAndSwap(max, cur) {
			break
		}
	}
	defer r.inflight.Add(-1)
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]types.Event, len(events))
	copy(cp, events)
	r.batches = append(r.batches, cp)
	return nil
}
func (r *recordingOutput) Close() error { return nil }
func (r *recordingOutput) Name() string {
	if r.name == "" {
		return "recording"
	}
	return r.name
}

func (r *recordingOutput) totalEvents() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, b := range r.batches {
		n += len(b)
	}
	return n
}

// newTestManager wires a manager with a single recordingOutput, bypassing the
// registry-based factory used in production. flushConcurrency controls the
// number of default-pipeline workers (P1 is always 1).
func newTestManager(t *testing.T, bufferSize int, flushInterval time.Duration, batchSize, flushConcurrency int) (*Manager, *recordingOutput) {
	t.Helper()
	if flushConcurrency <= 0 {
		flushConcurrency = 1
	}
	rec := &recordingOutput{}
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		outputs:          []Output{rec},
		logger:           logger,
		buffer:           make(chan types.Event, bufferSize),
		p1Buffer:         make(chan types.Event, bufferSize),
		batches:          make(chan []types.Event, flushConcurrency),
		p1Batches:        make(chan []types.Event, 1),
		flushInterval:    flushInterval,
		batchSize:        batchSize,
		flushConcurrency: flushConcurrency,
		ctx:              ctx,
		cancel:           cancel,
		flushRequests:    make(chan chan struct{}, 1),
		p1FlushRequests: make(chan chan struct{}, 1),
	}
	m.wg.Add(2)
	go m.processLoop(m.buffer, m.flushRequests, m.batches, &m.inflightWG)
	go m.processLoop(m.p1Buffer, m.p1FlushRequests, m.p1Batches, &m.p1InflightWG)
	m.wg.Add(flushConcurrency)
	for i := 0; i < flushConcurrency; i++ {
		go m.flushWorker(m.batches, &m.inflightWG)
	}
	m.wg.Add(1)
	go m.flushWorker(m.p1Batches, &m.p1InflightWG)
	t.Cleanup(func() { _ = m.Close() })
	return m, rec
}

func TestManager_FlushP1_BlocksUntilDurable(t *testing.T) {
	m, rec := newTestManager(t, 100, time.Hour, 100, 1)
	for i := 0; i < 10; i++ {
		if err := m.SendP1(types.Event{ClusterName: "c", Operation: "EXISTING"}); err != nil {
			t.Fatalf("SendP1 failed: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.FlushP1(ctx); err != nil {
		t.Fatalf("FlushP1 failed: %v", err)
	}
	if got := rec.totalEvents(); got != 10 {
		t.Errorf("expected 10 events durable after FlushP1, got %d", got)
	}
}

func TestManager_P1AndDefaultBuffersIndependent(t *testing.T) {
	m, rec := newTestManager(t, 100, time.Hour, 100, 1)

	// Fill the default buffer with 5 events; do NOT flush them.
	for i := 0; i < 5; i++ {
		if err := m.Send(types.Event{ClusterName: "c", Operation: "MODIFIED"}); err != nil {
			t.Fatalf("Send failed: %v", err)
		}
	}
	// Send 3 P1 events and FlushP1.
	for i := 0; i < 3; i++ {
		if err := m.SendP1(types.Event{ClusterName: "c", Operation: "EXISTING"}); err != nil {
			t.Fatalf("SendP1 failed: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.FlushP1(ctx); err != nil {
		t.Fatalf("FlushP1 failed: %v", err)
	}

	// The P1 events MUST have made it to the output. The default-buffer events
	// MAY also have been sent (if processLoop happened to drain them) — what
	// matters is that FlushP1 returned without waiting on them.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	totalP1 := 0
	for _, batch := range rec.batches {
		for _, e := range batch {
			if e.Operation == "EXISTING" {
				totalP1++
			}
		}
	}
	if totalP1 != 3 {
		t.Errorf("expected 3 P1 events durable after FlushP1, got %d (batches: %d)", totalP1, len(rec.batches))
	}
}

// TestManager_FlushDrainsBufferBeforeSignaling guards the race fix in
// processLoop: events sent to the buffer immediately before Flush must be
// included in the flushed batch, even though the buffer and flush channels
// are both ready when the select fires.
func TestManager_FlushDrainsBufferBeforeSignaling(t *testing.T) {
	m, rec := newTestManager(t, 1000, time.Hour, 100, 1)
	const n = 500
	for i := 0; i < n; i++ {
		if err := m.Send(types.Event{ClusterName: "c", Operation: "MODIFIED"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := rec.totalEvents(); got != n {
		t.Errorf("Flush returned with %d/%d events durable — race in processLoop", got, n)
	}
}

// TestManager_Flush_WaitsForAsyncBatches guards the worker-pool durability
// invariant: when a slow Send is still in flight on a worker, Flush must not
// return until that Send completes. If processLoop closed `done` immediately
// after enqueuing onto `batches`, Flush would return before the events are
// durable.
func TestManager_Flush_WaitsForAsyncBatches(t *testing.T) {
	// batchSize=50, slow Send of 100ms, 1 worker so the batch is provably
	// still in-flight when Flush runs.
	m, rec := newTestManager(t, 1000, time.Hour, 50, 1)
	rec.delay = 100 * time.Millisecond

	// Fill exactly one batch — this triggers size-based enqueue from
	// processLoop, so by the time we call Flush, a worker is mid-Send.
	for i := 0; i < 50; i++ {
		if err := m.Send(types.Event{ClusterName: "c", Operation: "MODIFIED"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	// Brief sleep so processLoop has a chance to enqueue the batch onto the
	// worker. Without this, the test reduces to FlushDrainsBufferBeforeSignaling.
	time.Sleep(10 * time.Millisecond)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	elapsed := time.Since(start)
	// Flush should have waited for the in-flight Send to finish. Allow some
	// slack but require that we waited for most of the artificial delay.
	if elapsed < 50*time.Millisecond {
		t.Errorf("Flush returned in %v; expected to wait on in-flight Send (~100ms)", elapsed)
	}
	if got := rec.totalEvents(); got != 50 {
		t.Errorf("expected 50 events durable after Flush, got %d", got)
	}
}

// TestManager_FlushConcurrency_RunsSendsInParallel asserts that with
// flushConcurrency > 1, multiple size-triggered batches drain concurrently.
//
// Note: we deliberately exercise the size-based enqueue path (one batch per
// batchSize events) rather than the flush-drain path (which collapses all
// pending events into one batch). The size path is the production hot path.
func TestManager_FlushConcurrency_RunsSendsInParallel(t *testing.T) {
	const (
		workers   = 4
		batchSize = 100
		batches   = 4
	)
	m, rec := newTestManager(t, 10000, time.Hour, batchSize, workers)
	rec.delay = 200 * time.Millisecond // long enough for overlap to be observable

	// Push batches one at a time, briefly yielding so processLoop reads the
	// full batch from the buffer and triggers a size-based enqueue before we
	// push the next batch. This produces N distinct batches on the workers
	// channel rather than one big batch.
	for b := 0; b < batches; b++ {
		for i := 0; i < batchSize; i++ {
			if err := m.Send(types.Event{ClusterName: "c", Operation: "MODIFIED"}); err != nil {
				t.Fatalf("Send: %v", err)
			}
		}
		// Yield so processLoop can drain the buffer up to batchSize and
		// enqueue this batch onto the workers channel.
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := rec.totalEvents(); got != batchSize*batches {
		t.Errorf("expected %d events durable, got %d", batchSize*batches, got)
	}
	if peak := rec.maxInflight.Load(); peak < 2 {
		t.Errorf("expected at least 2 concurrent Sends with flushConcurrency=%d, observed peak %d", workers, peak)
	}
}

// silence unused imports when we reduce coverage in a refactor
var _ = config.OutputConfig{}
