package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEstimateTokens(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"", 0}, {"a", 1}, {"abc", 1}, {"abcd", 2}, {"abcdef", 2},
		{"한글", 2}, {"한글a", 3}, {"😀", 2}, {"a\n한", 2},
	} {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestRefreshResourceLimitsKeepLastGoodSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit func(*indexLimits)
		add   func(*testing.T, string)
	}{
		{"files", func(l *indexLimits) { l.files = 1 }, func(t *testing.T, root string) { writeMD(t, root, "second.md", "# second\nbanana") }},
		{"bytes", func(l *indexLimits) { l.bytes = 30 }, func(t *testing.T, root string) {
			writeMD(t, root, "second.md", "# second\n"+strings.Repeat("banana ", 10))
		}},
		{"chunks", func(l *indexLimits) { l.chunks = 1 }, func(t *testing.T, root string) { writeMD(t, root, "second.md", "# second\nbanana") }},
		{"entries", func(l *indexLimits) { l.entries = 1 }, func(t *testing.T, root string) { writeMD(t, root, "ignored.txt", "not Markdown") }},
		{"depth", func(l *indexLimits) { l.depth = 0 }, func(t *testing.T, root string) { writeMD(t, root, "nested/second.md", "# second\nbanana") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeMD(t, root, "first.md", "# First\napple")
			index := testIndex(t, root)
			before := index.data.Load()
			tc.limit(&index.limits)
			tc.add(t, root)
			if err := index.Refresh(context.Background()); !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("expected resource error, got %v", err)
			}
			if index.data.Load() != before {
				t.Fatal("resource failure replaced last good snapshot")
			}
			hits, err := index.Search(context.Background(), "apple", 2, 100)
			if err != nil || len(hits) != 1 || hits[0].Path != "first.md" {
				t.Fatalf("last good snapshot unavailable: %v, %v", hits, err)
			}
		})
	}
}

func TestInitialRefreshFailureStaysNotReadyAndCanRecover(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "first.md", "# First\napple")
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	index.limits.bytes = 1
	if err := index.Refresh(context.Background()); !errors.Is(err, ErrResourceLimit) {
		t.Fatal(err)
	}
	if _, err := index.Search(context.Background(), "apple", 2, 100); !errors.Is(err, ErrNotReady) {
		t.Fatalf("uninitialized search: %v", err)
	}
	if !index.Stats().UpdatedAt.IsZero() {
		t.Fatal("failed initial refresh became ready")
	}
	index.limits.bytes = MaxTotalBytes
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if index.Stats().Documents != 1 {
		t.Fatal("index did not recover")
	}
}

func TestRefreshCancellationWhileWaitingForAnotherRefresh(t *testing.T) {
	index := testIndex(t, t.TempDir())
	before := index.data.Load()
	index.refreshLock <- struct{}{}
	defer func() { <-index.refreshLock }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- index.Refresh(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled refresh waited for another refresh to finish")
	}
	if index.data.Load() != before {
		t.Fatal("cancellation replaced snapshot")
	}
}

func TestMarkdownParsingIsBoundedAndCancelable(t *testing.T) {
	text := "# Topic\n" + strings.Repeat("searchable detailed sentence. ", 800)
	if _, err := parseMarkdownContext(context.Background(), "topic.md", text, 1); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("chunk limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseMarkdownContext(ctx, "topic.md", text, MaxChunks); !errors.Is(err, context.Canceled) {
		t.Fatalf("parse cancellation: %v", err)
	}
	text = "# " + strings.Repeat("Title ", 500) + "\n## " + strings.Repeat("Heading ", 500) + "\n" + text
	chunks, err := parseMarkdownContext(context.Background(), "topic.md", text, MaxChunks)
	if err != nil || len(chunks) == 0 {
		t.Fatalf("bounded metadata: %v", err)
	}
	for _, c := range chunks {
		if utf8.RuneCountInString(c.hit.Title) > maxTitleRunes || utf8.RuneCountInString(c.hit.Heading) > maxHeadingRunes || utf8.RuneCountInString(c.hit.Text) > maxChunkRunes {
			t.Fatal("metadata or text exceeded bound")
		}
	}
}

func TestGenericIndexFilenameAndInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "00_목차.md", "# 목차\nsearchable contents")
	writeMD(t, root, "INDEX.MD", "# Index\nsearchable navigation")
	if err := os.WriteFile(filepath.Join(root, "invalid.md"), []byte{'x', 0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	index := testIndex(t, root)
	if index.Stats().Documents != 2 || index.Stats().Skipped != 1 {
		t.Fatalf("stats = %+v", index.Stats())
	}
	hits, err := index.Search(context.Background(), "searchable", 3, 1000)
	if err != nil || len(hits) != 2 {
		t.Fatalf("ordinary index filenames excluded: %v, %v", hits, err)
	}
}
