//go:build darwin || linux || windows

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLockContentionAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	want := []byte("persistent contents must not be truncated")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	if second, err := TryLock(path); !errors.Is(err, ErrLocked) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second lock = %v, want ErrLocked", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := first.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	wg.Wait()
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("Close replaced/removed lock file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("lock changed contents: %q, %v", got, err)
	}
	second, err := TryLock(path)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	second.Close()
}

func TestLockCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	l, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("created file: %v, %v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("file mode = %v", info.Mode())
	}
}

func TestLockRejectsSymlinkAndDirectory(t *testing.T) {
	dir := t.TempDir()
	if l, err := TryLock(dir); err == nil {
		l.Close()
		t.Fatal("accepted directory")
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if l, err := TryLock(link); err == nil {
		l.Close()
		t.Fatal("accepted symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("changed symlink target: %q, %v", data, err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if l, err := TryLock(link); err == nil {
		l.Close()
		t.Fatal("accepted dangling symlink")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created dangling symlink target: %v", err)
	}
}

func TestLockCrossProcessAndExitRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonHelperProcess$")
	cmd.Env = helperEnv("lock", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	// CommandContext bounds this read even if the helper fails unexpectedly.
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		cancel()
		cmd.Wait()
		t.Fatalf("helper readiness: %q, %v", line, err)
	}
	if l, err := TryLock(path); !errors.Is(err, ErrLocked) {
		if l != nil {
			l.Close()
		}
		stdin.Close()
		cmd.Wait()
		t.Fatalf("cross-process lock = %v, want ErrLocked", err)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	// The helper deliberately exits without Close. Windows may defer OS lock
	// cleanup briefly after process exit, so allow a bounded retry.
	deadline := time.Now().Add(3 * time.Second)
	for {
		l, err := TryLock(path)
		if err == nil {
			l.Close()
			break
		}
		if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			t.Fatalf("lock not released on exit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type launchReport struct {
	PID      int
	Dir      string
	Env      string
	Argument string
	StdinEOF bool
}

func TestLaunchDetached(t *testing.T) {
	dir := t.TempDir()
	// Launch from a short-lived helper so the test verifies that the worker
	// survives the launcher's exit, and leaves no unreaped direct child.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonHelperProcess$")
	cmd.Env = helperEnv("launcher", dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher: %v: %s", err, output)
	}
	var pid int
	if err := json.Unmarshal(output, &pid); err != nil || pid <= 0 {
		t.Fatalf("launcher output should contain only PID: %q, %v", output, err)
	}
	t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "finish"), nil, 0o600) })
	data := waitForFile(t, filepath.Join(dir, "ready"))
	var report launchReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotDir, err := filepath.EvalSymlinks(report.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.PID != pid || gotDir != wantDir || report.Env != "inherited test value" || report.Argument != "argument with spaces" || !report.StdinEOF {
		t.Fatalf("unexpected launch report: %+v", report)
	}
	checkDetachedProcess(t, pid)
	if err := os.WriteFile(filepath.Join(dir, "finish"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, filepath.Join(dir, "done"))
}

func TestLaunchInvalidExecutable(t *testing.T) {
	pid, err := Launch(filepath.Join(t.TempDir(), "missing-executable"), nil, nil, "")
	if err == nil || pid != 0 {
		t.Fatalf("Launch = %d, %v", pid, err)
	}
}

func helperEnv(mode, path string) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CLIVOY_DAEMON_TEST_") {
			env = append(env, entry)
		}
	}
	return append(env, "CLIVOY_DAEMON_TEST_MODE="+mode, "CLIVOY_DAEMON_TEST_PATH="+path, "CLIVOY_DAEMON_TEST_VALUE=inherited test value")
}

func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("wait for %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Helpers use os.Exit to bypass the testing runner and exercise process-exit
// cleanup without an explicit lock Close or inherited test output.
func TestDaemonHelperProcess(t *testing.T) {
	mode, path := os.Getenv("CLIVOY_DAEMON_TEST_MODE"), os.Getenv("CLIVOY_DAEMON_TEST_PATH")
	if mode == "" {
		return
	}
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch mode {
	case "lock":
		lock, err := TryLock(path)
		if err != nil {
			fail(err)
		}
		fmt.Fprintln(os.Stdout, "locked")
		io.Copy(io.Discard, os.Stdin)
		runtime.KeepAlive(lock)
	case "launcher":
		pid, err := Launch(os.Args[0], []string{"-test.run=^TestDaemonHelperProcess$", "--", "argument with spaces"}, helperEnv("worker", path), path)
		if err != nil {
			fail(err)
		}
		fmt.Fprintln(os.Stdout, pid)
	case "worker":
		cwd, err := os.Getwd()
		if err != nil {
			fail(err)
		}
		buf := make([]byte, 1)
		n, err := os.Stdin.Read(buf)
		report := launchReport{PID: os.Getpid(), Dir: cwd, Env: os.Getenv("CLIVOY_DAEMON_TEST_VALUE"), Argument: os.Args[len(os.Args)-1], StdinEOF: n == 0 && errors.Is(err, io.EOF)}
		// These must reach DevNull, not the launcher's captured output.
		fmt.Fprintln(os.Stdout, "worker stdout")
		fmt.Fprintln(os.Stderr, "worker stderr")
		data, _ := json.Marshal(report)
		if err := os.WriteFile(filepath.Join(path, "ready.tmp"), data, 0o600); err != nil {
			fail(err)
		}
		if err := os.Rename(filepath.Join(path, "ready.tmp"), filepath.Join(path, "ready")); err != nil {
			fail(err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(path, "finish")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(2)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(filepath.Join(path, "done"), nil, 0o600); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown helper mode %q", mode))
	}
	os.Exit(0)
}
