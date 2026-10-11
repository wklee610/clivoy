package modelcatalog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wklee610/clivoy/internal/clidiscovery"
)

// Refresh asks the installed CLI for metadata, never starting a model turn.
// Failures return no models: cached or built-in selectors are not substituted.
// A CLI may itself return cached metadata; this is not an entitlement check.
// The total subprocess budget is five seconds, plus bounded process cleanup.
// It does not write Clivoy's configuration or the native CLI's cache itself.
func Refresh(ctx context.Context, kind, configuredCommand string, excludedEnv ...string) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch kind {
	case "gemini":
		return DiscoverGemini(ctx, configuredCommand, excludedEnv...)
	case "grok":
		return DiscoverGrok(ctx, configuredCommand, excludedEnv...)
	case "codex", "claude":
	default:
		return nil, errors.New("unsupported CLI model discovery provider")
	}
	resolution, err := clidiscovery.Resolve(kind, configuredCommand)
	if err != nil {
		return nil, errors.New(kind + " CLI could not be resolved; run clivoy doctor")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"app-server", "--listen", "stdio://"}
	if kind == "claude" {
		// Bare mode avoids user hooks, plugin sync, OAuth/keychain reads and
		// background prefetches. This is a CLI catalog, not an access check.
		args = []string{"--bare", "--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--disable-slash-commands", "--no-session-persistence", "--settings", `{"disableAllHooks":true}`}
	}
	workingDir, err := os.MkdirTemp("", "clivoy-models-*")
	if err != nil {
		return nil, discoveryError(kind)
	}
	defer os.RemoveAll(workingDir)
	cmd := exec.CommandContext(ctx, resolution.Path, append(resolution.Args, args...)...)
	cmd.Env = resolution.Environment(discoveryEnvironment(os.Environ(), excludedEnv))
	cmd.Dir = workingDir
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	configureProcess(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, discoveryError(kind)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, discoveryError(kind)
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return nil, discoveryError(kind)
	}
	// Closing our pipe handles also bounds reads if a descendant inherited
	// stdout and outlives the direct process.
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
	encoder := json.NewEncoder(input)
	var models []Model
	if kind == "codex" {
		models, err = codexModels(scanner, encoder)
	} else {
		models, err = claudeModels(scanner, encoder)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, discoveryError(kind)
	}
	models = normalize(models)
	if len(models) == 0 {
		return nil, discoveryError(kind)
	}
	return models, nil
}

func discoveryEnvironment(base, excluded []string) []string {
	blocked := map[string]bool{"CLIVOY_API_KEY": true, "CLIVOY_HOME": true}
	for _, name := range excluded {
		blocked[strings.ToUpper(name)] = true
	}
	result := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[strings.ToUpper(name)] {
			result = append(result, entry)
		}
	}
	return result
}

func codexModels(scanner *bufio.Scanner, encoder *json.Encoder) ([]Model, error) {
	if err := encoder.Encode(map[string]any{"method": "initialize", "id": 1, "params": map[string]any{"clientInfo": map[string]string{"name": "clivoy", "title": "Clivoy", "version": "1"}}}); err != nil {
		return nil, err
	}
	if _, err := codexResponse(scanner, 1); err != nil {
		return nil, err
	}
	if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	var models []Model
	cursor := ""
	for id := 2; id < 12; id++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := encoder.Encode(map[string]any{"method": "model/list", "id": id, "params": params}); err != nil {
			return nil, err
		}
		result, err := codexResponse(scanner, id)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data []struct {
				ID              string   `json:"id"`
				Model           string   `json:"model"`
				DisplayName     string   `json:"displayName"`
				Hidden          bool     `json:"hidden"`
				InputModalities []string `json:"inputModalities"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, err
		}
		for _, entry := range page.Data {
			if entry.Hidden {
				continue
			}
			if entry.Model == "" {
				entry.Model = entry.ID
			}
			models = append(models, Model{ID: entry.Model, Name: entry.DisplayName, Capabilities: capabilities(entry.InputModalities)})
		}
		if page.NextCursor == "" {
			return models, nil
		}
		cursor = page.NextCursor
	}
	return nil, errors.New("model catalog exceeded pagination limit")
}

func codexResponse(scanner *bufio.Scanner, id int) (json.RawMessage, error) {
	for scanner.Scan() {
		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, err
		}
		if message.ID != id {
			continue
		}
		if len(message.Error) != 0 || len(message.Result) == 0 || string(message.Result) == "null" {
			return nil, errors.New("model metadata request failed")
		}
		return message.Result, nil
	}
	return nil, io.ErrUnexpectedEOF
}

func claudeModels(scanner *bufio.Scanner, encoder *json.Encoder) ([]Model, error) {
	const requestID = "clivoy-models"
	if err := encoder.Encode(map[string]any{"type": "control_request", "request_id": requestID, "request": map[string]any{"subtype": "initialize", "hooks": nil}}); err != nil {
		return nil, err
	}
	for scanner.Scan() {
		var message struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Subtype   string `json:"subtype"`
				Response  struct {
					Models []struct {
						Value       string `json:"value"`
						DisplayName string `json:"displayName"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, err
		}
		if message.Type != "control_response" || message.Response.RequestID != requestID {
			continue
		}
		if message.Response.Subtype != "success" {
			return nil, errors.New("model metadata initialization failed")
		}
		models := make([]Model, 0, len(message.Response.Response.Models))
		for _, entry := range message.Response.Response.Models {
			models = append(models, Model{ID: entry.Value, Name: entry.DisplayName, Capabilities: capabilities(nil)})
		}
		return models, nil
	}
	return nil, io.ErrUnexpectedEOF
}
