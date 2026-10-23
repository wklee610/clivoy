package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// KnowledgeBase describes a read-only Markdown source selected by model aliases.
type KnowledgeBase struct {
	Root             string `json:"root"`
	TopK             int    `json:"top_k"`
	MaxContextTokens int    `json:"max_context_tokens"`
	RefreshInterval  string `json:"refresh_interval"`
}

// validateKnowledge keeps retrieval references and model-scoped answer policy
// together. The zero value preserves the pre-fallback behavior.
func (m Model) validateKnowledge(bases map[string]KnowledgeBase) error {
	switch m.KnowledgeFallback {
	case "", "none", "model":
	default:
		return fmt.Errorf("knowledge_fallback must be 'none' or 'model'")
	}
	if m.Knowledge == "" {
		if m.KnowledgeFallback != "" {
			return fmt.Errorf("knowledge_fallback requires knowledge")
		}
		return nil
	}
	if _, ok := bases[m.Knowledge]; !ok {
		return fmt.Errorf("references an unknown knowledge base")
	}
	if m.Capabilities != nil && (len(m.Capabilities) != 1 || m.Capabilities[0] != "chat") {
		return fmt.Errorf("knowledge models support text chat only")
	}
	return nil
}

// DefaultKnowledgeBase supplies retrieval defaults without selecting a directory.
func DefaultKnowledgeBase() KnowledgeBase {
	return KnowledgeBase{TopK: 10, MaxContextTokens: 6000, RefreshInterval: "30s"}
}

// UnmarshalJSON applies defaults only to omitted fields, never explicit zero or
// null values, and keeps the surrounding configuration's strict field checking.
func (k *KnowledgeBase) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("knowledge base must be an object")
	}
	type knowledgeBase KnowledgeBase
	decoded := knowledgeBase(DefaultKnowledgeBase())
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("knowledge base field %q must not be null", name)
		}
	}
	result := KnowledgeBase(decoded)
	if err := result.validate(); err != nil {
		return err
	}
	*k = result
	return nil
}

func (k KnowledgeBase) validate() error {
	if strings.TrimSpace(k.Root) == "" || strings.ContainsRune(k.Root, '\x00') {
		return fmt.Errorf("root must be a nonempty directory path without NUL")
	}
	if k.TopK < 1 || k.TopK > 50 {
		return fmt.Errorf("top_k must be between 1 and 50")
	}
	if k.MaxContextTokens < 256 || k.MaxContextTokens > 32768 {
		return fmt.Errorf("max_context_tokens must be between 256 and 32768")
	}
	if d, err := time.ParseDuration(k.RefreshInterval); err != nil || d < time.Second || d > 24*time.Hour {
		return fmt.Errorf("refresh_interval must be between 1s and 24h")
	}
	return nil
}

// ResolveKnowledgeRoot resolves relative roots beside the loaded configuration
// and expands ~/ using the current user's home. It does not access or create the
// document directory; the index performs its own filesystem validation.
func (c Config) ResolveKnowledgeRoot(name string) (string, error) {
	base, ok := c.KnowledgeBases[name]
	if !ok {
		return "", fmt.Errorf("unknown knowledge base %q", name)
	}
	if err := base.validate(); err != nil {
		return "", fmt.Errorf("knowledge base %q: %w", name, err)
	}
	root := base.Root
	if root == "~" || strings.HasPrefix(root, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(root, `~\`)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve knowledge home: %w", err)
		}
		root = filepath.Join(home, strings.TrimLeft(root[1:], `/\`))
	}
	root = filepath.FromSlash(root)
	if !filepath.IsAbs(root) {
		root = filepath.Join(c.configDir, root)
	}
	return filepath.Abs(root)
}
