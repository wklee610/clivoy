package knowledge

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSmallOperationalPagePreservesAllSectionsInOneHit(t *testing.T) {
	root := t.TempDir()
	body := "## 특징\n사내 발송 요청을 처리합니다.\n\n" +
		"## 이용 대상\n등록된 서버에서 사용할 수 있습니다.\n\n" +
		"## 사용 절차\n1. 발신 주소를 등록합니다.\n2. relay.example.test:2525로 연결합니다.\n\n" +
		"## 접속 정보\n| 항목 | 값 |\n| --- | --- |\n| relay | relay.example.test |\n| port | 2525 |\n\n" +
		"## 검증\n```sh\nsmtp-check --host relay.example.test --port 2525\n```\n\n" +
		"## 문의\n등록된 담당자에게 요청합니다."
	writeMD(t, root, "smtp.md", "https://wiki.example.test/smtp\n\n# 사내 SMTP\n\n"+body+"\n")
	i := testIndex(t, root)
	if i.Stats().Chunks != 1 {
		t.Fatalf("small page split: %+v", i.Stats())
	}
	hits, err := i.Search(context.Background(), "SMTP 특징", 2, 6000)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	if hits[0].Text != body {
		t.Fatalf("short page evidence changed or omitted sections:\n%s", hits[0].Text)
	}
	if hits[0].URL != "https://wiki.example.test/smtp" || hits[0].Title != "사내 SMTP" {
		t.Fatal("metadata lost")
	}
}

func TestLargePageMergesAdjacentSmallSectionsWithLabels(t *testing.T) {
	var source strings.Builder
	source.WriteString("https://wiki.example.test/procedure\n# Procedure\n")
	code := "```sh\ncheck --host relay.example.test\n```"
	table := "| key | value |\n| --- | --- |\n| port | 2525 |"
	source.WriteString("## Overview\nShort overview.\n\n## Commands\n" + code + "\n\n## Settings\n" + table + "\n")
	for n := 0; n < 35; n++ {
		source.WriteString("\n## Additional detail\n" + strings.Repeat("The procedure preserves all ordered steps. ", 5) + "\n")
	}
	chunks := parseMarkdown("procedure.md", source.String())
	if len(chunks) < 2 {
		t.Fatal("large document was not bounded")
	}
	var all strings.Builder
	for _, c := range chunks {
		if utf8.RuneCountInString(c.hit.Text) > maxChunkRunes {
			t.Fatal("merged chunk exceeded bound")
		}
		all.WriteString(c.hit.Text)
		all.WriteString("\n")
	}
	first := chunks[0].hit.Text
	for _, want := range []string{"### Overview", "### Commands", "### Settings", code, table} {
		if !strings.Contains(first, want) {
			t.Fatalf("adjacent section or block missing: %q in %s", want, first)
		}
	}
	if strings.Count(all.String(), "The procedure preserves all ordered steps.") != 35*5 {
		t.Fatal("large document text lost")
	}
}
