//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNamedPipeMarkdownIsSkippedWithoutOpening(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.md"), 0600); err != nil {
		t.Fatal(err)
	}
	writeMD(t, root, "visible.md", "# Visible\nneedle")
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if index.Stats().Documents != 1 || index.Stats().Skipped != 1 {
		t.Fatalf("stats = %+v", index.Stats())
	}
}

func TestRootedReaderDoesNotFollowExternalSymlink(t *testing.T) {
	rootPath, outside := t.TempDir(), t.TempDir()
	writeMD(t, outside, "external.md", "# External\nprivate marker")
	if err := os.Symlink(filepath.Join(outside, "external.md"), filepath.Join(rootPath, "link.md")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	content, skip, err := readFile(context.Background(), root, "link.md")
	if err != nil || !skip || len(content) != 0 {
		t.Fatalf("symlink was read: %q, %v, %v", content, skip, err)
	}
	if f, err := openRegular(root, "link.md"); err == nil {
		f.Close()
		t.Fatal("rooted open followed external symlink")
	}
}
