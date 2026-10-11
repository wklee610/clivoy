//go:build darwin || linux

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func checkDetachedProcess(t *testing.T, pid int) {
	t.Helper()
	group, err := syscall.Getpgid(pid)
	if err != nil || group != pid {
		t.Fatalf("detached process group = %d, want %d: %v", group, pid, err)
	}
}

func TestLockRejectsUnsafePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o620, 0o601} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if l, err := TryLock(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			if l != nil {
				l.Close()
			}
			t.Fatalf("mode %o: expected actionable private-mode error, got %v", mode, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("existing permissions changed: %v, %v", info, err)
		}
	}
}

func TestLockRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := TryLock(path); err == nil {
		l.Close()
		t.Fatal("accepted FIFO")
	}
}
