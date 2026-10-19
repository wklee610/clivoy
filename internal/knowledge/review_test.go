package knowledge_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wklee610/clivoy/internal/knowledge"
)

func TestReviewListAndQuotedCodeRemainEvidence(t *testing.T) {
	for _, block := range []string{"- ```bash\n  # sourcecommentmarker\n  echo sourcecommentmarker\n  ```", "1. ```bash\n   # sourcecommentmarker\n   echo sourcecommentmarker\n   ```", "> ```bash\n> # sourcecommentmarker\n> echo sourcecommentmarker\n> ```"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "code.md"), []byte("https://wiki.example.test/code\n\n# Real title\n\n"+block+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		idx, err := knowledge.New(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = idx.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		hits, err := idx.Search(context.Background(), "sourcecommentmarker", 4, 3000)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || !strings.Contains(hits[0].Text, "# sourcecommentmarker") || !strings.Contains(hits[0].Text, "echo sourcecommentmarker") || hits[0].Title != "Real title" || strings.Contains(hits[0].Heading, "sourcecommentmarker") {
			t.Errorf("code comment became heading or lost: %#v", hits)
		}
	}
}

func TestReviewRuneBudgetAndStableIDs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "smtp.md"), []byte("https://wiki.example.test/smtp\n\n# SMTP\n\nSMTP 서버에 연결합니다. "+strings.Repeat("한글본문 ", 500)), 0600); err != nil {
		t.Fatal(err)
	}
	idx, err := knowledge.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = idx.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := idx.Search(context.Background(), "SMTP", 5, 73)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("SMTP evidence absent")
	}
	count := 0
	for _, h := range first {
		count += utf8.RuneCountInString(h.Text)
		if !utf8.ValidString(h.Text) {
			t.Error("split invalid UTF8")
		}
	}
	if count > 73 {
		t.Errorf("rune budget exceeded: %d", count)
	}
	if err = idx.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := idx.Search(context.Background(), "SMTP", 5, 73)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) || first[0].ID != second[0].ID || first[0].Text != second[0].Text {
		t.Error("unchanged refresh altered citation identity or content")
	}
}
