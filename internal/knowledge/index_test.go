package knowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func writeMD(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func testIndex(t *testing.T, root string) *Index {
	t.Helper()
	i, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return i
}

func TestKoreanSearchAndMetadata(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "infra/mail.md", "https://wiki.example.test/smtp\n\n# 사내 SMTP 메일\n\n## 발송 설정\nSMTP를 이용하는 메일서버의 포트는 2525입니다. 이메일을 보낼 때 인증이 필요합니다.\n")
	writeMD(t, root, "storage.md", "https://wiki.example.test/storage\n# 스토리지\n## 접속\n파일 저장소와 디스크 볼륨을 연결합니다.\n")
	i := testIndex(t, root)
	for _, query := range []string{"SMTP 포트", "메일은 어떻게 보내나요?", "메일서버를 알려주세요", "email"} {
		hits, err := i.Search(context.Background(), query, 5, 10000)
		if err != nil || len(hits) == 0 {
			t.Fatalf("%q: hits=%v err=%v", query, hits, err)
		}
		hit := hits[0]
		if hit.Path != "infra/mail.md" || hit.Title != "사내 SMTP 메일" || hit.Heading != "발송 설정" || hit.URL != "https://wiki.example.test/smtp" || hit.Score <= 0 {
			t.Fatalf("metadata: %#v", hit)
		}
		if strings.Contains(hit.Text, "https://wiki.example.test/smtp") || strings.Contains(hit.Text, "# 사내") {
			t.Fatalf("metadata leaked into body: %s", hit.Text)
		}
	}
	for _, query := range []string{"화성의 날씨를 알려줘", "화성 설정 방법", "설정 방법", "어떻게 하는 방법인지 알려주세요", "please tell me how", ""} {
		hits, err := i.Search(context.Background(), query, 5, 10000)
		if err != nil || len(hits) != 0 {
			t.Fatalf("unrelated %q: hits=%v err=%v", query, hits, err)
		}
	}
}

func TestCodeTablePreservedAndChunksBounded(t *testing.T) {
	code := "```sh\nprintf 'first\\nsecond\\n'\n  echo kept\n```"
	table := "| 설정 | 값 |\n| --- | --- |\n| SMTP | 2525 |\n| 인증 | 필요 |"
	body := "https://wiki.example.test/blocks\n# blocks\n## SMTP\n\n" + code + "\n\n" + table + "\n\n" + strings.Repeat("긴설명입니다. ", 800)
	chunks := parseMarkdown("blocks.md", body)
	var combined strings.Builder
	for _, c := range chunks {
		if utf8.RuneCountInString(c.hit.Text) > maxChunkRunes {
			t.Fatalf("chunk too long: %d", utf8.RuneCountInString(c.hit.Text))
		}
		combined.WriteString(c.hit.Text)
		combined.WriteString("\n")
	}
	if !strings.Contains(combined.String(), code) || !strings.Contains(combined.String(), table) {
		t.Fatal("ordinary code/table changed")
	}
	longCode := "```python\n" + strings.Repeat("print('preserved')\n", 700) + "```"
	parts := splitBlock(longCode)
	if len(parts) < 2 {
		t.Fatal("large block not split")
	}
	var restored []string
	for _, p := range parts {
		if !strings.HasPrefix(p, "```python\n") || !strings.HasSuffix(p, "\n```") || utf8.RuneCountInString(p) > maxChunkRunes {
			t.Fatalf("invalid code chunk")
		}
		restored = append(restored, strings.TrimSuffix(strings.TrimPrefix(p, "```python\n"), "\n```"))
	}
	if strings.Join(restored, "\n") != strings.TrimSuffix(strings.TrimPrefix(longCode, "```python\n"), "\n```") {
		t.Fatal("large code lost content")
	}
	longTable := "| key | value |\n| --- | --- |\n" + strings.Repeat("| key | preserved value |\n", 600)
	parts = splitBlock(strings.TrimSuffix(longTable, "\n"))
	count := 0
	for _, p := range parts {
		if !strings.HasPrefix(p, "| key | value |\n| --- | --- |\n") || utf8.RuneCountInString(p) > maxChunkRunes {
			t.Fatal("invalid table chunk")
		}
		count += strings.Count(p, "| key | preserved value |")
	}
	if count != 600 {
		t.Fatalf("table lost rows: %d", count)
	}
}

func TestExclusionsAndLimits(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeMD(t, root, "visible.md", "# visible\nneedle valid")
	writeMD(t, root, ".hidden.md", "# hidden\nneedle secret")
	writeMD(t, root, ".obsidian/hidden.md", "# hidden\nneedle secret")
	writeMD(t, root, "00_목차.md", "# contents\nsearchable table of contents")
	writeMD(t, root, "large.md", strings.Repeat("x", maxFileBytes+1))
	writeMD(t, outside, "external.md", "# external\nneedle secret")
	if err := os.Symlink(filepath.Join(outside, "external.md"), filepath.Join(root, "link.md")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink privilege unavailable")
		}
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	i := testIndex(t, root)
	stats := i.Stats()
	if stats.Documents != 2 || stats.Chunks != 2 || stats.Skipped != 5 || stats.UpdatedAt.IsZero() {
		t.Fatalf("stats=%+v", stats)
	}
	hits, err := i.Search(context.Background(), "needle", 20, 1000)
	if err != nil || len(hits) != 1 || hits[0].Path != "visible.md" {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	if _, err := New(filepath.Join(root, "linked-dir")); err == nil {
		t.Fatal("symlink root accepted")
	}
	if _, err := New(""); err == nil {
		t.Fatal("empty root accepted")
	}
}

func TestRefreshChangesAndLastGood(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "one.md", "# first\napple")
	i := testIndex(t, root)
	first := i.data.Load().docs["one.md"]
	if err := i.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := i.data.Load().docs["one.md"]
	if &first.chunks[0] != &second.chunks[0] {
		t.Fatal("unchanged document was reparsed")
	}
	writeMD(t, root, "one.md", "# first\npears") // same byte count
	writeMD(t, root, "new.md", "# new\nbanana")
	if err := i.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits, _ := i.Search(context.Background(), "apple", 5, 1000); len(hits) != 0 {
		t.Fatal("old content retained")
	}
	if hits, _ := i.Search(context.Background(), "pears", 5, 1000); len(hits) != 1 {
		t.Fatal("changed content missing")
	}
	if err := os.Remove(filepath.Join(root, "one.md")); err != nil {
		t.Fatal(err)
	}
	if err := i.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i.Stats().Documents != 1 {
		t.Fatal("deletion not reflected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := i.Stats()
	if err := i.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	if i.Stats() != before {
		t.Fatal("failed refresh published partial snapshot")
	}
	if hits, err := i.Search(ctx, "banana", 5, 1000); !errors.Is(err, context.Canceled) || hits != nil {
		t.Fatalf("canceled search=%v %v", hits, err)
	}
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(root+"-moved", root) })
	if err := i.Refresh(context.Background()); err == nil {
		t.Fatal("missing root accepted")
	}
	if hits, _ := i.Search(context.Background(), "banana", 5, 1000); len(hits) != 1 {
		t.Fatal("last good snapshot lost")
	}
}

func TestSearchBudgetStableIDsAndDiversity(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "one.md", "# SMTP\n## one\n"+strings.Repeat("SMTP 설정본문입니다 ", 150)+"\n## two\n"+strings.Repeat("SMTP 두번째 설명입니다 ", 150)+"\n## three\n"+strings.Repeat("SMTP 세번째 설명입니다 ", 150))
	writeMD(t, root, "two.md", "# SMTP backup\nSMTP other document")
	i := testIndex(t, root)
	hits, err := i.Search(context.Background(), "SMTP", 20, 10000)
	if err != nil || len(hits) != 3 {
		t.Fatalf("diversity hits=%v err=%v", hits, err)
	}
	for _, budget := range []int{1, 10, 30, 100, 3000} {
		got, err := i.Search(context.Background(), "SMTP", 20, budget)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, h := range got {
			total += utf8.RuneCountInString(h.Text)
		}
		if total > budget {
			t.Fatalf("budget=%d used=%d", budget, total)
		}
	}
	if err := i.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	newHits, _ := i.Search(context.Background(), "SMTP", 20, 10000)
	for n := range hits {
		if hits[n].ID != newHits[n].ID {
			t.Fatal("unstable IDs")
		}
	}
}

func TestConcurrentRefreshAndSearch(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "one.md", "# SMTP\n메일 발송")
	i := testIndex(t, root)
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				hits, err := i.Search(context.Background(), "메일", 5, 1000)
				if err != nil || len(hits) != 1 {
					t.Errorf("concurrent query hits=%d err=%v", len(hits), err)
					return
				}
				_ = i.Stats()
			}
		}()
	}
	for n := 0; n < 20; n++ {
		writeMD(t, root, "one.md", fmt.Sprintf("# SMTP\n메일 발송 revision %d", n))
		if err := i.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
