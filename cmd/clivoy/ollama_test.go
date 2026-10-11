package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/config"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// These command tests run sequentially and never depend on a local Ollama service.
func mockOllamaCatalog(t *testing.T, f catalogTransport) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = original })
}

func catalogResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestOllamaModelChoicesPreserveAliasesAndQualifyNames(t *testing.T) {
	t.Setenv("TEST_OLLAMA_KEY", "private-key")
	calls := 0
	mockOllamaCatalog(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != "https://ollama.example/api/v1/models" || r.Header.Get("Authorization") != "Bearer private-key" {
			t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL)
		}
		return catalogResponse(http.StatusOK, `{"data":[{"id":"library/model:tag"},{"id":"pinned:latest"}]}`), nil
	})
	c := config.Default()
	c.Providers = map[string]config.Provider{
		"local": {Type: "ollama", BaseURL: "https://ollama.example/api/v1", APIKeyEnv: "TEST_OLLAMA_KEY"},
		"http":  {Type: "openai", BaseURL: "https://other.example/v1"},
	}
	c.Models = map[string]config.Model{"local/pinned:latest": {Provider: "http", Model: "pinned-upstream", Capabilities: []string{"chat", "vision"}}}
	for _, refresh := range []bool{false, true} {
		models, warnings := modelChoices(c, refresh)
		if len(warnings) != 0 || len(models) != 2 {
			t.Fatalf("models=%v warnings=%v", models, warnings)
		}
		m := models["local/library/model:tag"]
		if m.Provider != "local" || m.Model != "library/model:tag" || strings.Join(m.Capabilities, ",") != "chat" {
			t.Fatalf("wrong discovered binding: %+v", m)
		}
		if m := models["local/pinned:latest"]; m.Provider != "http" || m.Model != "pinned-upstream" || len(m.Capabilities) != 2 {
			t.Fatalf("explicit alias overwritten: %+v", m)
		}
	}
	if calls != 2 || len(c.Models) != 1 {
		t.Fatalf("catalog calls=%d or configuration mutated", calls)
	}
}

func TestUnavailableOllamaCatalogPreservesConfiguredModels(t *testing.T) {
	mockOllamaCatalog(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private transport error")
	})
	c := config.Default()
	c.Providers = map[string]config.Provider{"ollama": {Type: "ollama"}}
	c.Models = map[string]config.Model{"local": {Provider: "ollama", Model: "installed:latest"}}
	models, warnings := modelChoices(c, false)
	if len(models) != 1 || models["local"].Model != "installed:latest" || len(warnings) != 1 || !strings.Contains(warnings[0], "ollama:") || strings.Contains(warnings[0], "private") {
		t.Fatalf("models=%v warnings=%v", models, warnings)
	}
}

func TestOllamaMissingConfiguredKeyDoesNotRequestCatalog(t *testing.T) {
	t.Setenv("TEST_OLLAMA_KEY", "")
	mockOllamaCatalog(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("attempted discovery without configured credential")
		return nil, nil
	})
	c := config.Default()
	c.Providers = map[string]config.Provider{"ollama": {Type: "ollama", APIKeyEnv: "TEST_OLLAMA_KEY"}}
	c.Models = nil
	models, warnings := modelChoices(c, false)
	if len(models) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "TEST_OLLAMA_KEY is empty") {
		t.Fatalf("models=%v warnings=%v", models, warnings)
	}
}

func TestOllamaOnlyModelsCommand(t *testing.T) {
	mockOllamaCatalog(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://127.0.0.1:11434/v1/models" || r.Header.Get("Authorization") != "" {
			t.Fatalf("wrong default catalog request: %s", r.URL)
		}
		return catalogResponse(http.StatusOK, `{"data":[{"id":"installed:latest"}]}`), nil
	})
	path := filepath.Join(t.TempDir(), "ollama.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"ollama":{"type":"ollama"}},"models":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err := run([]string{"models", "--config", path}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ollama/installed:latest") || diagnostics.Len() != 0 {
		t.Fatalf("output=%s diagnostics=%s", out.String(), diagnostics.String())
	}
}
