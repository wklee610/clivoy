package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

func TestEightConcurrentRequestsShareQueueAcrossProtocols(t *testing.T) {
	const concurrent, queued = 8, 32
	paths := []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"}
	// Each token completes exactly one provider call. Leaving this channel
	// empty lets the test observe actual overlap without timing sleeps.
	release := make(chan struct{}, concurrent+queued)
	var active, peak atomic.Int32
	p := &fakeProvider{generate: func(ctx context.Context, _ engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); n > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, n) {
				break
			}
		}
		select {
		case <-release:
			return engine.Result{Text: "completed", FinishReason: "stop"}, nil
		case <-ctx.Done():
			return engine.Result{}, ctx.Err()
		}
	}}
	s := testServer(t, p, func(o *Options) {
		o.MaxConcurrent = concurrent
		o.MaxQueue = queued
		o.RequestTimeout = 10 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("request handlers did not stop after test cleanup")
		}
	})
	type response struct {
		path string
		body *httptest.ResponseRecorder
	}
	responses := make(chan response, concurrent+queued+len(paths))
	launch := func(path string) {
		request := apiRequest(path, bodyFor(path, false)).WithContext(ctx)
		workers.Add(1)
		go func() {
			defer workers.Done()
			responses <- response{path: path, body: invoke(s, request)}
		}()
	}
	awaitState := func(description string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if ready() {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("%s: provider calls=%d active=%d peak=%d queued=%d slots=%d",
			description, p.calls.Load(), active.Load(), peak.Load(), len(s.queue), len(s.slots))
	}
	awaitResponse := func(status int) {
		t.Helper()
		select {
		case result := <-responses:
			if result.body.Code != status {
				t.Fatalf("%s returned %d, want %d: %s", result.path, result.body.Code, status, result.body.Body)
			}
			if status == http.StatusTooManyRequests && result.body.Header().Get("Retry-After") != "1" {
				t.Fatalf("%s queue rejection lacks retry advice", result.path)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("request did not finish with expected status %d", status)
		}
	}

	for i := 0; i < concurrent; i++ {
		launch(paths[i%len(paths)])
	}
	awaitState("eight requests must enter the provider together", func() bool {
		return active.Load() == concurrent && p.calls.Load() == concurrent
	})
	for i := 0; i < queued; i++ {
		launch(paths[i%len(paths)])
	}
	awaitState("all 32 additional requests must wait", func() bool { return len(s.queue) == queued })
	if p.calls.Load() != concurrent || active.Load() != concurrent {
		t.Fatal("a waiting request entered the provider before a slot was released")
	}
	// The 41st request is rejected on every protocol: none has its own extra
	// provider slots or independent waiting-room capacity.
	for _, path := range paths {
		launch(path)
		awaitResponse(http.StatusTooManyRequests)
	}
	if p.calls.Load() != concurrent || len(s.queue) != queued {
		t.Fatal("a rejected request invoked the provider or disturbed the waiting queue")
	}

	release <- struct{}{}
	awaitResponse(http.StatusOK)
	awaitState("one completion must admit exactly one queued request", func() bool {
		return p.calls.Load() == concurrent+1 && active.Load() == concurrent && len(s.queue) == queued-1
	})
	for i := 0; i < concurrent+queued-1; i++ {
		release <- struct{}{}
	}
	for i := 0; i < concurrent+queued-1; i++ {
		awaitResponse(http.StatusOK)
	}
	if p.calls.Load() != concurrent+queued || active.Load() != 0 || len(s.queue) != 0 || len(s.slots) != 0 {
		t.Fatal("completing all accepted requests did not return every queue and provider permit")
	}

	// Refill all eight slots after draining, rather than only inspecting the
	// semaphore: leaked permits must not reduce subsequent usable capacity.
	for i := 0; i < concurrent; i++ {
		launch(paths[i%len(paths)])
	}
	awaitState("all eight slots must be reusable", func() bool { return active.Load() == concurrent })
	for i := 0; i < concurrent; i++ {
		release <- struct{}{}
	}
	for i := 0; i < concurrent; i++ {
		awaitResponse(http.StatusOK)
	}
	if peak.Load() != concurrent || p.calls.Load() != 2*concurrent+queued || active.Load() != 0 || len(s.queue) != 0 || len(s.slots) != 0 {
		t.Fatalf("capacity was exceeded or not fully returned: calls=%d active=%d peak=%d queued=%d slots=%d",
			p.calls.Load(), active.Load(), peak.Load(), len(s.queue), len(s.slots))
	}
}
