package knowledge

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

const targetChunkRunes = 1800
const maxChunkRunes = 3600
const maxTitleRunes = 256
const maxHeadingRunes = 1024
const maxURLBytes = 4096

func parseMarkdown(filePath, content string) []chunk {
	chunks, _ := parseMarkdownContext(context.Background(), filePath, content, MaxChunks)
	return chunks
}

func parseMarkdownContext(ctx context.Context, filePath, content string, limit int) ([]chunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content = strings.ReplaceAll(strings.TrimPrefix(content, "\ufeff"), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	sourceURL := ""
	if len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		if u, err := url.Parse(first); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && len(first) <= maxURLBytes && !strings.ContainsAny(first, " \t") {
			sourceURL = first
			lines = lines[1:]
		}
	}
	title := strings.TrimSuffix(path.Base(filePath), path.Ext(filePath))
	// Locate the first H1 outside a fenced block before constructing chunks.
	titleLine := -1
	fenceChar, fenceLen := byte(0), 0
	for n, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if fenceChar != 0 {
			if closesFence(line, fenceChar, fenceLen) {
				fenceChar = 0
			}
			continue
		}
		if c, l := fence(line); l != 0 {
			fenceChar, fenceLen = c, l
			continue
		}
		if level, text := heading(line); level == 1 {
			title, titleLine = trimRunes(text, maxTitleRunes), n
			break
		}
	}
	// Small operational pages should remain complete evidence. Splitting a
	// one-page procedure at every heading can retrieve its overview while losing
	// the actual endpoint or command, especially under a per-document hit limit.
	bodyLines := make([]string, 0, len(lines))
	var sectionNames []string
	seenSection := make(map[string]bool)
	fenceChar, fenceLen = 0, 0
	for n, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if n == titleLine {
			continue
		}
		bodyLines = append(bodyLines, line)
		if fenceChar != 0 {
			if closesFence(line, fenceChar, fenceLen) {
				fenceChar = 0
			}
			continue
		}
		if c, l := fence(line); l != 0 {
			fenceChar, fenceLen = c, l
			continue
		}
		if level, text := heading(line); level > 0 && !seenSection[text] {
			sectionNames = append(sectionNames, text)
			seenSection[text] = true
		}
	}
	body := strings.TrimSpace(strings.Join(bodyLines, "\n"))
	if utf8.RuneCountInString(body) <= maxChunkRunes {
		if body == "" {
			return nil, nil
		}
		if limit < 1 {
			return nil, fmt.Errorf("%w: Markdown chunks", ErrResourceLimit)
		}
		return []chunk{makeChunk(filePath, title, strings.Join(sectionNames, " · "), sourceURL, body)}, nil
	}
	var chunks []chunk
	var parseErr error
	var headings [6]string
	var pending []string
	section := func() string {
		var parts []string
		for _, h := range headings {
			if h != "" {
				parts = append(parts, h)
			}
		}
		return strings.Join(parts, " > ")
	}
	flush := func() {
		if parseErr != nil {
			return
		}
		if err := ctx.Err(); err != nil {
			parseErr = err
			return
		}
		text := strings.TrimSpace(strings.Join(pending, "\n\n"))
		if text != "" {
			if len(chunks) >= limit {
				parseErr = fmt.Errorf("%w: Markdown chunks", ErrResourceLimit)
				return
			}
			chunks = append(chunks, makeChunk(filePath, title, section(), sourceURL, text))
		}
		pending = nil
	}
	add := func(block string) {
		for _, part := range splitBlock(block) {
			current := utf8.RuneCountInString(strings.Join(pending, "\n\n"))
			if len(pending) > 0 && current+2+utf8.RuneCountInString(part) > targetChunkRunes {
				flush()
				if parseErr != nil {
					return
				}
			}
			pending = append(pending, part)
		}
	}
	for n := 0; n < len(lines); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if parseErr != nil {
			return nil, parseErr
		}
		line := lines[n]
		if n == titleLine {
			n++
			continue
		}
		if strings.TrimSpace(line) == "" {
			n++
			continue
		}
		if level, text := heading(line); level > 0 {
			flush()
			headings[level-1] = trimRunes(text, maxTitleRunes)
			for d := level; d < len(headings); d++ {
				headings[d] = ""
			}
			n++
			continue
		}
		start := n
		if c, l := fence(line); l != 0 {
			n++
			for n < len(lines) {
				closed := closesFence(lines[n], c, l)
				n++
				if closed {
					break
				}
			}
		} else {
			n++
			for n < len(lines) && strings.TrimSpace(lines[n]) != "" {
				if level, _ := heading(lines[n]); level > 0 {
					break
				}
				if _, l := fence(lines[n]); l != 0 {
					break
				}
				n++
			}
		}
		add(strings.Join(lines[start:n], "\n"))
	}
	flush()
	if parseErr != nil {
		return nil, parseErr
	}
	return mergeSmallSections(chunks), nil
}

// Merge adjacent small sections without changing code/table blocks. Reinsert
// each source section's name in the combined body so its scope stays explicit.
func mergeSmallSections(chunks []chunk) []chunk {
	var result []chunk
	var group []chunk
	renderedRunes := 0
	sectionText := func(c chunk) string {
		if c.hit.Heading == "" {
			return c.hit.Text
		}
		return "### " + c.hit.Heading + "\n\n" + c.hit.Text
	}
	flush := func() {
		if len(group) == 0 {
			return
		}
		if len(group) == 1 {
			result = append(result, group[0])
		} else {
			var texts, headings []string
			seen := make(map[string]bool)
			for _, c := range group {
				texts = append(texts, sectionText(c))
				if c.hit.Heading != "" && !seen[c.hit.Heading] {
					headings = append(headings, c.hit.Heading)
					seen[c.hit.Heading] = true
				}
			}
			first := group[0].hit
			result = append(result, makeChunk(first.Path, first.Title, strings.Join(headings, " · "), first.URL, strings.Join(texts, "\n\n")))
		}
		group = nil
		renderedRunes = 0
	}
	for _, c := range chunks {
		size := utf8.RuneCountInString(sectionText(c))
		if len(group) > 0 && renderedRunes+2+size > targetChunkRunes {
			flush()
		}
		if len(group) > 0 {
			renderedRunes += 2
		}
		group = append(group, c)
		renderedRunes += size
	}
	flush()
	return result
}

func heading(line string) (int, string) {
	if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
		return 0, ""
	}
	line = strings.TrimLeft(line, " ")
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n < 1 || n > 6 || n == len(line) || (line[n] != ' ' && line[n] != '\t') {
		return 0, ""
	}
	text := strings.TrimSpace(line[n:])
	end := len(text)
	for end > 0 && text[end-1] == '#' {
		end--
	}
	if end > 0 && end < len(text) && (text[end-1] == ' ' || text[end-1] == '\t') {
		text = strings.TrimSpace(text[:end])
	}
	return n, text
}

func fence(line string) (byte, int) {
	line = fenceContent(line)
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == line[0] {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return line[0], n
}

func closesFence(line string, char byte, length int) bool {
	line = fenceContent(line)
	c, n := fence(line)
	return c == char && n >= length && strings.TrimSpace(line[n:]) == ""
}

func fenceContent(line string) string {
	line = strings.TrimSpace(line)
	for strings.HasPrefix(line, ">") {
		line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
	}
	if len(line) >= 2 && strings.ContainsRune("-*+", rune(line[0])) && (line[1] == ' ' || line[1] == '\t') {
		return strings.TrimSpace(line[2:])
	}
	n := 0
	for n < len(line) && line[n] >= '0' && line[n] <= '9' {
		n++
	}
	if n > 0 && n+1 < len(line) && (line[n] == '.' || line[n] == ')') && (line[n+1] == ' ' || line[n+1] == '\t') {
		return strings.TrimSpace(line[n+2:])
	}
	return line
}

// Split oversized blocks at line boundaries. Reopen code fences and repeat table
// headers when splitting; ordinary code/table blocks remain byte-for-byte intact.
func splitBlock(block string) []string {
	if utf8.RuneCountInString(block) <= maxChunkRunes {
		return []string{block}
	}
	lines := strings.Split(block, "\n")
	prefix, suffix := "", ""
	if c, n := fence(lines[0]); n != 0 {
		prefix = lines[0] + "\n"
		suffix = "\n" + strings.Repeat(string(c), n)
		lines = lines[1:]
		if len(lines) > 0 && closesFence(lines[len(lines)-1], c, n) {
			suffix = "\n" + lines[len(lines)-1]
			lines = lines[:len(lines)-1]
		}
	} else if len(lines) >= 2 && strings.Contains(lines[0], "|") && tableSeparator(lines[1]) {
		prefix = lines[0] + "\n" + lines[1] + "\n"
		lines = lines[2:]
	}
	// Extremely long fence labels or table headers must not defeat the bound.
	if utf8.RuneCountInString(prefix)+utf8.RuneCountInString(suffix) > maxChunkRunes/2 {
		return splitPlain(block, maxChunkRunes)
	}
	budget := maxChunkRunes - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix)
	var out []string
	var group []string
	length := 0
	flush := func() {
		if len(group) > 0 {
			out = append(out, prefix+strings.Join(group, "\n")+suffix)
		}
		group, length = nil, 0
	}
	for _, line := range lines {
		for _, part := range splitPlain(line, budget) {
			count := utf8.RuneCountInString(part)
			if len(group) > 0 && length+1+count > budget {
				flush()
			}
			if len(group) > 0 {
				length++
			}
			group = append(group, part)
			length += count
		}
	}
	flush()
	return out
}

func tableSeparator(line string) bool {
	line = strings.TrimSpace(line)
	return strings.Contains(line, "-") && strings.Trim(line, "|:- \t") == ""
}

func splitPlain(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	var out []string
	for len(runes) > limit {
		out = append(out, string(runes[:limit]))
		runes = runes[limit:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

func trimRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for pos := range text {
		if count == limit {
			return text[:pos]
		}
		count++
	}
	return text
}

func boundedExcerpt(text string, limit int) string {
	marker := "\n[excerpt truncated]"
	if limit <= utf8.RuneCountInString(marker) {
		return trimRunes(text, limit)
	}
	return trimRunes(text, limit-utf8.RuneCountInString(marker)) + marker
}
