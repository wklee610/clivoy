//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestCLICancellationStopsDescendants(t *testing.T) {
	dir := t.TempDir()
	pidFile, heartbeat := filepath.Join(dir, "pid"), filepath.Join(dir, "heartbeat")
	t.Setenv("CLIVOY_TEST_PID", pidFile)
	t.Setenv("CLIVOY_TEST_HEARTBEAT", heartbeat)
	p := fakeCLI(t, "codex", "tree")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := p.Generate(ctx, basicRequest(), nil); result <- err }()
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(string(data))
		if _, err := os.Stat(heartbeat); err == nil && pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("fake CLI did not start its descendant")
	}
	defer func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected cancellation error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CLI did not stop")
	}
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("descendant continued running after request cancellation")
	}
}
