package invocation

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/mcp"
)

type memoryStreamEventRepo struct {
	mu     sync.Mutex
	events []Event
}

type orderedStreamEventRepo struct {
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
}

func (r *orderedStreamEventRepo) Create(ctx context.Context, event Event) error {
	if event.EventType != "invocation.stream_event" {
		return nil
	}
	var payload struct {
		Seq int `json:"seq"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	switch payload.Seq {
	case 1:
		r.firstOnce.Do(func() { close(r.firstStarted) })
		select {
		case <-r.releaseFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
	case 2:
		r.secondOnce.Do(func() { close(r.secondStarted) })
	}
	return nil
}

func (r *orderedStreamEventRepo) ListByInvocation(context.Context, string, EventListFilter) ([]Event, int, error) {
	return nil, 0, nil
}

func (r *memoryStreamEventRepo) Create(_ context.Context, event Event) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *memoryStreamEventRepo) ListByInvocation(context.Context, string, EventListFilter) ([]Event, int, error) {
	return nil, 0, nil
}

func (r *memoryStreamEventRepo) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func TestAuditingSinkCreationDoesNotStartPerInvocationGoroutine(t *testing.T) {
	repo := &memoryStreamEventRepo{}
	before := runtime.NumGoroutine()
	sinks := make([]*auditingSink, 1000)
	for i := range sinks {
		sinks[i] = newAuditingSink(nil, repo, "inv", nil, "upstream", StreamAuditLimits{})
	}
	runtime.Gosched()
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()
	t.Logf("goroutine growth after creating 1000 sinks: %d", after-before)

	if growth := after - before; growth > 16 {
		t.Fatalf("creating 1000 audit sinks added %d goroutines; want service-level bounded workers", growth)
	}
	for _, sink := range sinks {
		sink.finish(time.Now().UTC(), "succeeded")
	}
}

func TestSharedAuditDispatcherHandlesOneThousandConcurrentSinks(t *testing.T) {
	repo := &memoryStreamEventRepo{}
	var wg sync.WaitGroup
	wg.Add(1000)
	for i := 0; i < 1000; i++ {
		go func() {
			defer wg.Done()
			sink := newAuditingSink(nil, repo, "inv-load", nil, "upstream", StreamAuditLimits{MaxEvents: 1})
			_ = sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress"}`)})
			sink.finish(time.Now().UTC(), "succeeded")
		}()
	}
	wg.Wait()

	completed := 0
	for _, event := range repo.snapshot() {
		if event.EventType == "invocation.stream_completed" {
			completed++
		}
	}
	if completed != 1000 {
		t.Fatalf("stream completion rows = %d, want 1000", completed)
	}
}

func TestAuditingSinkIncludesStandaloneDropsInCompletion(t *testing.T) {
	repo := &memoryStreamEventRepo{}
	sink := newAuditingSink(nil, repo, "inv-drops", nil, "upstream", StreamAuditLimits{})
	sink.StreamStats(mcp.StreamStats{StandaloneEventsDropped: 7})
	sink.finish(time.Now().UTC(), "succeeded")

	for _, event := range repo.snapshot() {
		if event.EventType != "invocation.stream_completed" {
			continue
		}
		var payload struct {
			StandaloneEventsDropped int64 `json:"standalone_events_dropped"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.StandaloneEventsDropped != 7 {
			t.Fatalf("standalone_events_dropped = %d, want 7", payload.StandaloneEventsDropped)
		}
		return
	}
	t.Fatal("missing invocation.stream_completed event")
}

func TestAuditingSinkPreservesEventOrderThroughSharedWorkers(t *testing.T) {
	repo := &orderedStreamEventRepo{
		firstStarted:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	sink := newAuditingSink(nil, repo, "inv-order", nil, "upstream", StreamAuditLimits{})
	_ = sink.Event(mcp.StreamEvent{Data: []byte(`{"progress":1}`)})
	_ = sink.Event(mcp.StreamEvent{Data: []byte(`{"progress":2}`)})

	select {
	case <-repo.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first audit write did not start")
	}
	select {
	case <-repo.secondStarted:
		t.Fatal("second event started before the first event completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(repo.releaseFirst)
	sink.finish(time.Now().UTC(), "succeeded")
	select {
	case <-repo.secondStarted:
	default:
		t.Fatal("second event never ran after the first completed")
	}
}

func BenchmarkAuditingSinkDispatch(b *testing.B) {
	repo := &memoryStreamEventRepo{}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sink := newAuditingSink(nil, repo, "inv", nil, "upstream", StreamAuditLimits{MaxEvents: 1})
			_ = sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress"}`)})
			sink.finish(time.Now().UTC(), "succeeded")
		}
	})
}
