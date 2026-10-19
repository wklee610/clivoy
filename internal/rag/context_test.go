package rag

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wklee610/clivoy/internal/knowledge"
)

func TestContextBudgetIncludesEscapedJSONAndMetadata(t *testing.T) {
	hits := []knowledge.Hit{
		{ID: "a-1", Path: "한글/manual.md", Title: "Synthetic \"Manual\"", Heading: "<quoted>", Text: strings.Repeat("가\"\\<data>\n", 1000)},
		{ID: "b-1", Path: "excluded.md", Title: "Excluded source", Text: strings.Repeat("More context.", 100)},
	}
	for _, budget := range []int{256, 512, 2048} {
		prompt, sources := buildContext(hits, budget)
		if knowledge.EstimateTokens(prompt) > budget || !utf8.ValidString(prompt) {
			t.Fatalf("budget=%d estimated=%d valid=%v", budget, knowledge.EstimateTokens(prompt), utf8.ValidString(prompt))
		}
		var context struct {
			Documents []document `json:"documents"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(prompt, guidance)), &context); err != nil {
			t.Fatalf("invalid JSON after truncation: %v", err)
		}
		if len(context.Documents) != 1 || len(sources) != 1 || context.Documents[0].Text == "" || !utf8.ValidString(context.Documents[0].Text) {
			t.Fatalf("budget=%d unexpected documents=%+v sources=%+v", budget, context.Documents, sources)
		}
		if strings.Contains(sourceFooter(sources, "question"), "Excluded") {
			t.Fatal("source excluded by context budget appears in footer")
		}
	}
}

func TestContextSourcesDeduplicateOnlyIncludedDocuments(t *testing.T) {
	hits := []knowledge.Hit{
		{ID: "too-large", Path: strings.Repeat("long/", 5000), Title: "Excluded", Text: "Cannot fit metadata."},
		{ID: "a-1", Path: "a.md", Title: "First", Text: "First chunk."},
		{ID: "a-2", Path: "a.md", Title: "First", Text: "Second chunk."},
		{ID: "b-1", Path: "b.md", Title: "Second", Text: "Third chunk."},
	}
	prompt, sources := buildContext(hits, 512)
	if len(sources) != 2 || sources[0].Path != "a.md" || sources[1].Path != "b.md" {
		t.Fatalf("unexpected sources: %+v", sources)
	}
	var context struct {
		Documents []document `json:"documents"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(prompt, guidance)), &context); err != nil {
		t.Fatal(err)
	}
	if len(context.Documents) != 3 || context.Documents[0].Source != 1 || context.Documents[1].Source != 1 || context.Documents[2].Source != 2 {
		t.Fatalf("citation numbers do not match included sources: %+v", context.Documents)
	}
	if got := sourceFooter(sources, "질문"); got != "\n\n출처:\n- [1] First — a.md\n- [2] Second — b.md" {
		t.Fatalf("unexpected footer: %q", got)
	}
}

func TestSourceLinksAreSafeMarkdownAndHTTPOnly(t *testing.T) {
	for _, value := range []string{
		"javascript:alert(1)", "file:///etc/passwd", "//example.test/path", "https://user:password@example.test/path", "https://example.test/line\nbreak", "https:///missing-host",
	} {
		if got := safeURL(value); got != "" {
			t.Fatalf("unsafe URL %q became %q", value, got)
		}
	}
	if got := safeURL("https://example.test/?q=one\u2028two"); strings.ContainsRune(got, '\u2028') || !strings.Contains(got, "%E2%80%A8") {
		t.Fatalf("Unicode line separator was not escaped: %q", got)
	}
	hits := []knowledge.Hit{
		{ID: "a", Path: "manual.md", Title: "[Click](javascript:alert(1))\n<script>*bold*", URL: "https://example.test/a)b<c>\\d?q=\"quoted\"", Text: "Synthetic context."},
		{ID: "b", Path: "unsafe[link].md", Title: "Unsafe URL", URL: "javascript:alert(1)", Text: "Synthetic context."},
	}
	_, sources := buildContext(hits, 2048)
	footer := sourceFooter(sources, "question")
	if strings.Contains(footer, "<script>") || strings.Contains(footer, "[Click](javascript:") || strings.Contains(footer, ")b<c>") || strings.Count(footer, "\n") != 4 {
		t.Fatalf("unsafe source footer: %q", footer)
	}
	if !strings.Contains(footer, "https://example.test/a%29b%3Cc%3E%5Cd?q=%22quoted%22") || !strings.Contains(footer, "Unsafe URL — unsafe\\[link\\].md") {
		t.Fatalf("escaped URL or path fallback missing: %q", footer)
	}
}

func TestRepeatedSourceURLHasOneFooterEntry(t *testing.T) {
	hits := []knowledge.Hit{
		{ID: "a-1", Path: "first.md", Title: "First", URL: "https://example.test/manual", Text: "First context."},
		{ID: "b-1", Path: "copy.md", Title: "Copy", URL: "https://example.test/manual", Text: "Second context."},
	}
	prompt, sources := buildContext(hits, 2048)
	if len(sources) != 1 || sources[0].Title != "First" || strings.Count(prompt, `"source":1`) != 2 {
		t.Fatalf("repeated source was not deduplicated: sources=%+v prompt=%q", sources, prompt)
	}
}

func TestNoContextCanExceedBudgetThroughMetadata(t *testing.T) {
	hits := []knowledge.Hit{{ID: "oversize", Path: "manual.md", Title: strings.Repeat("\"", 4000), Text: "Short body."}}
	prompt, sources := buildContext(hits, 256)
	if len(sources) != 0 || knowledge.EstimateTokens(prompt) > 256 {
		t.Fatalf("oversized metadata accepted: sources=%+v tokens=%d", sources, knowledge.EstimateTokens(prompt))
	}
}
