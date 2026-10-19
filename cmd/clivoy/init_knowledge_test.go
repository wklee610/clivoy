package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/wklee610/clivoy/internal/config"
)

func TestInitCreatesDedicatedKnowledgeDirectory(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "custom"}[custom], func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "clivoy")
			t.Setenv("CLIVOY_HOME", home)
			path := filepath.Join(home, "clivoy.json")
			args := []string{"init"}
			if custom {
				path = filepath.Join(t.TempDir(), "custom.json")
				args = append(args, "--output", path)
			}
			var out bytes.Buffer
			if err := run(args, &out, &out); err != nil {
				t.Fatal(err)
			}
			c, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			want := config.DefaultKnowledgeBase()
			want.Root = "knowledge/default"
			if len(c.KnowledgeBases) != 1 || c.KnowledgeBases["default"] != want {
				t.Fatalf("initial knowledge settings = %#v", c.KnowledgeBases)
			}
			if m := c.Models["infra"]; m.Provider != "codex" || m.Knowledge != "default" || !reflect.DeepEqual(m.Capabilities, []string{"chat"}) {
				t.Fatalf("initial infra model = %#v", m)
			}
			for name, model := range config.Default().Models {
				if !reflect.DeepEqual(c.Models[name], model) {
					t.Fatalf("init changed the existing %s model", name)
				}
			}
			t.Chdir(t.TempDir())
			root, err := c.ResolveKnowledgeRoot("default")
			if err != nil || root != filepath.Join(filepath.Dir(path), "knowledge", "default") {
				t.Fatalf("initial knowledge root = %q, %v", root, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("knowledge directory must start empty: %v, %v", entries, err)
			}
			for _, dir := range []string{filepath.Dir(root), root} {
				info, err := os.Stat(dir)
				if err != nil || !info.IsDir() {
					t.Fatalf("missing knowledge directory: %v", err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
					t.Fatalf("knowledge directory permissions = %s", info.Mode())
				}
			}
		})
	}
}

func TestInitPreservesExistingKnowledgeData(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "knowledge", "default")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(root, "note.md")
	if err := os.WriteFile(note, []byte("existing document"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"init", "--output", filepath.Join(dir, "clivoy.json")}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(note); err != nil || string(data) != "existing document" {
		t.Fatal("initialization changed an existing document")
	}
}

func TestInitRollsBackFilesWhenKnowledgeDirectoryIsUnavailable(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			knowledge := filepath.Join(dir, "knowledge")
			if kind == "file" {
				if err := os.WriteFile(knowledge, []byte("do not change"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(t.TempDir(), knowledge); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			before, err := os.Lstat(knowledge)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := run([]string{"init", "--output", filepath.Join(dir, "clivoy.json")}, &out, &out); err == nil {
				t.Fatal("init accepted an unavailable knowledge directory")
			}
			for _, name := range []string{"clivoy.json", "auth.json"} {
				if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("failed init left %s behind: %v", name, err)
				}
			}
			after, err := os.Lstat(knowledge)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("init removed or replaced a pre-existing knowledge path")
			}
		})
	}
}

func TestKnowledgeDirectoryRollbackPreservesDataAndReplacements(t *testing.T) {
	for _, kind := range []string{"empty", "data", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			rollback, err := createInitialKnowledgeDirectories(dir)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "knowledge", "default")
			if kind == "data" {
				if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("new document"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "replacement" {
				if err := os.Rename(root, root+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			rollback()
			_, err = os.Stat(root)
			if kind == "empty" {
				if !os.IsNotExist(err) {
					t.Fatalf("rollback left new empty directories: %v", err)
				}
			} else if err != nil {
				t.Fatalf("rollback removed new data or a replacement directory: %v", err)
			}
		})
	}
}
