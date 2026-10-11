package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/daemon"
	"github.com/wklee610/clivoy/internal/state"
)

func TestDaemonStoppedCommandsDoNotInitializeHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("CLIVOY_HOME", home)
	for _, command := range []string{"status", "stop"} {
		var out bytes.Buffer
		if err := run([]string{command}, &out, &out); err != nil || !strings.Contains(out.String(), "stopped") {
			t.Fatalf("%s: %s %v", command, &out, err)
		}
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only status initialized home: %v", err)
	}
}

func daemonTestRuntime(t *testing.T) *daemonRuntime {
	t.Helper()
	t.Setenv("CLIVOY_HOME", filepath.Join(t.TempDir(), "home"))
	paths, err := state.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(paths.Root, ".daemon-start-test.json")
	if err := os.WriteFile(readyPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return &daemonRuntime{paths: paths, readyPath: readyPath, record: daemonRecord{
		PID: os.Getpid(), Token: strings.Repeat("a", 64), Config: paths.Config,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), LogFile: paths.Log,
	}}
}

func TestDaemonControlRequiresInstanceTokenAndAcknowledgesShutdown(t *testing.T) {
	d := daemonTestRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.control(cancel); err != nil {
		t.Fatal(err)
	}
	defer d.close()
	if err := d.ready(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45678}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	for _, path := range []string{"/status", "/stop"} {
		req, _ := http.NewRequest(http.MethodPost, d.record.ControlURL+path, nil)
		req.Header.Set("Authorization", "Bearer wrong-token")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 401 || bytes.Contains(body, []byte(d.record.Token)) {
			t.Fatalf("unauthenticated control: %d %s", response.StatusCode, body)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("unauthenticated request cancelled server")
	}
	if err := controlRequest(&d.record, "/status"); err != nil {
		t.Fatal(err)
	}
	if err := controlRequest(&d.record, "/stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("authenticated stop did not cancel server")
	}
}

func TestDaemonDoesNotTrustPIDOrUnrelatedControlServer(t *testing.T) {
	d := daemonTestRuntime(t)
	// Even a live PID cannot establish daemon ownership: the instance lock
	// and authenticated control endpoint must belong to this invocation.
	d.record.ControlURL = "http://127.0.0.1:1"
	if err := writePrivateJSON(filepath.Join(d.paths.Root, "daemon.json"), d.record); err != nil {
		t.Fatal(err)
	}
	_, running, err := inspectDaemon(d.paths)
	if err != nil || running {
		t.Fatalf("trusted a stale PID: running=%v err=%v", running, err)
	}
	lock, err := daemon.TryLock(filepath.Join(d.paths.Root, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"pid":1,"started_at":"not-this-instance"}`)
	}))
	defer foreign.Close()
	d.record.ControlURL = foreign.URL
	if err := writePrivateJSON(filepath.Join(d.paths.Root, "daemon.json"), d.record); err != nil {
		t.Fatal(err)
	}
	if _, running, err := inspectDaemon(d.paths); err == nil || running {
		t.Fatal("accepted an unrelated listener while instance lock was held")
	}
}

func TestDaemonControlRejectsNonlocalURLsAndRedirects(t *testing.T) {
	for _, address := range []string{"https://127.0.0.1:1", "http://example.com:1", "http://localhost:1", "http://127.0.0.1:1/path", "http://user@127.0.0.1:1", "http://127.0.0.1:1?secret=yes"} {
		if err := controlRequest(&daemonRecord{ControlURL: address}, "/status"); err == nil || !strings.Contains(err.Error(), "invalid local control") {
			t.Fatalf("accepted nonlocal control URL %s: %v", address, err)
		}
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1", http.StatusFound)
	}))
	defer redirect.Close()
	if err := controlRequest(&daemonRecord{ControlURL: redirect.URL}, "/status"); err == nil {
		t.Fatal("accepted control redirect")
	}
}

func TestDaemonStateFilesRejectUnsafePathsAndPreserveReplacement(t *testing.T) {
	d := daemonTestRuntime(t)
	path := filepath.Join(d.paths.Root, "daemon.json")
	if err := writePrivateJSON(path, d.record); err != nil {
		t.Fatal(err)
	}
	replacement := d.record
	replacement.Token = strings.Repeat("b", 64)
	if err := writePrivateJSON(path, replacement); err != nil {
		t.Fatal(err)
	}
	removeDaemonRecord(d.paths, &d.record)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("removed another instance's state: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readDaemonRecord(d.paths); err == nil {
			t.Fatal("accepted world-readable control token")
		}
		if err := writePrivateJSON(path, d.record); err == nil {
			t.Fatal("overwrote unsafe state file")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(path, d.record); err == nil {
		t.Fatal("overwrote directory")
	}
}

func TestDaemonStartupReadinessFailureClosesAPIListener(t *testing.T) {
	failure := errors.New("cannot save daemon state")
	address := ""
	err := serveReady(context.Background(), "127.0.0.1:0", http.NotFoundHandler(), io.Discard, io.Discard, func(addr net.Addr) error {
		address = addr.String()
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("readiness failure ignored: %v", err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("failed startup retained API port: %v", err)
	}
	listener.Close()
}
