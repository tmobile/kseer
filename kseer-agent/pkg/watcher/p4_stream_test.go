package watcher

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/kseer/kseer-agent/pkg/config"
	"github.com/kseer/kseer-agent/pkg/outputs"
	"github.com/kseer/kseer-agent/pkg/types"
)

// captureSink records the (kind, operation, name) of every event the capturing
// output receives, so the P4 stream test can assert which operations were and
// were not emitted.
type captureSink struct {
	mu  sync.Mutex
	ops []capturedEvent
}

type capturedEvent struct {
	kind      string
	operation string
	name      string
}

func (s *captureSink) add(e types.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kind, _ := e.Object["kind"].(string)
	name := ""
	if md, ok := e.Object["metadata"].(map[string]interface{}); ok {
		name, _ = md["name"].(string)
	}
	s.ops = append(s.ops, capturedEvent{kind: kind, operation: e.Operation, name: name})
}

func (s *captureSink) snapshot() []capturedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedEvent, len(s.ops))
	copy(out, s.ops)
	return out
}

type captureOutput struct{ sink *captureSink }

func (o *captureOutput) Send(_ context.Context, events []types.Event) error {
	for _, e := range events {
		o.sink.add(e)
	}
	return nil
}
func (o *captureOutput) Close() error  { return nil }
func (o *captureOutput) Name() string  { return "p4capture" }

// p4TestSink is the sink the registered "p4capture" output writes to. The test
// sets it before constructing the output manager (the factory is invoked
// synchronously inside NewManager).
var p4TestSink *captureSink

func init() {
	outputs.Register("p4capture", func(_ map[string]interface{}, _ *zap.Logger) (outputs.Output, error) {
		return &captureOutput{sink: p4TestSink}, nil
	})
}

// TestP4StreamFromNow_NoExistingNoSeeded verifies the P4 (stream) path:
//   - it opens a WATCH and forwards subsequent changes as ADDED, and
//   - it never emits an initial EXISTING burst for pre-existing objects, and
//   - it never emits a SEEDED sentinel.
func TestP4StreamFromNow_NoExistingNoSeeded(t *testing.T) {
	p4TestSink = &captureSink{}

	mgr, err := outputs.NewManager(
		[]config.OutputConfig{{Type: "p4capture"}},
		config.BufferConfig{Size: 1000, FlushInterval: 10 * time.Millisecond, BatchSize: 1},
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	gvrKey := gvrToString(gvr)

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{gvr: "EventList"},
	)

	makeEvent := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Event",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "default",
			},
		}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A pre-existing object the stream tier must NOT replay (no initial LIST).
	if _, err := client.Resource(gvr).Namespace("default").Create(ctx, makeEvent("preexisting-1"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed pre-existing object: %v", err)
	}

	w := &ClusterWatcher{
		clusterName:      "test",
		dynamicClient:    client,
		outputManager:    mgr,
		logger:           zap.NewNop(),
		rateLimiter:      rate.NewLimiter(rate.Inf, 1),
		watchConfig:      config.WatchConfig{AllowBookmarks: true, TimeoutSeconds: 3600},
		watchedResources: map[string]context.CancelFunc{},
		gvrKindMap:       map[string]string{gvrKey: "Event"},
		gvrTier:          map[string]Tier{gvrKey: TierP4},
		gvrBookmarkRVs:   map[string]string{},
		kindLastEvent:    map[string]time.Time{},
		kindEventCount:   map[string]int64{},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Single connection (not the reconnect loop) keeps the test
		// deterministic; runWatchEventLoop arms no resync timer for P4.
		_ = w.streamFromNow(ctx, gvr)
	}()

	// The fake watcher only delivers events created after the watch opens, and
	// we can't observe the exact open moment, so create live objects in a loop
	// until one is captured as ADDED.
	deadline := time.After(3 * time.Second)
	i := 0
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for an ADDED event via the P4 stream")
		default:
		}
		i++
		_, _ = client.Resource(gvr).Namespace("default").Create(ctx, makeEvent(fmt.Sprintf("live-%d", i)), metav1.CreateOptions{})

		gotAdded := false
		for _, e := range p4TestSink.snapshot() {
			if e.operation == "ADDED" && strings.HasPrefix(e.name, "live-") {
				gotAdded = true
				break
			}
		}
		if gotAdded {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done
	w.wg.Wait() // wait for in-flight handleObject goroutines to finish sending
	if err := mgr.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	sawAdded := false
	for _, e := range p4TestSink.snapshot() {
		if e.kind != "Event" {
			continue
		}
		if e.operation == "EXISTING" {
			t.Errorf("P4 stream must not emit EXISTING events; got one for %q", e.name)
		}
		if e.operation == "SEEDED" {
			t.Errorf("P4 stream must not emit SEEDED sentinels; got one for %q", e.name)
		}
		if e.name == "preexisting-1" {
			t.Errorf("P4 stream must not replay pre-existing objects; got %q", e.name)
		}
		if e.operation == "ADDED" {
			sawAdded = true
		}
	}
	if !sawAdded {
		t.Error("expected at least one ADDED event from the P4 stream")
	}
}
