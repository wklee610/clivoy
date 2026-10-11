package state

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestResolveOverrideDoesNotCreateFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created", "clivoy")
	t.Setenv("CLIVOY_HOME", root)
	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{
		Root: root, Config: filepath.Join(root, "clivoy.json"),
		Auth: filepath.Join(root, "auth.json"), Logs: filepath.Join(root, "logs"),
		Log: filepath.Join(root, "logs", "clivoy.log"),
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%+v want=%+v", paths, want)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("Resolve changed the filesystem: %v", err)
	}
}

func TestResolveUsesOperatingSystemUserHome(t *testing.T) {
	// Read the real home location without creating anything there. This also
	// exercises os.UserHomeDir's USERPROFILE handling on native Windows CI.
	t.Setenv("CLIVOY_HOME", "")
	if err := os.Unsetenv("CLIVOY_HOME"); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no operating-system home configured: %v", err)
	}
	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if paths.Root != filepath.Join(home, ".clivoy") {
		t.Fatalf("root=%q home=%q", paths.Root, home)
	}
}

func TestResolveRejectsNonAbsoluteOverride(t *testing.T) {
	for _, value := range []string{"", ".clivoy", "../clivoy", "~/clivoy"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CLIVOY_HOME", value)
			if _, err := Resolve(); err == nil || !strings.Contains(err.Error(), "absolute") {
				t.Fatalf("accepted override %q: %v", value, err)
			}
		})
	}
}

func TestEnsureCreatesOnlyPrivateDirectoriesAndPreservesFiles(t *testing.T) {
	t.Setenv("CLIVOY_HOME", filepath.Join(t.TempDir(), "nested", "clivoy"))
	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.Root, paths.Logs} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory %q: %v", path, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
			t.Fatalf("directory %q permissions=%o", path, info.Mode().Perm())
		}
	}
	if _, err := os.Lstat(filepath.Join(paths.Root, "tmp")); !os.IsNotExist(err) {
		t.Fatalf("Ensure created a persistent temporary directory: %v", err)
	}
	for _, path := range []string{paths.Config, paths.Auth, paths.Log} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("Ensure created file %q: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("preserve-existing-content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.Config, paths.Auth, paths.Log} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "preserve-existing-content" {
			t.Fatalf("Ensure changed file %q: %v", path, err)
		}
	}
}

func TestEnsureRejectsSymlinksAndFiles(t *testing.T) {
	for _, field := range []string{"root", "logs"} {
		for _, kind := range []string{"file", "symlink"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				t.Setenv("CLIVOY_HOME", filepath.Join(t.TempDir(), "clivoy"))
				paths, err := Resolve()
				if err != nil {
					t.Fatal(err)
				}
				path := paths.Root
				if field != "root" {
					if err := os.Mkdir(paths.Root, 0700); err != nil {
						t.Fatal(err)
					}
					path = paths.Logs
				}
				if kind == "file" {
					if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				if err := Ensure(paths); err == nil {
					t.Fatalf("accepted %s at %s", kind, field)
				}
				if kind == "file" {
					if data, err := os.ReadFile(path); err != nil || string(data) != "preserve" {
						t.Fatal("existing file changed")
					}
				} else if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("existing symlink changed")
				}
			})
		}
	}
}

func TestEnsureRejectsBroadPermissionsWithoutChangingThem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory access uses ACLs")
	}
	for _, field := range []string{"root", "logs"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("CLIVOY_HOME", filepath.Join(t.TempDir(), "clivoy"))
			paths, err := Resolve()
			if err != nil {
				t.Fatal(err)
			}
			if err := Ensure(paths); err != nil {
				t.Fatal(err)
			}
			path := paths.Root
			if field == "logs" {
				path = paths.Logs
			}
			if err := os.Chmod(path, 0750); err != nil {
				t.Fatal(err)
			}
			if err := Ensure(paths); err == nil || !strings.Contains(err.Error(), "chmod 700") {
				t.Fatalf("expected permissions guidance: %v", err)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0750 {
				t.Fatal("existing permissions changed")
			}
		})
	}
}
