package outputs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kseer/kseer-agent/pkg/config"
	"github.com/kseer/kseer-agent/pkg/types"
	"go.uber.org/zap"
)

// Manager manages multiple outputs.
//
// The Manager runs two independent processing pipelines:
//
//   - The default pipeline (buffer/flushRequests/batches) handles P2 and P3
//     events. It uses time- and size-based batch flushing with a worker pool
//     of size bufferCfg.FlushConcurrency draining batches in parallel.
//   - The P1 pipeline (p1Buffer/p1FlushRequests/p1Batches) is dedicated to
//     P1 events so that the watcher's sequential per-kind resync can call
//     FlushP1 and block until that kind's events are durable in CH without
//     contending with other clusters' P2/P3 traffic. P1 always uses a single
//     worker — the watcher serializes per-kind resync, so concurrent P1
//     workers would sit idle.
//
// For each pipeline, processLoop accumulates events into batches and hands
// them off to a bounded `batches` channel. A pool of flushWorker goroutines
// pulls batches off the channel and calls sendBatch. An inflightWG per
// pipeline tracks batches that have been handed off but not yet acked; the
// flush case waits on this WG before signaling done, so Flush/FlushP1
// preserve the "events durable in CH on return" contract.
type Manager struct {
	outputs []Output
	logger  *zap.Logger

	// Default (P2/P3) pipeline.
	buffer        chan types.Event
	flushRequests chan chan struct{}
	batches       chan []types.Event
	inflightWG    sync.WaitGroup

	// P1 pipeline.
	p1Buffer        chan types.Event
	p1FlushRequests chan chan struct{}
	p1Batches       chan []types.Event
	p1InflightWG    sync.WaitGroup

	flushInterval    time.Duration
	batchSize        int
	flushConcurrency int

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// NewManager creates a new output manager
func NewManager(configs []config.OutputConfig, bufferCfg config.BufferConfig, logger *zap.Logger) (*Manager, error) {

	// Validate that at least one output is configured
	if len(configs) == 0 {
		return nil, fmt.Errorf("no outputs configured - at least one output (kafka, clickhouse, or stdout) must be enabled in the configuration")
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Use configured batch size, default to 1000 if not set
	batchSize := bufferCfg.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}

	// flush_concurrency <= 0 preserves the original "single in-flight batch"
	// behavior. Recommended production value is 2-4 for the default pipeline.
	flushConcurrency := bufferCfg.FlushConcurrency
	if flushConcurrency <= 0 {
		flushConcurrency = 1
	}

	m := &Manager{
		outputs:          make([]Output, 0, len(configs)),
		logger:           logger,
		buffer:           make(chan types.Event, bufferCfg.Size),
		p1Buffer:         make(chan types.Event, bufferCfg.Size),
		batches:          make(chan []types.Event, flushConcurrency),
		p1Batches:        make(chan []types.Event, 1),
		flushInterval:    bufferCfg.FlushInterval,
		batchSize:        batchSize,
		flushConcurrency: flushConcurrency,
		ctx:              ctx,
		cancel:           cancel,
		flushRequests:    make(chan chan struct{}, 1),
		p1FlushRequests:  make(chan chan struct{}, 1),
	}

	// Create outputs
	for i, cfg := range configs {
		// Validate output type is not empty
		if cfg.Type == "" {
			// Clean up already created outputs
			for _, o := range m.outputs {
				o.Close()
			}
			return nil, fmt.Errorf("output at index %d has empty type - check configuration file outputs section", i)
		}

		output, err := Create(cfg.Type, cfg.Config, logger)
		if err != nil {
			// Clean up already created outputs
			for _, o := range m.outputs {
				o.Close()
			}
			return nil, err
		}

		m.outputs = append(m.outputs, output)
		logger.Info("Initialized output",
			zap.String("type", cfg.Type),
			zap.Int("index", i))
	}

	// Start the two processLoops.
	m.wg.Add(2)
	go m.processLoop(m.buffer, m.flushRequests, m.batches, &m.inflightWG)
	go m.processLoop(m.p1Buffer, m.p1FlushRequests, m.p1Batches, &m.p1InflightWG)

	// Default pipeline workers.
	m.wg.Add(flushConcurrency)
	for i := 0; i < flushConcurrency; i++ {
		go m.flushWorker(m.batches, &m.inflightWG)
	}

	// P1 pipeline always runs with one worker — see type doc.
	m.wg.Add(1)
	go m.flushWorker(m.p1Batches, &m.p1InflightWG)

	logger.Info("Output manager started",
		zap.Int("default_flush_concurrency", flushConcurrency),
		zap.Int("p1_flush_concurrency", 1),
		zap.Int("batch_size", batchSize))

	return m, nil
}

// Send sends an event through the default (P2/P3) pipeline.
func (m *Manager) Send(event types.Event) error {
	return m.send(event, m.buffer, "default")
}

// SendP1 sends an event through the P1-dedicated pipeline. Use this for
// events emitted by the P1 watch and resync paths so that P1 FlushP1 only
// blocks on P1 traffic.
func (m *Manager) SendP1(event types.Event) error {
	return m.send(event, m.p1Buffer, "p1")
}

func (m *Manager) send(event types.Event, buffer chan types.Event, pipeline string) error {
	select {
	case buffer <- event:
		return nil
	case <-m.ctx.Done():
		return m.ctx.Err()
	default:
		kind := ""
		name := ""
		if event.Object != nil {
			if k, ok := event.Object["kind"].(string); ok {
				kind = k
			}
			if metadata, ok := event.Object["metadata"].(map[string]interface{}); ok {
				if n, ok := metadata["name"].(string); ok {
					name = n
				}
			}
		}
		m.logger.Warn("Buffer full, dropping event",
			zap.String("pipeline", pipeline),
			zap.String("cluster", event.ClusterName),
			zap.String("kind", kind),
			zap.String("name", name))
		return nil
	}
}

// Flush blocks until all events currently in the default buffer have been
// sent to all outputs. It must be called before writing any out-of-band
// sentinel that depends on the buffered events being durable in the
// downstream store.
func (m *Manager) Flush(ctx context.Context) error {
	return m.flush(ctx, m.flushRequests)
}

// FlushP1 blocks until all P1 events currently buffered have been sent.
// Used by the watcher's per-kind sequential resync to wait for kind X's
// EXISTING events to land in CH before listing kind Y.
func (m *Manager) FlushP1(ctx context.Context) error {
	return m.flush(ctx, m.p1FlushRequests)
}

func (m *Manager) flush(ctx context.Context, requests chan chan struct{}) error {
	done := make(chan struct{})
	select {
	case requests <- done:
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

// processLoop drains events from buffer, batching by size/time, and honors
// explicit flush requests. Each completed batch is handed off to the
// pipeline's worker pool via `batches`; the worker pool calls sendBatch
// concurrently. inflightWG tracks handoffs so the flush case can wait for
// all in-flight batches to be durable before signaling done.
//
// On shutdown (ctx.Done), processLoop hands off any remaining partial batch
// and closes `batches` so workers `range`-ing over it can exit.
func (m *Manager) processLoop(
	buffer chan types.Event,
	requests chan chan struct{},
	batches chan []types.Event,
	inflightWG *sync.WaitGroup,
) {
	defer m.wg.Done()
	defer close(batches)

	batch := make([]types.Event, 0, m.batchSize)
	ticker := time.NewTicker(m.flushInterval)
	defer ticker.Stop()

	// enqueue hands off the current batch to the worker pool. Add(1) MUST
	// happen on the producer side (here) before the channel push — if a
	// worker did Add on dequeue, a flush could observe the WG counter as
	// zero in the gap between channel receive and Add, returning before
	// the batch is actually durable.
	enqueue := func(b []types.Event) {
		if len(b) == 0 {
			return
		}
		inflightWG.Add(1)
		batches <- b
	}

	for {
		select {
		case event := <-buffer:
			batch = append(batch, event)
			if len(batch) >= m.batchSize {
				enqueue(batch)
				// Fresh backing array — the previous one is owned by a worker.
				batch = make([]types.Event, 0, m.batchSize)
			}

		case <-ticker.C:
			if len(batch) > 0 {
				enqueue(batch)
				batch = make([]types.Event, 0, m.batchSize)
			}

		case done := <-requests:
			// Drain any events still queued in the buffer channel before
			// declaring the flush complete. Without this, a Send-then-Flush
			// from the same goroutine could race: Go's select picks
			// pseudo-randomly when multiple cases are ready, so the flush
			// could fire while events still sit in the channel.
			for {
				select {
				case event := <-buffer:
					batch = append(batch, event)
				default:
					goto drained
				}
			}
		drained:
			if len(batch) > 0 {
				enqueue(batch)
				batch = make([]types.Event, 0, m.batchSize)
			}
			// Wait for the final batch we just enqueued AND any earlier
			// async batches that haven't yet been Sent to drain. Only then
			// is every event prior to this Flush call durable in CH.
			inflightWG.Wait()
			close(done)

		case <-m.ctx.Done():
			if len(batch) > 0 {
				// Best-effort: hand off the partial batch on shutdown. The
				// worker's Send will likely error out since m.ctx is already
				// cancelled, but matches the pre-refactor behavior.
				enqueue(batch)
			}
			return
		}
	}
}

// flushWorker pulls batches off the pipeline's `batches` channel and sends
// them to all outputs. Exits when the channel is closed by processLoop on
// shutdown. inflightWG.Done() runs after sendBatch returns so that
// processLoop's flush case sees the batch as in-flight until it's durable.
func (m *Manager) flushWorker(batches chan []types.Event, inflightWG *sync.WaitGroup) {
	defer m.wg.Done()
	for batch := range batches {
		m.sendBatch(batch)
		inflightWG.Done()
	}
}

// sendBatch sends a batch of events to all outputs. Each output is sent to
// in its own goroutine so that multiple outputs (Kafka + ClickHouse, etc.)
// run in parallel within a single batch. Parallelism across batches comes
// from running multiple flushWorker goroutines.
func (m *Manager) sendBatch(events []types.Event) {
	var wg sync.WaitGroup

	for _, output := range m.outputs {
		wg.Add(1)
		go func(o Output) {
			defer wg.Done()

			if err := o.Send(m.ctx, events); err != nil {
				m.logger.Error("Failed to send events",
					zap.String("output", o.Name()),
					zap.Error(err),
					zap.Int("event_count", len(events)))
			} else {
				// Log at INFO level so users can see progress
				m.logger.Info("Sent events",
					zap.String("output", o.Name()),
					zap.Int("event_count", len(events)))
			}
		}(output)
	}

	wg.Wait()
}

// GetOutput returns the first output with the given Name(), if any.
// Used by callers that need direct access to a specific output (e.g. to
// extract a SeederChecker from the ClickHouse output).
func (m *Manager) GetOutput(name string) (Output, bool) {
	for _, o := range m.outputs {
		if o.Name() == name {
			return o, true
		}
	}
	return nil, false
}

// Close closes all outputs. It cancels the manager context, which causes
// each processLoop to exit and close its `batches` channel; workers then
// drain whatever's left in the channel and exit. m.wg.Wait() waits for
// both processLoops and all workers to finish.
func (m *Manager) Close() error {
	m.cancel()
	m.wg.Wait()

	var lastErr error
	for _, output := range m.outputs {
		if err := output.Close(); err != nil {
			m.logger.Error("Failed to close output",
				zap.String("output", output.Name()),
				zap.Error(err))
			lastErr = err
		}
	}

	return lastErr
}
