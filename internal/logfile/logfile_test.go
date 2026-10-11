package logfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestAppendAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clivoy.log")
	for _, text := range []string{"first\n", "second\n"} {
		w, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := w.Write([]byte(text)); err != nil || n != len(text) {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		if _, err := w.Write([]byte("closed")); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("write after Close: %v", err)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "first\nsecond\n" {
		t.Fatalf("content = %q, %v", content, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
}

func TestRejectUnsafeExistingFiles(t *testing.T) {
	for _, name := range []string{"clivoy.log", "clivoy.log.1", "clivoy.log.3"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if w, err := Open(filepath.Join(dir, "clivoy.log")); err == nil {
				_ = w.Close()
				t.Fatal("accepted a directory as a current log or backup")
			}
		})
	}
	if runtime.GOOS == "windows" {
		return // POSIX permission bits do not describe Windows ACLs.
	}
	for _, suffix := range []string{"", ".1"} {
		t.Run("public_permissions"+suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "clivoy.log")
			if err := os.WriteFile(path+suffix, []byte("private log"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path+suffix, 0640); err != nil {
				t.Fatal(err)
			}
			if w, err := Open(path); err == nil {
				_ = w.Close()
				t.Fatal("accepted group-readable file")
			}
			content, err := os.ReadFile(path + suffix)
			if err != nil || string(content) != "private log" {
				t.Fatalf("existing file changed: %q, %v", content, err)
			}
		})
	}
}

func TestRejectSymlinksWithoutChangingTargets(t *testing.T) {
	for _, suffix := range []string{"", ".1", ".3"} {
		t.Run("suffix"+suffix, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "untouched")
			if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "clivoy.log")
			if err := os.Symlink(target, path+suffix); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("creating symlinks requires Windows privileges: %v", err)
				}
				t.Fatal(err)
			}
			if w, err := Open(path); err == nil {
				_ = w.Close()
				t.Fatal("accepted symlink")
			}
			content, err := os.ReadFile(target)
			if err != nil || string(content) != "original" {
				t.Fatalf("symlink target changed: %q, %v", content, err)
			}
		})
	}
}

func TestRuntimeRotationAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clivoy.log")
	w, err := open(path, 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, record := range []string{"aaaaaa\n", "bbbbbb\n", "cccccc\n", "dddddd\n", "eeeeee\n"} {
		if _, err := w.Write([]byte(record)); err != nil {
			t.Fatal(err)
		}
	}
	for suffix, expected := range map[string]string{
		"": "eeeeee\n", ".1": "dddddd\n", ".2": "cccccc\n", ".3": "bbbbbb\n",
	} {
		content, err := os.ReadFile(path + suffix)
		if err != nil || string(content) != expected {
			t.Fatalf("%s = %q, want %q; %v", suffix, content, expected, err)
		}
		if runtime.GOOS != "windows" {
			info, _ := os.Stat(path + suffix)
			if info.Mode().Perm() != 0600 {
				t.Fatalf("%s permissions = %o", suffix, info.Mode().Perm())
			}
		}
	}
	if _, err := os.Stat(path + ".4"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected fourth backup: %v", err)
	}
}

func TestLargeWriteStaysBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clivoy.log")
	w, err := open(path, 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	input := []byte("1111111122222222333333334444444455555555")
	if n, err := w.Write(input); err != nil || n != len(input) {
		t.Fatalf("large Write = %d, %v", n, err)
	}
	var retained []byte
	for _, suffix := range []string{".3", ".2", ".1", ""} {
		content, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if len(content) > 8 {
			t.Fatalf("file exceeded bound: %d", len(content))
		}
		retained = append(retained, content...)
	}
	if !bytes.Equal(retained, input[8:]) {
		t.Fatalf("retained = %q", retained)
	}
}

func TestConcurrentWritesKeepCompleteRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clivoy.log")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for index := 0; index < 50; index++ {
				if _, err := fmt.Fprintf(w, "%02d:%02d\n", worker, index); err != nil {
					t.Errorf("concurrent write: %v", err)
					return
				}
			}
		}(worker)
	}
	workers.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	counts := map[string]int{}
	for _, line := range lines {
		counts[line]++
	}
	if len(lines) != 800 || len(counts) != 800 {
		t.Fatalf("lost or merged records: %d lines, %d unique", len(lines), len(counts))
	}
	for worker := 0; worker < 16; worker++ {
		for index := 0; index < 50; index++ {
			if counts[fmt.Sprintf("%02d:%02d", worker, index)] != 1 {
				t.Fatalf("missing or duplicated record %d:%d", worker, index)
			}
		}
	}
}

func TestRotationRejectsUnsafeBackupInsertedWhileOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clivoy.log")
	w, err := open(path, 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".2", 0700); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("new")); err == nil || n != 0 {
		t.Fatalf("rotation with unsafe backup = %d, %v", n, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "original" {
		t.Fatalf("current log changed after failed rotation: %q, %v", content, err)
	}
	if _, err := w.Write([]byte("later")); err == nil {
		t.Fatal("accepted data after rotation failure")
	}
}

func TestParentMustAlreadyExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "clivoy.log")
	if w, err := Open(path); err == nil {
		_ = w.Close()
		t.Fatal("created a missing parent directory")
	}
}
