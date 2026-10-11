package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type listeningAddressWriter struct{ ready chan string }

func (w listeningAddressWriter) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if strings.HasPrefix(line, "Clivoy listening on ") {
		w.ready <- strings.TrimPrefix(line, "Clivoy listening on ")
	}
	return len(p), nil
}

func TestServeWaitsForActiveHandlerCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	cancellationSeen := make(chan struct{})
	allowCleanup := make(chan struct{})
	cleaned := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(allowCleanup) }) }
	defer release()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancellationSeen)
		// A CLI provider can need time to kill descendants and wait for exit.
		<-allowCleanup
		close(cleaned)
		w.WriteHeader(http.StatusNoContent)
	})
	ready := make(chan string, 1)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serve(ctx, "127.0.0.1:0", handler, listeningAddressWriter{ready: ready}, io.Discard)
	}()
	var address string
	select {
	case address = <-ready:
	case err := <-serverDone:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not report its address")
	}
	client := &http.Client{Timeout: 4 * time.Second}
	defer client.CloseIdleConnections()
	clientDone := make(chan error, 1)
	go func() {
		response, err := client.Get(address)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				err = fmt.Errorf("unexpected shutdown response status %d", response.StatusCode)
			}
		}
		clientDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request handler was not started")
	}
	cancel()
	select {
	case <-cancellationSeen:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the active request")
	}
	select {
	case err := <-serverDone:
		t.Fatalf("serve returned before handler cleanup finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not finish after cleanup")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("serve returned before request cleanup")
	}
	select {
	case err := <-clientDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown response was not delivered")
	}
}
