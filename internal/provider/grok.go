package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

// Grok's empty --tools value means "inherit", and an unknown tool name can
// restore the entire toolset. Allow a known tool, then remove it and the two
// always-on MCP meta-tools. See xai-org/grok-build's agent/tool_list.rs.
func (p *cliProvider) grokArgs(req engine.Request) []string {
	args := []string{
		"--no-auto-update", "--no-leader", "--output-format", "streaming-json",
		"--tools", "read_file", "--disallowed-tools", "read_file,search_tool,use_tool",
		"--permission-mode", "dontAsk", "--sandbox", "clivoy-read-only",
		"--no-plan", "--no-subagents", "--no-memory", "--disable-web-search",
		"--max-turns", "1", "--verbatim",
		"--system-prompt-override", "Answer the supplied conversation. Do not use tools or access the local environment.",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	return args
}

// --prompt-file keeps request content out of the process arguments. Grok does
// not document a stdin prompt marker; use its cross-platform file interface.
func (p *cliProvider) prepareGrokInput(req engine.Request) ([]string, string, func(), error) {
	dir, err := os.MkdirTemp(p.tempDir, "clivoy-grok-prompt-*")
	if err != nil {
		return nil, "", nil, upstreamError("cannot create Grok CLI prompt directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "prompt.txt")
	// --verbatim only suppresses Grok's wrapper; @path expansion still runs.
	// The conversation is JSON, so escaping @ preserves its decoded value.
	prompt := strings.ReplaceAll(cliPrompt(req), "@", `\u0040`)
	if err := os.WriteFile(path, []byte(prompt), 0600); err != nil {
		cleanup()
		return nil, "", nil, upstreamError("cannot prepare Grok CLI prompt")
	}
	return append(p.grokArgs(req), "--prompt-file", path), "", cleanup, nil
}

const grokIsolatedConfig = `[cli]
auto_update = false
use_leader = false
[compat.claude]
agents = false
hooks = false
mcps = false
rules = false
skills = false
[compat.cursor]
agents = false
hooks = false
mcps = false
rules = false
skills = false
[compat.codex]
hooks = false
skills = false
[managed_mcps]
enabled = false
gateway_tools_enabled = false
[memory]
enabled = false
[workflows]
enabled = false
[features]
backend_tools = false
managed_config = false
campaigns = false
[telemetry]
trace_upload = false
`

// PrepareGrokMetadataEnvironment shares the adapter's configuration isolation
// with metadata-only CLI discovery. The caller must invoke the cleanup function.
func PrepareGrokMetadataEnvironment(env []string) ([]string, func(), error) {
	return (&cliProvider{}).prepareGrokEnvironment(env)
}

// A private Grok home prevents personal MCP servers, hooks, plugins, and saved
// sessions from being loaded. GROK_AUTH_PATH preserves the native login file,
// including Grok's own cross-process locking and rotating-token refresh writes.
// Managed installations fail closed: relocating their policy would be unsafe.
func (p *cliProvider) prepareGrokEnvironment(env []string) ([]string, func(), error) {
	if p.workdir != "" {
		return nil, nil, upstreamError("Grok CLI requires an isolated working directory; remove work_dir")
	}
	sourceHome, authPath := "", ""
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GROK_HOME") {
			sourceHome = value
		}
		if strings.EqualFold(key, "GROK_AUTH_PATH") {
			authPath = value
		}
	}
	if sourceHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, upstreamError("cannot locate Grok CLI login directory")
		}
		sourceHome = filepath.Join(home, ".grok")
	}
	if authPath == "" {
		authPath = filepath.Join(sourceHome, "auth.json")
	}
	authPath, err := filepath.Abs(authPath)
	if err != nil {
		return nil, nil, upstreamError("cannot locate Grok CLI login file")
	}
	if canonical, err := filepath.EvalSymlinks(filepath.Dir(authPath)); err == nil {
		authPath = filepath.Join(canonical, filepath.Base(authPath))
	}
	// Check before the child opens the path: an accidental FIFO must not leave
	// headless login blocked. Clivoy never reads the credential contents.
	if info, err := os.Stat(authPath); err != nil && !os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
		return nil, nil, upstreamError("Grok CLI login must be a regular file")
	}
	for _, path := range []string{
		filepath.Join(sourceHome, "requirements.toml"),
		filepath.Join(sourceHome, "managed_config.toml"),
		"/etc/grok/requirements.toml", "/etc/grok/managed_config.toml",
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return nil, nil, upstreamError("Grok CLI managed configuration is not supported in isolated mode; use an OpenAI-compatible HTTP provider")
		}
	}
	dir, err := os.MkdirTemp(p.tempDir, "clivoy-grok-home-*")
	if err != nil {
		return nil, nil, upstreamError("cannot create Grok CLI configuration directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// Canonicalize macOS /var and /tmp symlinks: Grok's sandbox rejects a
	// GROK_HOME path containing symlink components.
	if canonical, err := filepath.EvalSymlinks(dir); err == nil {
		dir = canonical
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(grokIsolatedConfig), 0600); err != nil {
		cleanup()
		return nil, nil, upstreamError("cannot prepare Grok CLI configuration")
	}
	// Native auth saves use an adjacent lock and atomic temporary-file rename,
	// requiring the auth directory to be writable even with a separate home.
	// All other paths retain the built-in read-only sandbox policy.
	authDir, _ := json.Marshal(filepath.Dir(authPath))
	sandbox := "[profiles.clivoy-read-only]\nextends = \"read-only\"\nread_write = [" + string(authDir) + "]\n"
	if err := os.WriteFile(filepath.Join(dir, "sandbox.toml"), []byte(sandbox), 0600); err != nil {
		cleanup()
		return nil, nil, upstreamError("cannot prepare Grok CLI sandbox")
	}
	result := make([]string, 0, len(env)+16)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GROK_") {
			result = append(result, entry)
		}
	}
	result = append(result, "GROK_HOME="+dir, "GROK_AUTH_PATH="+authPath, "GROK_DISABLE_AUTOUPDATER=1", "GROK_MEMORY=0", "GROK_SUBAGENTS=0", "GROK_WORKFLOWS=0", "GROK_BACKEND_SEARCH=0", "GROK_MANAGED_MCPS_ENABLED=0", "GROK_TELEMETRY_TRACE_UPLOAD=0")
	for _, source := range []string{"CLAUDE", "CURSOR", "CODEX"} {
		for _, feature := range []string{"AGENTS", "HOOKS", "MCPS", "RULES", "SKILLS"} {
			result = append(result, "GROK_"+source+"_"+feature+"_ENABLED=0")
		}
	}
	return result, cleanup, nil
}

func (p *cliParser) grokLine(line []byte) error {
	var event struct {
		Type       string          `json:"type"`
		Data       json.RawMessage `json:"data"`
		StopReason string          `json:"stopReason"`
		Usage      *struct {
			InputTokens   int `json:"input_tokens"`
			OutputTokens  int `json:"output_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &event) != nil || event.Type == "" {
		return upstreamError("Grok CLI returned invalid JSON output")
	}
	if p.done {
		return upstreamError("Grok CLI returned output after completion")
	}
	switch event.Type {
	case "error":
		return upstreamError("Grok CLI could not complete the request; check grok login, selected model, and configuration")
	case "tool_call", "tool_call_update":
		return upstreamError("Grok CLI unexpectedly attempted to use a tool")
	case "text":
		var data string
		if len(event.Data) == 0 || string(event.Data) == "null" || json.Unmarshal(event.Data, &data) != nil {
			return upstreamError("Grok CLI returned invalid text output")
		}
		return p.add(data)
	case "usage", "end":
		if event.Usage != nil {
			u := event.Usage
			if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheRead < 0 || u.CacheCreation < 0 {
				return upstreamError("Grok CLI returned invalid token usage")
			}
			p.result.Usage = engine.Usage{InputTokens: u.InputTokens + u.CacheRead + u.CacheCreation, OutputTokens: u.OutputTokens}
		}
		if event.Type == "end" {
			switch event.StopReason {
			case "end_turn", "stop_sequence":
				p.result.FinishReason = "stop"
			case "max_tokens":
				p.result.FinishReason = "length"
			default:
				return upstreamError("Grok CLI did not complete the response")
			}
			p.done = true
		}
	}
	return nil
}
