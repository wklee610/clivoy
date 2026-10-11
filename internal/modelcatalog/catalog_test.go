package modelcatalog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The test executable also acts as a deliberately small metadata-only CLI.
// Unexpected methods fail: no test can silently start inference or tools.
func TestMain(m *testing.M) {
	mode := os.Getenv("CLIVOY_MODELCATALOG_TEST")
	if mode == "" {
		os.Exit(m.Run())
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "CLIVOY_API_KEY", "CLIVOY_HOME", "CUSTOM_GATEWAY", "CUSTOM_UPSTREAM":
			os.Exit(10)
		}
	}
	if mode == "hang" {
		time.Sleep(time.Minute)
		os.Exit(2)
	}
	if mode == "malformed" {
		fmt.Fprintln(os.Stderr, "private-cli-credential")
		fmt.Println("private-cli-credential is not JSON")
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	initialized := false
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		if mode == "claude" {
			options := strings.Join(os.Args[1:], " ")
			for _, flag := range []string{"--bare", "--no-session-persistence", "--strict-mcp-config", `{"disableAllHooks":true}`} {
				if !strings.Contains(options, flag) {
					os.Exit(4)
				}
			}
			body, ok := request["request"].(map[string]any)
			if !ok || request["type"] != "control_request" || body["subtype"] != "initialize" {
				os.Exit(5)
			}
			_ = encoder.Encode(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": request["request_id"], "response": map[string]any{
					"models": []map[string]string{{"value": "sonnet[1m]", "displayName": "Long context"}, {"value": "--unsafe", "displayName": "ignored"}},
				},
			}})
			continue
		}
		params, _ := request["params"].(map[string]any)
		switch request["method"] {
		case "initialize":
			if params["clientInfo"] == nil {
				os.Exit(6)
			}
			_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{}})
		case "initialized":
			initialized = true
		case "model/list":
			if !initialized || params["includeHidden"] != false || params["limit"] != float64(100) {
				os.Exit(7)
			}
			if params["cursor"] == nil {
				_ = encoder.Encode(map[string]any{"method": "notice", "params": map[string]any{}})
				_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{
					"data":       []map[string]any{{"id": "picker-id", "model": "native-a", "displayName": "Text model", "inputModalities": []string{"text"}}, {"model": "hidden", "hidden": true}},
					"nextCursor": "next-page",
				}})
			} else if params["cursor"] == "next-page" {
				_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{
					"data":       []map[string]any{{"model": "native-b", "displayName": "Legacy model"}, {"model": "native-a"}, {"model": "bad model"}},
					"nextCursor": nil,
				}})
			} else {
				os.Exit(8)
			}
		default:
			os.Exit(9)
		}
	}
	os.Exit(0)
}

func TestDiscoverReadsOnlyVisibleSafeCachedSelectors(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("CODEX_HOME", "native-cache")
	t.Setenv("CLIVOY_MODELCATALOG_TEST", "malformed")
	if err := os.Mkdir("native-cache", 0700); err != nil {
		t.Fatal(err)
	}
	cache := `{"models":[
		{"slug":"native-text","display_name":"Text","visibility":"list","input_modalities":["text"]},
		{"slug":"native-vision","visibility":"list","input_modalities":["text","image"]},
		{"slug":"native-old","display_name":"\u001b[31munsafe","visibility":"list"},
		{"slug":"native-text","visibility":"list"},
		{"slug":"hidden","visibility":"hide"},
		{"slug":"--model=bad","visibility":"list"},
		{"slug":"secret\nline","visibility":"list"}
	]}`
	if err := os.WriteFile(filepath.Join("native-cache", "models_cache.json"), []byte(cache), 0600); err != nil {
		t.Fatal(err)
	}
	models, err := Discover(context.Background(), "codex", "/this-command-must-never-run")
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{
		{ID: "native-old", Name: "native-old", Capabilities: []string{"chat", "vision"}},
		{ID: "native-text", Name: "Text", Capabilities: []string{"chat"}},
		{ID: "native-vision", Name: "native-vision", Capabilities: []string{"chat", "vision"}},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("filtered catalog = %+v, want %+v", models, want)
	}
}

func TestDiscoverMissingCacheAndClaudeAliasesDoNotRequireCLI(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	models, err := Discover(context.Background(), "codex", "/missing-codex")
	if err != nil || len(models) != 0 {
		t.Fatalf("missing optional cache: %v, %v", models, err)
	}
	models, err = Discover(context.Background(), "claude", "/missing-claude")
	if err != nil || len(models) != 4 {
		t.Fatalf("known aliases: %v, %v", models, err)
	}
	for i, id := range []string{"default", "haiku", "opus", "sonnet"} {
		if models[i].ID != id {
			t.Fatalf("alias %d = %q, want %q", i, models[i].ID, id)
		}
	}
}

func TestGeminiAndGrokRefreshDoesNotSubstituteLocalSelectors(t *testing.T) {
	for _, kind := range []string{"gemini", "grok"} {
		local, err := Discover(context.Background(), kind, "/must-not-run")
		if err != nil {
			t.Fatal(err)
		}
		refreshed, err := Refresh(context.Background(), kind, "/must-not-run")
		if err == nil || len(refreshed) != 0 {
			t.Fatalf("%s refresh should report an unavailable CLI without local selectors: %v", kind, err)
		}
		if kind == "gemini" {
			var ids []string
			for _, model := range local {
				ids = append(ids, model.ID)
				if !reflect.DeepEqual(model.Capabilities, []string{"chat"}) {
					t.Fatalf("Gemini advertised unsupported capabilities: %+v", model)
				}
			}
			if !reflect.DeepEqual(ids, []string{"auto", "flash", "flash-lite", "pro"}) {
				t.Fatalf("missing Gemini selectors: %v", ids)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Refresh(ctx, kind, ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s refresh ignored cancellation: %v", kind, err)
		}
	}
}

func TestDiscoverRejectsUnsafeOrMalformedCacheWithoutEchoingContents(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	path := filepath.Join(root, "models_cache.json")
	for _, content := range []string{"private-cli-credential", strings.Repeat("x", maxCatalogBytes+1), `{"models":[]} {"secret":"private-cli-credential"}`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Discover(context.Background(), "codex", ""); err == nil || strings.Contains(err.Error(), "private-cli-credential") {
			t.Fatalf("unsafe cache error: %v", err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), "codex", ""); err == nil {
		t.Fatal("accepted a nonregular cache")
	}
}

func TestRefreshUsesMetadataOnlyAndHandlesPagination(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLIVOY_MODELCATALOG_TEST", "codex")
	t.Setenv("clivoy_api_key", "private-gateway")
	t.Setenv("CLIVOY_HOME", "private-home")
	t.Setenv("CUSTOM_GATEWAY", "private-gateway")
	t.Setenv("custom_upstream", "private-upstream")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	models, err := Refresh(context.Background(), "codex", binary, "custom_gateway", "CUSTOM_UPSTREAM")
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "native-a", Name: "Text model", Capabilities: []string{"chat"}}, {ID: "native-b", Name: "Legacy model", Capabilities: []string{"chat", "vision"}}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("metadata catalog = %+v, want %+v", models, want)
	}
	t.Setenv("CLIVOY_MODELCATALOG_TEST", "claude")
	models, err = Refresh(context.Background(), "claude", binary, "custom_gateway", "CUSTOM_UPSTREAM")
	if err != nil || len(models) != 1 {
		t.Fatalf("Claude metadata must not add built-in aliases = %+v, %v", models, err)
	}
	if models[0].ID != "sonnet[1m]" {
		t.Fatalf("missing metadata selector: %+v", models)
	}
}

func TestRefreshBoundsWaitWithoutFallbackOrLeakingErrors(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIVOY_MODELCATALOG_TEST", "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	models, err := Refresh(ctx, "claude", binary)
	if !errors.Is(err, context.DeadlineExceeded) || len(models) != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("bounded failure: models=%d elapsed=%s error=%v", len(models), time.Since(start), err)
	}
	t.Setenv("CLIVOY_MODELCATALOG_TEST", "malformed")
	models, err = Refresh(context.Background(), "claude", binary)
	if err == nil || len(models) != 0 || strings.Contains(err.Error(), "private-cli-credential") {
		t.Fatalf("sanitized error without fallback: %v, models=%d", err, len(models))
	}
}

func TestDiscoveryEnvironmentRetainsOnlyNativeCredentials(t *testing.T) {
	base := []string{"PATH=bin", "CODEX_HOME=cache", "ANTHROPIC_API_KEY=native", "OPENAI_API_KEY=native", "clivoy_api_key=gateway", "Clivoy_Home=state", "Custom_Upstream=upstream"}
	want := base[:4]
	if got := discoveryEnvironment(base, []string{"custom_upstream"}); !reflect.DeepEqual(got, want) {
		t.Fatal("discovery environment did not preserve native settings while removing gateway/upstream credentials")
	}
}
