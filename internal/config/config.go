// Package config loads administrator-owned configuration. API callers cannot
// choose executable paths, working directories, or upstream URLs.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wklee610/clivoy/internal/state"
)

type Provider struct {
	Type      string  `json:"type"`
	Command   Command `json:"command,omitempty"`
	WorkDir   string  `json:"work_dir,omitempty"`
	BaseURL   string  `json:"base_url,omitempty"`
	APIKeyEnv string  `json:"api_key_env,omitempty"`
}

func (p Provider) IsCLI() bool {
	return p.Type == "codex" || p.Type == "claude" || p.Type == "gemini" || p.Type == "grok"
}

func (p Provider) SupportsNativeModels() bool {
	return p.IsCLI() || p.Type == "ollama"
}

// CLICapabilities describes the adapter, not everything the upstream model can do.
func (p Provider) CLICapabilities() []string {
	if p.Type == "codex" || p.Type == "claude" {
		return []string{"chat", "vision"}
	}
	return []string{"chat"}
}

type Model struct {
	Provider     string   `json:"provider"`
	Model        string   `json:"model,omitempty"`
	Knowledge    string   `json:"knowledge,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// UnmarshalJSON keeps an explicitly empty or null capabilities value distinct
// from an omitted value, while preserving strict configuration decoding.
func (m *Model) UnmarshalJSON(data []byte) error {
	type model Model
	var decoded model
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for field := range fields {
		if strings.EqualFold(field, "capabilities") && decoded.Capabilities == nil {
			decoded.Capabilities = []string{}
		}
	}
	*m = Model(decoded)
	return nil
}

type Config struct {
	Listen         string                   `json:"listen"`
	APIKeyEnv      string                   `json:"api_key_env"`
	AuthFile       string                   `json:"auth_file,omitempty"`
	APIKeyFile     string                   `json:"api_key_file,omitempty"`
	LogFile        string                   `json:"log_file,omitempty"`
	RequestTimeout string                   `json:"request_timeout"`
	MaxConcurrent  int                      `json:"max_concurrent"`
	MaxQueue       int                      `json:"max_queue"`
	MaxBodyBytes   int64                    `json:"max_body_bytes"`
	Providers      map[string]Provider      `json:"providers"`
	Models         map[string]Model         `json:"models"`
	KnowledgeBases map[string]KnowledgeBase `json:"knowledge_bases,omitempty"`
	configDir      string
}

func Default() Config {
	return Config{
		Listen: "0.0.0.0:9610", APIKeyEnv: "CLIVOY_API_KEY",
		RequestTimeout: "5m", MaxConcurrent: 8, MaxQueue: 32, MaxBodyBytes: 32 << 20,
		Providers: map[string]Provider{
			"codex": {Type: "codex"}, "claude": {Type: "claude"},
			"gemini": {Type: "gemini"}, "grok": {Type: "grok"},
			"ollama": {Type: "ollama"},
		},
		Models: map[string]Model{
			"codex":  {Provider: "codex", Capabilities: []string{"chat", "vision"}},
			"claude": {Provider: "claude", Capabilities: []string{"chat", "vision"}},
			"gemini": {Provider: "gemini", Capabilities: []string{"chat"}},
			"grok":   {Provider: "grok", Capabilities: []string{"chat"}},
		},
	}
}

func Load(path string) (Config, error) {
	c := Default()
	if path == "" {
		paths, err := state.Resolve()
		if err != nil {
			return c, err
		}
		path = paths.Config
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return c, fmt.Errorf("resolve config path: %w", err)
	}
	c.configDir = filepath.Dir(absolute)
	f, err := os.Open(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			return c, fmt.Errorf("open config (run clivoy init to create your configuration): %w", err)
		}
		return c, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	// Replace maps rather than merging the default providers into an explicit config.
	c.Providers, c.Models, c.KnowledgeBases = nil, nil, nil
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("config must contain one JSON object")
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	for name, model := range c.Models {
		if model.Capabilities == nil {
			model.Capabilities = []string{"chat"}
			c.Models[name] = model
		}
	}
	return c, nil
}

// ResolveLogFile returns the configured destination without opening or creating
// it. A dash disables file logging. Relative paths belong to the config file's
// directory; an omitted destination uses the managed user log directory.
func (c Config) ResolveLogFile() (string, error) {
	if c.LogFile == "-" {
		return "-", nil
	}
	if c.LogFile == "" {
		paths, err := state.Resolve()
		if err != nil {
			return "", err
		}
		return paths.Log, nil
	}
	if strings.TrimSpace(c.LogFile) == "" {
		return "", fmt.Errorf("log_file must be a file path or '-' to disable file logging")
	}
	path := c.LogFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.configDir, path)
	}
	return filepath.Abs(path)
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("listen is required")
	}
	if strings.TrimSpace(c.APIKeyEnv) == "" && strings.TrimSpace(c.AuthFile) == "" && strings.TrimSpace(c.APIKeyFile) == "" {
		return fmt.Errorf("api_key_env, auth_file, or api_key_file is required")
	}
	if c.AuthFile != "" && strings.TrimSpace(c.AuthFile) == "" {
		return fmt.Errorf("auth_file must be a file path")
	}
	if c.LogFile != "" && strings.TrimSpace(c.LogFile) == "" {
		return fmt.Errorf("log_file must be a file path or '-' to disable file logging")
	}
	if d, err := time.ParseDuration(c.RequestTimeout); err != nil || d <= 0 || d > 24*time.Hour {
		return fmt.Errorf("request_timeout must be a positive duration up to 24h")
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 1024 {
		return fmt.Errorf("max_concurrent must be between 1 and 1024")
	}
	if c.MaxQueue < 0 || c.MaxQueue > 100000 {
		return fmt.Errorf("max_queue must be between 0 and 100000")
	}
	if c.MaxBodyBytes < 1 || c.MaxBodyBytes > 64<<20 {
		return fmt.Errorf("max_body_bytes must be between 1 and 67108864")
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("providers must not be empty")
	}
	for name, base := range c.KnowledgeBases {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("knowledge base names must not be empty")
		}
		if err := base.validate(); err != nil {
			return fmt.Errorf("knowledge base %q: %w", name, err)
		}
	}
	hasNativeModels := false
	for _, p := range c.Providers {
		hasNativeModels = hasNativeModels || p.SupportsNativeModels()
	}
	if len(c.Models) == 0 && !hasNativeModels {
		return fmt.Errorf("models must not be empty without a CLI or Ollama provider")
	}
	for name, p := range c.Providers {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("provider names must not be empty")
		}
		if err := p.Command.validate(); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		switch p.Type {
		case "codex", "claude", "gemini", "grok":
			if p.BaseURL != "" || p.APIKeyEnv != "" {
				return fmt.Errorf("provider %q: base_url and api_key_env apply only to HTTP providers", name)
			}
			if p.Type == "grok" && p.WorkDir != "" {
				return fmt.Errorf("provider %q: Grok CLI requires a temporary working directory; omit work_dir", name)
			}
		case "openai", "ollama":
			if p.Type == "openai" && p.BaseURL == "" {
				return fmt.Errorf("provider %q: base_url is required", name)
			}
			if len(p.Command) != 0 || p.WorkDir != "" {
				return fmt.Errorf("provider %q: command and work_dir apply only to CLI providers", name)
			}
		default:
			return fmt.Errorf("provider %q: unsupported type %q", name, p.Type)
		}
	}
	for name, m := range c.Models {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("model aliases must not be empty")
		}
		p, ok := c.Providers[m.Provider]
		if !ok {
			return fmt.Errorf("model %q references an unknown provider", name)
		}
		if m.Knowledge != "" {
			if _, ok := c.KnowledgeBases[m.Knowledge]; !ok {
				return fmt.Errorf("model %q references an unknown knowledge base", name)
			}
			if m.Capabilities != nil && (len(m.Capabilities) != 1 || m.Capabilities[0] != "chat") {
				return fmt.Errorf("model %q: knowledge models support text chat only", name)
			}
		}
		if (p.Type == "openai" || p.Type == "ollama") && strings.TrimSpace(m.Model) == "" {
			return fmt.Errorf("model %q requires an upstream model", name)
		}
		if m.Capabilities != nil && len(m.Capabilities) == 0 {
			return fmt.Errorf("model %q: capabilities must not be empty", name)
		}
		capabilities := make(map[string]bool)
		for _, capability := range m.Capabilities {
			if capability != "chat" && capability != "vision" && capability != "image_generation" {
				return fmt.Errorf("model %q: unsupported capability %q", name, capability)
			}
			if capabilities[capability] {
				return fmt.Errorf("model %q: duplicate capability %q", name, capability)
			}
			capabilities[capability] = true
		}
		if capabilities["vision"] && !capabilities["chat"] {
			return fmt.Errorf("model %q: vision requires chat capability", name)
		}
		if capabilities["vision"] && (p.Type == "gemini" || p.Type == "grok") {
			return fmt.Errorf("model %q: %s CLI supports text chat only", name, p.Type)
		}
		if capabilities["image_generation"] && p.Type != "openai" {
			return fmt.Errorf("model %q: image_generation requires an openai provider", name)
		}
	}
	return nil
}
