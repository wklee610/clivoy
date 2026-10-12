package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wklee610/clivoy/internal/config"
	"github.com/wklee610/clivoy/internal/modelcatalog"
)

func TestModelInspectionBeforeInitDoesNotCreateState(t *testing.T) {
	mockOllamaCatalog(t, func(*http.Request) (*http.Response, error) {
		return catalogResponse(http.StatusOK, `{"data":[]}`), nil
	})
	home := filepath.Join(t.TempDir(), "state")
	t.Setenv("CLIVOY_HOME", home)
	t.Setenv("CODEX_HOME", t.TempDir())
	var out, diagnostics bytes.Buffer
	queries := 0
	discover := func(_ context.Context, kind, _ string, _ ...string) ([]modelcatalog.Model, error) {
		queries++
		if kind == "grok" {
			return nil, errors.New("CLI not installed")
		}
		return []modelcatalog.Model{{ID: "reported-model", Capabilities: []string{"chat"}}}, nil
	}
	if err := listModelsWithDiscovery(nil, &out, &diagnostics, discover); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"codex/reported-model", "claude/reported-model", "gemini/reported-model", "grok", "SOURCE"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing model choice %q", want)
		}
	}
	if queries != 4 || !strings.Contains(diagnostics.String(), "grok: CLI not installed") {
		t.Fatalf("default models command did not query each CLI: queries=%d warnings=%s", queries, diagnostics.String())
	}
	for _, invented := range []string{"claude/sonnet", "gemini/pro", "grok/reported-model"} {
		if strings.Contains(out.String(), invented) {
			t.Fatalf("live output included a fallback model: %s", invented)
		}
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("model inspection initialized state: %v", err)
	}
	if err := run([]string{"models", "--config", filepath.Join(home, "missing.json")}, &out, &diagnostics); err == nil {
		t.Fatal("missing explicit configuration was ignored")
	}
}

func TestLiveModelChoicesPreserveAliasesAndRejectFallbacks(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.Provider{
		"local": {Type: "claude", Command: config.Command{"/configured/claude", "--dangerously-skip-permissions"}},
		"other": {Type: "gemini"},
	}
	c.Models = map[string]config.Model{"local/reported": {Provider: "other", Model: "pinned"}}
	query := func(_ context.Context, kind, command string, _ ...string) ([]modelcatalog.Model, error) {
		if kind == "claude" {
			if command != "/configured/claude" {
				t.Fatal("live discovery must use only the configured executable")
			}
			return []modelcatalog.Model{{ID: "reported"}, {ID: "second"}}, nil
		}
		return []modelcatalog.Model{{ID: "fallback-must-not-appear"}}, errors.New("metadata unavailable")
	}
	models, warnings := modelChoicesWithDiscovery(c, true, query)
	if len(models) != 2 || models["local/reported"].Provider != "other" || models["local/second"].Model != "second" || len(warnings) != 1 {
		t.Fatalf("incorrect live catalog or alias precedence: models=%v warnings=%v", models, warnings)
	}
}

func TestOllamaCatalogBudgetExcludesInterleavedCLIDiscovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		mockOllamaCatalog(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				time.Sleep(750 * time.Millisecond)
			} else {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) != 1250*time.Millisecond {
					t.Fatalf("remaining Ollama budget = %s, want 1.25s", time.Until(deadline))
				}
			}
			return catalogResponse(http.StatusOK, `{"data":[{"id":"installed:latest"}]}`), nil
		})
		c := config.Default()
		c.Providers = map[string]config.Provider{
			"a-ollama": {Type: "ollama"},
			"b-codex":  {Type: "codex"},
			"z-ollama": {Type: "ollama"},
		}
		c.Models = nil
		query := func(context.Context, string, string, ...string) ([]modelcatalog.Model, error) {
			time.Sleep(3 * time.Second)
			return []modelcatalog.Model{{ID: "reported"}}, nil
		}
		models, warnings := modelChoicesWithDiscovery(c, true, query)
		if calls != 2 || len(models) != 3 || len(warnings) != 0 {
			t.Fatalf("Ollama query budget included CLI time: calls=%d models=%v warnings=%v", calls, models, warnings)
		}
	})
}

func TestCachedModelChoicesPreserveExplicitAliases(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	cache := `{"models":[{"slug":"future-code-model","display_name":"Future model","visibility":"list","input_modalities":["text","image"]}]}`
	if err := os.WriteFile(filepath.Join(codexHome, "models_cache.json"), []byte(cache), 0600); err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	delete(c.Providers, "ollama")
	models := configuredModels(c)
	if m := models["codex/future-code-model"]; m.Provider != "codex" || m.Model != "future-code-model" {
		t.Fatalf("cache model was not qualified: %+v", m)
	}
	if len(c.Models) != 4 {
		t.Fatal("catalog discovery mutated configuration")
	}
	c.Models["codex/future-code-model"] = config.Model{Provider: "claude", Model: "pinned-model", Capabilities: []string{"chat"}}
	models = configuredModels(c)
	if m := models["codex/future-code-model"]; m.Provider != "claude" || m.Model != "pinned-model" {
		t.Fatal("catalog overwrote an explicit alias")
	}
	c.Providers = map[string]config.Provider{"http": {Type: "openai", BaseURL: "http://localhost/v1"}}
	c.Models = map[string]config.Model{"configured": {Provider: "http", Model: "upstream"}}
	if models := configuredModels(c); len(models) != 1 || models["configured"].Model != "upstream" {
		t.Fatal("native models leaked into an HTTP-only configuration")
	}
}

func TestNativeLoginKeySurvivesMatchingHTTPBackend(t *testing.T) {
	c := config.Default()
	c.Providers["xai"] = config.Provider{Type: "openai", APIKeyEnv: "XAI_API_KEY"}
	c.Providers["google"] = config.Provider{Type: "openai", APIKeyEnv: "GEMINI_API_KEY"}
	c.Providers["other"] = config.Provider{Type: "openai", APIKeyEnv: "PRIVATE_UPSTREAM_KEY"}
	for _, kind := range []string{"codex", "claude", "gemini", "grok"} {
		blocked := map[string]bool{}
		for _, name := range excludedCLIEnvironment(c, kind) {
			blocked[strings.ToUpper(name)] = true
		}
		if !blocked["CLIVOY_API_KEY"] || !blocked["PRIVATE_UPSTREAM_KEY"] {
			t.Fatalf("%s received unrelated gateway or backend credentials", kind)
		}
		if blocked["XAI_API_KEY"] != (kind != "grok") || blocked["GEMINI_API_KEY"] != (kind != "gemini") {
			t.Fatalf("%s native login credential handling is incorrect", kind)
		}
	}
	// Explicitly using a native variable for gateway authentication must still
	// keep that gateway key out of subprocesses.
	c.APIKeyEnv = "XAI_API_KEY"
	found := false
	for _, name := range excludedCLIEnvironment(c, "grok") {
		found = found || name == "XAI_API_KEY"
	}
	if !found {
		t.Fatal("native key exception exposed a gateway credential")
	}
}
