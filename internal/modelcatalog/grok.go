package modelcatalog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/wklee610/clivoy/internal/clidiscovery"
	"github.com/wklee610/clivoy/internal/provider"
)

// DiscoverGrok requests the same ACP model metadata as `grok models`. It never
// creates a session or submits a prompt. Grok may return its own bundled or
// cached catalog; the response is not proof of account access or a live fetch.
func DiscoverGrok(ctx context.Context, configuredCommand string, excludedEnv ...string) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolution, err := clidiscovery.Resolve("grok", configuredCommand)
	if err != nil {
		return nil, errors.New("grok CLI could not be resolved; run clivoy doctor")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	env, cleanup, err := provider.PrepareGrokMetadataEnvironment(discoveryEnvironment(os.Environ(), excludedEnv))
	if err != nil {
		return nil, discoveryError("grok")
	}
	defer cleanup()
	workingDir, err := os.MkdirTemp("", "clivoy-grok-models-*")
	if err != nil {
		return nil, discoveryError("grok")
	}
	defer os.RemoveAll(workingDir)
	// Top-level flags apply to process startup; --no-leader belongs to agent.
	args := []string{"--no-auto-update", "--sandbox", "clivoy-read-only", "agent", "--no-leader", "stdio"}
	cmd := exec.CommandContext(ctx, resolution.Path, append(resolution.Args, args...)...)
	cmd.Env = resolution.Environment(env)
	cmd.Dir = workingDir
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	configureProcess(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, discoveryError("grok")
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, discoveryError("grok")
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, discoveryError("grok")
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

	// Bound the whole stream as well as individual messages. Notifications
	// cannot keep an unbounded metadata stream alive until the deadline.
	scanner := bufio.NewScanner(io.LimitReader(output, maxCatalogBytes+1))
	scanner.Buffer(make([]byte, 4096), maxCatalogBytes)
	models, err := grokModels(scanner, json.NewEncoder(input))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, discoveryError("grok")
	}
	models = normalize(models)
	if len(models) == 0 {
		return nil, discoveryError("grok")
	}
	return models, nil
}

func grokModels(scanner *bufio.Scanner, encoder *json.Encoder) ([]Model, error) {
	// Match xai-grok-shell/src/cli_models.rs, including unavailable client
	// filesystem/terminal capabilities. ACP extensions have a wire '_' prefix.
	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": 1,
			"clientCapabilities": map[string]any{
				"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false,
			},
			"clientInfo": map[string]string{"name": "clivoy", "version": "1"},
			"_meta":      map[string]string{"clientType": "clivoy", "clientVersion": "1"},
		},
	}
	if err := encoder.Encode(initialize); err != nil {
		return nil, err
	}
	reader := grokMetadataReader{scanner: scanner}
	result, err := reader.response(1)
	if err != nil {
		return nil, err
	}
	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if json.Unmarshal(result, &initialized) != nil || initialized.ProtocolVersion != 1 {
		return nil, errors.New("unsupported Grok ACP protocol version")
	}
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "_x.ai/models/list", "params": map[string]any{},
	}); err != nil {
		return nil, err
	}
	result, err = reader.response(2)
	if err != nil {
		return nil, err
	}
	// Grok wraps extension responses in its own ExtMethodResult envelope.
	var state struct {
		Result *struct {
			AvailableModels []struct {
				ModelID string `json:"modelId"`
				Name    string `json:"name"`
			} `json:"availableModels"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(result, &state) != nil || state.Result == nil || nonNullJSON(state.Error) {
		return nil, errors.New("Grok model metadata request failed")
	}
	var models []Model
	for _, entry := range state.Result.AvailableModels {
		// Grok's gateway adapter accepts text input only; the CLI catalog does
		// not imply additional capabilities in Clivoy.
		models = append(models, Model{ID: entry.ModelID, Name: entry.Name, Capabilities: []string{"chat"}})
	}
	return models, nil
}

type grokMetadataReader struct {
	scanner  *bufio.Scanner
	messages int
	bytes    int
}

func (r *grokMetadataReader) response(id int) (json.RawMessage, error) {
	for r.scanner.Scan() {
		r.messages++
		r.bytes += len(r.scanner.Bytes()) + 1
		if r.messages > 128 || r.bytes > maxCatalogBytes {
			return nil, errors.New("Grok model metadata exceeded response limit")
		}
		var message struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal(r.scanner.Bytes(), &message) != nil || message.JSONRPC != "2.0" {
			return nil, errors.New("invalid Grok ACP response")
		}
		if message.Method != "" {
			if message.ID != nil {
				// No client callbacks, file access, terminal operations or tool
				// permissions are supported by a metadata-only connection.
				return nil, errors.New("Grok unexpectedly requested a client operation")
			}
			continue
		}
		if message.ID == nil || *message.ID != id {
			continue
		}
		if nonNullJSON(message.Error) || !nonNullJSON(message.Result) {
			return nil, errors.New("Grok ACP metadata request failed")
		}
		return message.Result, nil
	}
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.ErrUnexpectedEOF
}

func nonNullJSON(value json.RawMessage) bool {
	return len(value) != 0 && string(value) != "null"
}
