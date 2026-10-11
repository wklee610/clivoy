package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

// Gemini has no --ignore-user-config flag. Its system settings override user
// and workspace settings while leaving HOME and the CLI's login intact.
// These settings are supported by Gemini CLI 0.26.0 and the current CLI.
func (p *cliProvider) prepareGeminiEnvironment(env []string) ([]string, string, func(), error) {
	dir, err := os.MkdirTemp(p.tempDir, "clivoy-gemini-*")
	if err != nil {
		return nil, "", nil, upstreamError("cannot create Gemini CLI settings directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// An empty MCP allowlist means "all". Use a unique request name that is
	// not configured as a server. Pass it on argv too: newer CLIs intersect
	// settings allowlists, and an empty intersection can enable all servers.
	noMCP := filepath.Base(dir)
	settings := map[string]any{
		"tools": map[string]any{
			"core": []string{}, "allowed": []string{}, "discoveryCommand": "", "callCommand": "", "enableHooks": false,
		},
		"mcp":         map[string]any{"serverCommand": "", "allowed": []string{noMCP}, "excluded": []string{noMCP}},
		"hooksConfig": map[string]bool{"enabled": false},
		"skills":      map[string]bool{"enabled": false},
		"ide":         map[string]bool{"enabled": false},
		"context": map[string]any{
			// An empty filename list falls back to GEMINI.md in older CLIs.
			// This per-request name cannot import the user's normal memories.
			"fileName": filepath.Base(dir) + ".md", "includeDirectoryTree": false, "includeDirectories": []string{}, "loadMemoryFromIncludeDirectories": false,
			"memoryBoundaryMarkers": []string{},
		},
		"experimental": map[string]bool{"enableAgents": false, "autoMemory": false, "extensionReloading": false},
		"general":      map[string]bool{"enableAutoUpdate": false},
		"advanced":     map[string]any{"ignoreLocalEnv": true, "excludedEnvVars": p.excludedEnv},
		"telemetry":    map[string]bool{"enabled": false, "logPrompts": false},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		cleanup()
		return nil, "", nil, upstreamError("cannot encode Gemini CLI settings")
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		cleanup()
		return nil, "", nil, upstreamError("cannot write Gemini CLI settings")
	}
	// Set each override once; duplicate environment names differ across OSes.
	env = replaceEnvironment(env, map[string]string{
		"GEMINI_CLI_SYSTEM_SETTINGS_PATH":     path,
		"GEMINI_CLI_SYSTEM_DEFAULTS_PATH":     filepath.Join(dir, "no-defaults.json"),
		"GEMINI_CLI_NO_RELAUNCH":              "true",
		"GEMINI_SYSTEM_MD":                    "false",
		"GEMINI_WRITE_SYSTEM_MD":              "false",
		"GEMINI_CLI_IDE_SERVER_STDIO_COMMAND": "",
		"GEMINI_CLI_IDE_SERVER_STDIO_ARGS":    "",
		"GEMINI_CLI_IDE_SERVER_PORT":          "",
		"NO_BROWSER":                          "true",
	})
	return env, noMCP, cleanup, nil
}

func replaceEnvironment(env []string, overrides map[string]string) []string {
	out := make([]string, 0, len(env)+len(overrides))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[strings.ToUpper(key)]; !replaced {
			out = append(out, entry)
		}
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

// See Gemini CLI's documented stream-json protocol. Only assistant content is
// returned; user echoes, tool results and diagnostics never become chat text.
func (p *cliParser) geminiLine(line []byte) error {
	var event struct {
		Type     string `json:"type"`
		Role     string `json:"role"`
		Content  string `json:"content"`
		Delta    bool   `json:"delta"`
		Status   string `json:"status"`
		Severity string `json:"severity"`
		Stats    struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			ToolCalls    int `json:"tool_calls"`
		} `json:"stats"`
	}
	if json.Unmarshal(line, &event) != nil {
		return upstreamError("Gemini CLI returned invalid JSON output")
	}
	if p.done {
		return upstreamError("Gemini CLI returned output after its completed result")
	}
	switch event.Type {
	case "message":
		if event.Role == "assistant" {
			if !event.Delta {
				return upstreamError("Gemini CLI returned an unsupported message event")
			}
			return p.add(event.Content)
		}
	case "tool_use", "tool_result":
		return upstreamError("Gemini CLI attempted to use a disabled tool")
	case "error":
		if event.Severity != "warning" {
			return upstreamError("Gemini CLI could not complete the request; run gemini locally to check login, selected model, and configuration")
		}
	case "result":
		if event.Status != "success" || event.Stats.ToolCalls != 0 {
			return upstreamError("Gemini CLI could not complete the request; run gemini locally to check login, selected model, and configuration")
		}
		if event.Stats.InputTokens < 0 || event.Stats.OutputTokens < 0 {
			return upstreamError("Gemini CLI returned invalid token usage")
		}
		p.done = true
		p.result.FinishReason = "stop"
		p.result.Usage = engine.Usage{InputTokens: event.Stats.InputTokens, OutputTokens: event.Stats.OutputTokens}
	}
	return nil
}
