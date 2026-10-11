package modelcatalog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wklee610/clivoy/internal/clidiscovery"
)

var errGeminiCatalog = errors.New("Gemini CLI model metadata is unavailable; check the installed CLI version and login")

// DiscoverGemini queries the CLI's ACP session metadata using its existing
// login. It never sends session/prompt or authenticate, reads credentials, or
// substitutes a built-in catalog. Older CLIs (including 0.26.0) do not expose
// models in session/new and must be upgraded to support this query.
func DiscoverGemini(ctx context.Context, configuredCommand string, excludedEnv ...string) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolution, err := clidiscovery.Resolve("gemini", configuredCommand)
	if err != nil {
		return nil, errGeminiCatalog
	}
	dir, err := os.MkdirTemp("", "clivoy-gemini-models-*")
	if err != nil {
		return nil, errGeminiCatalog
	}
	defer os.RemoveAll(dir)
	cwd := filepath.Join(dir, "work")
	if err := os.Mkdir(cwd, 0700); err != nil {
		return nil, errGeminiCatalog
	}
	env, noMCP, err := geminiCatalogEnvironment(dir, excludedEnv)
	if err != nil {
		return nil, errGeminiCatalog
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"--experimental-acp", "--approval-mode", "default", "--extensions", "none", "--allowed-mcp-server-names", noMCP}
	cmd := exec.CommandContext(ctx, resolution.Path, append(resolution.Args, args...)...)
	cmd.Env = resolution.Environment(env)
	cmd.Dir = cwd
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	configureProcess(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, errGeminiCatalog
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errGeminiCatalog
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return nil, errGeminiCatalog
	}
	stopPipeCleanup := context.AfterFunc(ctx, func() {
		_ = input.Close()
		_ = output.Close()
	})
	defer func() {
		stopPipeCleanup()
		_ = input.Close()
		_ = output.Close()
		_ = cmd.Cancel()
		_ = cmd.Wait()
	}()
	scanner := bufio.NewScanner(io.LimitReader(output, maxCatalogBytes+1))
	scanner.Buffer(make([]byte, 4096), maxCatalogBytes)
	models, err := geminiACPModels(scanner, json.NewEncoder(input), cwd)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return models, err
}

func geminiCatalogEnvironment(dir string, excludedEnv []string) ([]string, string, error) {
	noMCP := filepath.Base(dir)
	// Match the generation adapter's isolation without changing HOME or the
	// CLI's auth selection. Metadata must not start local integrations either.
	settings := map[string]any{
		"tools":       map[string]any{"core": []string{}, "allowed": []string{}, "discoveryCommand": "", "callCommand": "", "enableHooks": false},
		"mcp":         map[string]any{"serverCommand": "", "allowed": []string{noMCP}, "excluded": []string{noMCP}},
		"hooksConfig": map[string]bool{"enabled": false},
		"skills":      map[string]bool{"enabled": false},
		"ide":         map[string]bool{"enabled": false},
		"context": map[string]any{
			"fileName": noMCP + ".md", "includeDirectoryTree": false, "includeDirectories": []string{},
			"loadMemoryFromIncludeDirectories": false, "memoryBoundaryMarkers": []string{},
		},
		"experimental": map[string]bool{"enableAgents": false, "autoMemory": false, "extensionReloading": false},
		"general":      map[string]any{"enableAutoUpdate": false, "sessionRetention": map[string]bool{"enabled": false}},
		"advanced":     map[string]any{"ignoreLocalEnv": true, "excludedEnvVars": append([]string{"CLIVOY_API_KEY", "CLIVOY_HOME"}, excludedEnv...)},
		"telemetry":    map[string]bool{"enabled": false, "logPrompts": false},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, "", err
	}
	overrides := map[string]string{
		"GEMINI_CLI_SYSTEM_SETTINGS_PATH": path,
		"GEMINI_CLI_SYSTEM_DEFAULTS_PATH": filepath.Join(dir, "no-defaults.json"),
		"GEMINI_CLI_NO_RELAUNCH":          "true",
		"GEMINI_SYSTEM_MD":                "false", "GEMINI_WRITE_SYSTEM_MD": "false", "NO_BROWSER": "true",
		"GEMINI_CLI_IDE_SERVER_STDIO_COMMAND": "", "GEMINI_CLI_IDE_SERVER_STDIO_ARGS": "", "GEMINI_CLI_IDE_SERVER_PORT": "",
	}
	base := discoveryEnvironment(os.Environ(), excludedEnv)
	env := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[strings.ToUpper(name)]; !replaced {
			env = append(env, entry)
		}
	}
	for name, value := range overrides {
		env = append(env, name+"="+value)
	}
	return env, noMCP, nil
}

func geminiACPModels(scanner *bufio.Scanner, encoder *json.Encoder, cwd string) ([]Model, error) {
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "clivoy", "version": "1"}},
	}); err != nil {
		return nil, errGeminiCatalog
	}
	init, err := geminiACPResponse(scanner, 1)
	if err != nil {
		return nil, err
	}
	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if json.Unmarshal(init, &initialized) != nil || initialized.ProtocolVersion != 1 {
		return nil, errors.New("Gemini CLI returned an unsupported ACP protocol version")
	}
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/new",
		"params": map[string]any{"cwd": cwd, "mcpServers": []any{}},
	}); err != nil {
		return nil, errGeminiCatalog
	}
	result, err := geminiACPResponse(scanner, 2)
	if err != nil {
		return nil, err
	}
	var session struct {
		Models *struct {
			AvailableModels []struct {
				ID   string `json:"modelId"`
				Name string `json:"name"`
			} `json:"availableModels"`
		} `json:"models"`
	}
	if json.Unmarshal(result, &session) != nil {
		return nil, errGeminiCatalog
	}
	if session.Models == nil {
		return nil, errors.New("Gemini CLI does not expose model metadata through ACP; update Gemini CLI")
	}
	models := make([]Model, 0, len(session.Models.AvailableModels))
	for _, item := range session.Models.AvailableModels {
		models = append(models, Model{ID: item.ID, Name: item.Name, Capabilities: []string{"chat"}})
	}
	models = normalize(models)
	if len(models) == 0 {
		return nil, errors.New("Gemini CLI returned no available model metadata")
	}
	return models, nil
}

func geminiACPResponse(scanner *bufio.Scanner, id int) (json.RawMessage, error) {
	for count := 0; count < 128 && scanner.Scan(); count++ {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			return nil, errGeminiCatalog
		}
		// Notifications can arrive before the response. A server request for
		// permissions, files, or tools is never serviced during catalog lookup.
		if message.Method != "" {
			if len(message.ID) > 0 && !bytes.Equal(message.ID, []byte("null")) {
				return nil, errGeminiCatalog
			}
			continue
		}
		if string(message.ID) != strconv.Itoa(id) {
			continue
		}
		if len(message.Error) > 0 || len(message.Result) == 0 || bytes.Equal(message.Result, []byte("null")) {
			return nil, errGeminiCatalog
		}
		return message.Result, nil
	}
	return nil, errGeminiCatalog
}
