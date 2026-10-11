package modelcatalog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This fixture runs before the package's shared TestMain. It accepts only the
// two metadata messages; any session creation, prompt or tool operation fails.
func init() {
	mode := os.Getenv("CLIVOY_GROK_MODELS_TEST")
	if mode == "" {
		return
	}
	fail := func() {
		fmt.Fprintln(os.Stderr, "private-cli-credential")
		fmt.Println("private-cli-credential")
		os.Exit(20)
	}
	args := []string{"--no-auto-update", "--sandbox", "clivoy-read-only", "agent", "--no-leader", "stdio"}
	if !reflect.DeepEqual(os.Args[1:], args) {
		fail()
	}
	for _, key := range []string{"CLIVOY_API_KEY", "CLIVOY_HOME", "CUSTOM_GROK_METADATA_SECRET", "GROK_UNSAFE_OVERRIDE"} {
		if _, present := os.LookupEnv(key); present {
			fail()
		}
	}
	if os.Getenv("XAI_API_KEY") != "test-native-key" || os.Getenv("GROK_DISABLE_AUTOUPDATER") != "1" || os.Getenv("GROK_MANAGED_MCPS_ENABLED") != "0" {
		fail()
	}
	home := os.Getenv("GROK_HOME")
	if home == "" || home == os.Getenv("CLIVOY_GROK_ORIGINAL_HOME") || os.Getenv("GROK_AUTH_PATH") != os.Getenv("CLIVOY_GROK_EXPECTED_AUTH") {
		fail()
	}
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || !strings.Contains(string(config), "hooks = false") || !strings.Contains(string(config), "mcps = false") || !strings.Contains(string(config), "auto_update = false") {
		fail()
	}
	cwd, err := os.Getwd()
	if err != nil || cwd == os.Getenv("CLIVOY_GROK_ORIGINAL_CWD") {
		fail()
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		fail()
	}
	if trace := os.Getenv("CLIVOY_GROK_MODELS_TRACE"); trace != "" {
		data, _ := json.Marshal([]string{cwd, home})
		if os.WriteFile(trace, data, 0600) != nil {
			fail()
		}
	}
	if mode == "hang" {
		time.Sleep(time.Minute)
		os.Exit(21)
	}
	if mode == "malformed" {
		fail()
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for id, method := range []string{"initialize", "_x.ai/models/list"} {
		var request struct {
			JSONRPC string         `json:"jsonrpc"`
			ID      int            `json:"id"`
			Method  string         `json:"method"`
			Params  map[string]any `json:"params"`
		}
		if decoder.Decode(&request) != nil || request.JSONRPC != "2.0" || request.ID != id+1 || request.Method != method {
			fail()
		}
		if id == 0 {
			caps, _ := request.Params["clientCapabilities"].(map[string]any)
			fs, _ := caps["fs"].(map[string]any)
			if request.Params["protocolVersion"] != float64(1) || caps["terminal"] != false || fs["readTextFile"] != false || fs["writeTextFile"] != false {
				fail()
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]int{"protocolVersion": 1}})
			continue
		}
		if len(request.Params) != 0 {
			fail()
		}
		if mode == "error" {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"result": nil, "error": "private-cli-credential"}})
		} else {
			models := []map[string]string{{"modelId": "grok-native-b", "name": "Native B"}, {"modelId": "grok-native-a", "name": "Native A"}, {"modelId": "--unsafe"}, {"modelId": "grok-native-a"}}
			if mode == "empty" {
				models = nil
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "_x.ai/notice", "params": map[string]any{}})
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"result": map[string]any{"currentModelId": "must-not-be-synthesized", "availableModels": models}}})
		}
	}
	// The connection must close without any more requests. The parent may kill
	// the child during bounded cleanup, as it does for the real stdio agent.
	var unexpected any
	if decoder.Decode(&unexpected) == nil {
		fail()
	}
	os.Exit(0)
}

func grokTestExecutable(t *testing.T, mode string) string {
	t.Helper()
	home := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(home); err == nil {
		home = canonical
	}
	auth := filepath.Join(home, "native-auth.json")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"CLIVOY_GROK_MODELS_TEST": mode, "GROK_HOME": home, "GROK_AUTH_PATH": auth,
		"CLIVOY_GROK_ORIGINAL_HOME": home, "CLIVOY_GROK_EXPECTED_AUTH": auth, "CLIVOY_GROK_ORIGINAL_CWD": cwd,
		"CLIVOY_API_KEY": "test-gateway-key", "CLIVOY_HOME": "test-gateway-home", "CUSTOM_GROK_METADATA_SECRET": "test-upstream-key",
		"GROK_UNSAFE_OVERRIDE": "unsafe", "XAI_API_KEY": "test-native-key",
	} {
		t.Setenv(name, value)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestDiscoverGrokMetadataOnlyAndCleanup(t *testing.T) {
	executable := grokTestExecutable(t, "success")
	trace := filepath.Join(t.TempDir(), "dirs.json")
	t.Setenv("CLIVOY_GROK_MODELS_TRACE", trace)
	models, err := DiscoverGrok(context.Background(), executable, "CUSTOM_GROK_METADATA_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{
		{ID: "grok-native-a", Name: "Native A", Capabilities: []string{"chat"}},
		{ID: "grok-native-b", Name: "Native B", Capabilities: []string{"chat"}},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	if json.Unmarshal(data, &dirs) != nil || len(dirs) != 2 {
		t.Fatal("missing fixture directory trace")
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temporary directory retained: %s", dir)
		}
	}
}

func TestDiscoverGrokFailureHasNoFallbackOrPrivateOutput(t *testing.T) {
	for _, mode := range []string{"malformed", "error", "empty"} {
		t.Run(mode, func(t *testing.T) {
			executable := grokTestExecutable(t, mode)
			models, err := DiscoverGrok(context.Background(), executable, "CUSTOM_GROK_METADATA_SECRET")
			if err == nil || len(models) != 0 || strings.Contains(err.Error(), "private-cli-credential") {
				t.Fatalf("models = %+v, error = %v", models, err)
			}
		})
	}
}

func TestDiscoverGrokCancellation(t *testing.T) {
	executable := grokTestExecutable(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	models, err := DiscoverGrok(ctx, executable, "CUSTOM_GROK_METADATA_SECRET")
	if !errors.Is(err, context.DeadlineExceeded) || len(models) != 0 {
		t.Fatalf("models = %+v, error = %v", models, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Grok metadata cancellation did not stop the process promptly")
	}
}

func TestDiscoverGrokMissingCLIHasNoFallback(t *testing.T) {
	models, err := DiscoverGrok(context.Background(), filepath.Join(t.TempDir(), "missing-grok"))
	if err == nil || len(models) != 0 {
		t.Fatalf("models = %+v, error = %v", models, err)
	}
}

func TestGrokMetadataRejectsInvalidProtocol(t *testing.T) {
	initialized := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}` + "\n"
	for name, input := range map[string]string{
		"wrong protocol": `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":2}}`,
		"not jsonrpc":    `{"id":1,"result":{"protocolVersion":1}}`,
		"outer error":    initialized + `{"jsonrpc":"2.0","id":2,"error":{"message":"sensitive"}}`,
		"inner error":    initialized + `{"jsonrpc":"2.0","id":2,"result":{"result":{"availableModels":[]},"error":"sensitive"}}`,
		"missing result": initialized + `{"jsonrpc":"2.0","id":2,"result":{}}`,
		"client tool":    initialized + `{"jsonrpc":"2.0","id":9,"method":"fs/read_text_file","params":{"path":"secret"}}`,
		"malformed":      initialized + `{"jsonrpc":"2.0","id":2,"result":`,
		"eof":            initialized,
		"notifications":  strings.Repeat(`{"jsonrpc":"2.0","method":"_notice"}`+"\n", 129),
		"oversize":       `{"jsonrpc":"2.0","method":"_notice","params":"` + strings.Repeat("a", maxCatalogBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			scanner := bufio.NewScanner(strings.NewReader(input))
			scanner.Buffer(make([]byte, 4096), maxCatalogBytes)
			models, err := grokModels(scanner, json.NewEncoder(io.Discard))
			if err == nil || len(models) != 0 {
				t.Fatalf("models = %+v, error = %v", models, err)
			}
		})
	}
}

func TestGrokMetadataUsesOnlyAvailableModels(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1},"error":null}
{"jsonrpc":"2.0","method":"_notice","params":{}}
{"jsonrpc":"2.0","id":2,"result":{"result":{"currentModelId":"not-a-catalog-entry","availableModels":[{"modelId":"native-model","name":"Native model"}]},"error":null}}
`
	var sent bytes.Buffer
	models, err := grokModels(bufio.NewScanner(strings.NewReader(input)), json.NewEncoder(&sent))
	if err != nil || len(models) != 1 || models[0].ID != "native-model" || !reflect.DeepEqual(models[0].Capabilities, []string{"chat"}) {
		t.Fatalf("models = %+v, error = %v", models, err)
	}
	if strings.Count(sent.String(), "\n") != 2 || strings.Contains(sent.String(), "session/") {
		t.Fatalf("unexpected requests: %s", sent.String())
	}
}
