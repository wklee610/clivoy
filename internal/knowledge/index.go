// Package knowledge provides a read-only Markdown index independent of an LLM
// provider or HTTP protocol. Refresh publishes an immutable snapshot atomically.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Resource limits bound one snapshot, including unusually large Markdown trees.
const (
	MaxFileBytes  = 2 << 20
	MaxFiles      = 5000
	MaxTotalBytes = 32 << 20
	MaxChunks     = 20000
	MaxEntries    = 100000
	MaxDepth      = 64
	maxFileBytes  = MaxFileBytes
)

type indexLimits struct{ files, bytes, chunks, entries, depth int }

// ErrResourceLimit means a refresh exceeded an indexing limit. No partial
// snapshot is published, so searches continue to use the last successful one.
var ErrResourceLimit = errors.New("knowledge index resource limit exceeded")

type Hit struct {
	ID      string  `json:"id"`
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Heading string  `json:"heading"`
	URL     string  `json:"url"`
	Text    string  `json:"text"`
	Score   float64 `json:"score"`
}

type Stats struct {
	Documents int       `json:"documents"`
	Chunks    int       `json:"chunks"`
	Skipped   int       `json:"skipped"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrNotReady means that no Refresh has completed successfully yet.
var ErrNotReady = errors.New("knowledge index is not ready")

type Index struct {
	root        string
	refreshLock chan struct{}
	limits      indexLimits
	data        atomic.Pointer[snapshot]
}

type document struct {
	hash   [32]byte
	chunks []chunk
}

type chunk struct {
	hit                   Hit
	terms                 map[string]float64
	length                float64
	titleTerms, fileTerms map[string]bool
	headingTerms          map[string]bool
	titleFocus, fileFocus float64
}

type posting struct {
	chunk int
	tf    float64
}

type snapshot struct {
	docs   map[string]document
	chunks []chunk
	terms  map[string][]posting
	vocab  []string
	avgLen float64
	stats  Stats
}

// New validates root but does not index it. Call Refresh before serving queries.
// A symlink supplied as root is rejected; ancestor aliases are canonicalized.
func New(root string) (*Index, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("knowledge root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve knowledge root: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("knowledge root must be an existing directory, not a symlink")
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve knowledge root: %w", err)
	}
	i := &Index{root: real, refreshLock: make(chan struct{}, 1), limits: indexLimits{MaxFiles, MaxTotalBytes, MaxChunks, MaxEntries, MaxDepth}}
	i.data.Store(&snapshot{docs: map[string]document{}, terms: map[string][]posting{}})
	return i, nil
}

// Refresh hashes each eligible file, reuses parsed unchanged documents, and
// atomically replaces the index. Any traversal/read error keeps the last good
// snapshot. Oversized, hidden, non-regular, symlink and invalid UTF-8 files are
// excluded. Neither the Markdown tree nor any external path is written.
func (i *Index) Refresh(ctx context.Context) error {
	select {
	case i.refreshLock <- struct{}{}:
		defer func() { <-i.refreshLock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(i.root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("knowledge root is no longer a regular directory")
	}
	root, err := os.OpenRoot(i.root)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(rootInfo, opened) {
		return errors.New("knowledge root changed while opening")
	}
	old := i.data.Load()
	next := &snapshot{docs: make(map[string]document), terms: make(map[string][]posting)}
	files, totalBytes, totalChunks, entries := 0, 0, 0, 0
	// Stream directory entries in bounded batches instead of loading an entire
	// large directory. Rooted handles prevent symlink replacement from escaping
	// the configured tree, even when its directories are concurrently renamed.
	var walk func(*os.Root, string, int) error
	walk = func(dir *os.Root, prefix string, depth int) error {
		if depth > i.limits.depth {
			return fmt.Errorf("%w: directory depth", ErrResourceLimit)
		}
		listing, err := dir.Open(".")
		if err != nil {
			return err
		}
		defer listing.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch, readErr := listing.ReadDir(128)
			for _, entry := range batch {
				if err := ctx.Err(); err != nil {
					return err
				}
				entries++
				if entries > i.limits.entries {
					return fmt.Errorf("%w: directory entries", ErrResourceLimit)
				}
				if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 {
					next.stats.Skipped++
					continue
				}
				name := entry.Name()
				rel := name
				if prefix != "" {
					rel = prefix + "/" + name
				}
				if entry.IsDir() {
					before, err := dir.Lstat(name)
					if err != nil {
						return err
					}
					if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
						return errors.New("knowledge directory changed while opening")
					}
					child, err := dir.OpenRoot(name)
					if err != nil {
						return err
					}
					after, statErr := child.Stat(".")
					current, pathErr := dir.Lstat(name)
					if statErr != nil || pathErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) || !os.SameFile(after, current) {
						child.Close()
						return errors.New("knowledge directory changed while opening")
					}
					err = walk(child, rel, depth+1)
					child.Close()
					if err != nil {
						return err
					}
					current, pathErr = dir.Lstat(name)
					if pathErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, current) {
						return errors.New("knowledge directory changed while indexing")
					}
					continue
				}
				if !strings.EqualFold(filepath.Ext(name), ".md") {
					continue
				}
				files++
				if files > i.limits.files {
					return fmt.Errorf("%w: Markdown files", ErrResourceLimit)
				}
				content, skip, err := readFile(ctx, dir, name)
				if err != nil {
					return fmt.Errorf("read Markdown %q: %w", rel, err)
				}
				if skip || !utf8.Valid(content) {
					next.stats.Skipped++
					continue
				}
				totalBytes += len(content)
				if totalBytes > i.limits.bytes {
					return fmt.Errorf("%w: Markdown bytes", ErrResourceLimit)
				}
				hash := sha256.Sum256(content)
				doc, cached := old.docs[rel]
				if !cached || doc.hash != hash {
					parsed, err := parseMarkdownContext(ctx, rel, string(content), i.limits.chunks-totalChunks)
					if err != nil {
						return err
					}
					doc = document{hash: hash, chunks: parsed}
				}
				totalChunks += len(doc.chunks)
				if totalChunks > i.limits.chunks {
					return fmt.Errorf("%w: Markdown chunks", ErrResourceLimit)
				}
				next.docs[rel] = doc
				next.stats.Documents++
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	if err := walk(root, "", 0); err != nil {
		return err
	}
	paths := make([]string, 0, len(next.docs))
	for path := range next.docs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		for _, c := range next.docs[path].chunks {
			if err := ctx.Err(); err != nil {
				return err
			}
			pos := len(next.chunks)
			next.chunks = append(next.chunks, c)
			next.avgLen += c.length
			for term, tf := range c.terms {
				next.terms[term] = append(next.terms[term], posting{pos, tf})
			}
		}
	}
	for term := range next.terms {
		next.vocab = append(next.vocab, term)
	}
	sort.Strings(next.vocab)
	next.stats.Chunks = len(next.chunks)
	if next.stats.Chunks > 0 {
		next.avgLen /= float64(next.stats.Chunks)
	}
	next.stats.UpdatedAt = time.Now().UTC()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := os.Lstat(i.root)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return errors.New("knowledge root changed while indexing")
	}
	i.data.Store(next)
	return nil
}

func readFile(ctx context.Context, root *os.Root, name string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, false, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxFileBytes {
		return nil, true, nil
	}
	// Nonblocking/no-follow flags on Unix keep a raced FIFO from hanging a
	// refresh. The opened file must also have the expected regular-file identity.
	f, err := openRegular(root, name)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, false, errors.New("Markdown file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: f}, maxFileBytes+1))
	if err != nil {
		return nil, false, err
	}
	after, err := root.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		return nil, false, errors.New("Markdown path changed while reading")
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, false, errors.New("Markdown content changed while reading")
	}
	return data, len(data) > maxFileBytes, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (i *Index) Stats() Stats { return i.data.Load().stats }

// Search returns up to limit chunks (at most two per document), ranked using
// BM25 with title/heading boosts. maxChars bounds the sum of Text rune counts,
// not bytes. Nonpositive limits or a query containing only stop words return
// an empty slice. Search never falls back to unrelated documents.
func (i *Index) Search(ctx context.Context, query string, limit, maxChars int) ([]Hit, error) {
	result := make([]Hit, 0)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := i.data.Load()
	if s.stats.UpdatedAt.IsZero() {
		return nil, ErrNotReady
	}
	if limit <= 0 || maxChars <= 0 {
		return result, nil
	}
	if limit > 50 {
		limit = 50
	}
	query = trimRunes(query, 2048)
	words := queryWords(query)
	if len(words) == 0 || len(s.chunks) == 0 {
		return result, nil
	}
	scores := make(map[int]float64)
	coverage := make(map[int]int)
	anchored := make(map[int]bool)
	topics := make(map[int]int)
	topicCount := 0
	for _, word := range words {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		group := make(map[int]float64)
		isTopic := explicitTopic(word)
		if isTopic {
			topicCount++
		}
		expanded := expandWord(word, s)
		// One IDF per query concept prevents a rare substring or synonym from
		// outranking the user's exact service name just because it is rare.
		matching := make(map[int]bool)
		for _, term := range expanded {
			for _, p := range s.terms[term.word] {
				matching[p.chunk] = true
			}
		}
		idf := math.Log1p((float64(len(s.chunks)-len(matching)) + .5) / (float64(len(matching)) + .5))
		for _, term := range expanded {
			ps := s.terms[term.word]
			for _, p := range ps {
				c := s.chunks[p.chunk]
				body := (p.tf * 2.2) / (p.tf + 1.2*(.25+.75*c.length/s.avgLen))
				identity := 0.0
				if c.titleTerms[term.word] {
					identity = 5 * c.titleFocus
				}
				if c.fileTerms[term.word] {
					identity = math.Max(identity, 6*c.fileFocus)
				}
				if c.headingTerms[term.word] {
					identity = math.Max(identity, 1.5)
				}
				score := term.weight * idf * (body + identity)
				if weakWords[word] {
					score *= .12
				}
				if score > group[p.chunk] {
					group[p.chunk] = score
				}
			}
		}
		for pos, score := range group {
			scores[pos] += score
			if !weakWords[word] {
				coverage[pos]++
				anchored[pos] = true
			}
			if isTopic {
				topics[pos]++
			}
		}
	}
	mostTopics := 0
	for _, count := range topics {
		if count > mostTopics {
			mostTopics = count
		}
	}
	if topicCount > 0 && mostTopics == 0 {
		return result, nil
	}
	type candidate struct {
		pos   int
		score float64
	}
	ranked := make([]candidate, 0, len(scores))
	for pos, score := range scores {
		if !anchored[pos] {
			continue
		}
		if topicCount > 0 && topics[pos] < mostTopics {
			continue
		}
		// Prefer chunks covering the question rather than one frequent word.
		score *= 1 + .35*float64(coverage[pos]-1)
		ranked = append(ranked, candidate{pos, score})
	}
	sort.Slice(ranked, func(a, b int) bool {
		if ranked[a].score == ranked[b].score {
			return s.chunks[ranked[a].pos].hit.ID < s.chunks[ranked[b].pos].hit.ID
		}
		return ranked[a].score > ranked[b].score
	})
	perDoc := make(map[string]int)
	seenID := make(map[string]bool)
	remaining := maxChars
	for _, candidate := range ranked {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hit := s.chunks[candidate.pos].hit
		if perDoc[hit.Path] >= 2 || seenID[hit.ID] {
			continue
		}
		count := utf8.RuneCountInString(hit.Text)
		if count > remaining {
			// Prefer complete code/table blocks; if no complete result fits, a
			// clearly marked prefix still respects the caller's hard budget.
			if len(result) != 0 {
				continue
			}
			hit.Text = boundedExcerpt(hit.Text, remaining)
			count = utf8.RuneCountInString(hit.Text)
		}
		if count == 0 {
			continue
		}
		hit.Score = candidate.score
		result = append(result, hit)
		perDoc[hit.Path]++
		seenID[hit.ID] = true
		remaining -= count
		if len(result) == limit || remaining == 0 {
			break
		}
	}
	return result, nil
}

func makeChunk(path, title, heading, url, text string) chunk {
	text = strings.TrimSpace(text)
	title = trimRunes(title, maxTitleRunes)
	heading = trimRunes(heading, maxHeadingRunes)
	hash := sha256.Sum256([]byte(path + "\x00" + heading + "\x00" + text))
	c := chunk{hit: Hit{ID: hex.EncodeToString(hash[:12]), Path: path, Title: title, Heading: heading, URL: url, Text: text}, terms: make(map[string]float64)}
	fileTitle := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	c.titleTerms, c.titleFocus = identityTerms(title)
	c.fileTerms, c.fileFocus = identityTerms(trimRunes(fileTitle, maxTitleRunes))
	c.headingTerms, _ = identityTerms(heading)
	for _, field := range []struct {
		text   string
		weight float64
	}{{text, 1}, {title, 4}, {trimRunes(fileTitle, maxTitleRunes), 4}, {heading, 2}} {
		for _, token := range tokens(field.text) {
			c.terms[token] += field.weight
			c.length += field.weight
			if stem := koreanStem(token); stem != token {
				c.terms[stem] += field.weight
			}
		}
	}
	if c.length < 1 {
		c.length = 1
	}
	return c
}

func identityTerms(text string) (map[string]bool, float64) {
	terms := make(map[string]bool)
	important := make(map[string]bool)
	for _, word := range tokens(text) {
		stem := koreanStem(word)
		terms[word], terms[stem] = true, true
		if !stopWords[word] && !stopWords[stem] && !weakWords[stem] && utf8.RuneCountInString(stem) >= 2 {
			important[stem] = true
		}
	}
	count := len(important)
	if count < 1 {
		count = 1
	}
	return terms, 1 / math.Sqrt(float64(count))
}
