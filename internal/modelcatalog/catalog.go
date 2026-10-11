// Package modelcatalog discovers native CLI model selectors without inference.
// Catalog entries describe selectable names, not verified account entitlements.
package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const maxCatalogBytes = 4 << 20

type Model struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// Discover performs no CLI execution or network access. configuredCommand is
// accepted for symmetry with Refresh; local caches are scoped to CODEX_HOME,
// not to a particular executable. A missing Codex cache is an empty catalog.
func Discover(ctx context.Context, kind, configuredCommand string) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch kind {
	case "gemini":
		// Official CLI selectors; the CLI/account decides their current targets.
		return []Model{
			{ID: "auto", Name: "Gemini automatic selection", Capabilities: []string{"chat"}},
			{ID: "flash", Name: "Gemini Flash alias", Capabilities: []string{"chat"}},
			{ID: "flash-lite", Name: "Gemini Flash Lite alias", Capabilities: []string{"chat"}},
			{ID: "pro", Name: "Gemini Pro alias", Capabilities: []string{"chat"}},
		}, nil
	case "grok":
		// Grok's configured alias uses its default; explicit model selectors are
		// passed through as grok/<model-id> without claiming an account catalog.
		return nil, nil
	case "claude":
		return []Model{
			{ID: "default", Name: "Claude CLI default", Capabilities: capabilities(nil)},
			{ID: "haiku", Name: "Claude Haiku alias", Capabilities: capabilities(nil)},
			{ID: "opus", Name: "Claude Opus alias", Capabilities: capabilities(nil)},
			{ID: "sonnet", Name: "Claude Sonnet alias", Capabilities: capabilities(nil)},
		}, nil
	case "codex":
		root := os.Getenv("CODEX_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, errors.New("cannot locate Codex model cache")
			}
			root = filepath.Join(home, ".codex")
		}
		root, err := filepath.Abs(root)
		if err != nil {
			return nil, errors.New("cannot resolve Codex model cache directory")
		}
		return readCodexCache(filepath.Join(root, "models_cache.json"))
	default:
		return nil, errors.New("model discovery supports only codex, claude, gemini, and grok providers")
	}
}

func readCodexCache(path string) ([]Model, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCatalogBytes {
		return nil, errors.New("Codex model cache must be a regular file smaller than 4 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read Codex model cache")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("Codex model cache changed while opening")
	}
	var catalog struct {
		Models []struct {
			Slug            string   `json:"slug"`
			DisplayName     string   `json:"display_name"`
			Visibility      string   `json:"visibility"`
			InputModalities []string `json:"input_modalities"`
		} `json:"models"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, maxCatalogBytes+1))
	if err := decoder.Decode(&catalog); err != nil {
		return nil, errors.New("cannot decode Codex model cache")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("Codex model cache must contain one JSON object")
	}
	var models []Model
	for _, entry := range catalog.Models {
		if entry.Visibility != "" && entry.Visibility != "list" {
			continue
		}
		models = append(models, Model{ID: entry.Slug, Name: entry.DisplayName, Capabilities: capabilities(entry.InputModalities)})
	}
	return normalize(models), nil
}

func capabilities(modalities []string) []string {
	result := []string{"chat"}
	// Older CLI catalogs omit modalities; both native adapters support images.
	if modalities == nil {
		return append(result, "vision")
	}
	for _, modality := range modalities {
		if modality == "image" {
			return append(result, "vision")
		}
	}
	return result
}

func validID(id string) bool {
	alphanumeric := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	if len(id) == 0 || len(id) > 256 || !alphanumeric(id[0]) {
		return false
	}
	for i := 1; i < len(id); i++ {
		if !alphanumeric(id[i]) && !strings.ContainsRune("_-.:/[]", rune(id[i])) {
			return false
		}
	}
	return true
}

func normalize(models []Model) []Model {
	result := make([]Model, 0, len(models))
	seen := make(map[string]bool)
	for _, model := range models {
		if !validID(model.ID) || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		if len(model.Name) > 256 || strings.IndexFunc(model.Name, unicode.IsControl) >= 0 || strings.TrimSpace(model.Name) == "" {
			model.Name = model.ID
		}
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func discoveryError(kind string) error {
	// Metadata and stderr can include user settings or credentials. Never
	// include a subprocess response, stderr, or underlying parse error here.
	return fmt.Errorf("%s CLI model metadata is unavailable; check its installation and login", kind)
}
