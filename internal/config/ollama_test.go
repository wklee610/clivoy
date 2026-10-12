package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultOllamaProviderDoesNotAssumeInstalledModel(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	p, ok := c.Providers["ollama"]
	if !ok || p.Type != "ollama" || !p.SupportsNativeModels() || p.IsCLI() {
		t.Fatalf("default Ollama provider is not an HTTP native-model backend: %+v", p)
	}
	if p.BaseURL != "" || p.APIKeyEnv != "" || len(p.Command) != 0 || p.WorkDir != "" {
		t.Fatalf("default Ollama provider unexpectedly requires local configuration: %+v", p)
	}
	for alias, m := range c.Models {
		if m.Provider == "ollama" {
			t.Fatalf("default alias %q assumes an installed Ollama model", alias)
		}
	}
	for _, name := range []string{"codex", "claude", "gemini", "grok"} {
		if p := c.Providers[name]; !p.IsCLI() || !p.SupportsNativeModels() || c.Models[name].Provider != name {
			t.Fatalf("adding Ollama changed the existing %s default", name)
		}
	}
	if (Provider{Type: "openai"}).SupportsNativeModels() {
		t.Fatal("generic OpenAI backends must still require configured model aliases")
	}
}

func TestLoadOllamaConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, provider, models, wantError string
	}{
		{"defaults without aliases", `{"type":"ollama"}`, `{}`, ""},
		{"chat alias", `{"type":"ollama"}`, `{"local":{"provider":"local","model":"gpt-oss:20b"}}`, ""},
		{"custom endpoint and key", `{"type":"ollama","base_url":"https://models.example.test/proxy/v1","api_key_env":"LOCAL_MODEL_KEY"}`, `{"local":{"provider":"local","model":"team/model:latest"}}`, ""},
		{"explicit vision alias", `{"type":"ollama"}`, `{"local":{"provider":"local","model":"vision:latest","capabilities":["chat","vision"]}}`, ""},
		{"command", `{"type":"ollama","command":"ollama"}`, `{}`, "apply only to CLI providers"},
		{"working directory", `{"type":"ollama","work_dir":"workspace"}`, `{}`, "apply only to CLI providers"},
		{"missing upstream", `{"type":"ollama"}`, `{"local":{"provider":"local"}}`, "requires an upstream model"},
		{"blank upstream", `{"type":"ollama"}`, `{"local":{"provider":"local","model":" \t"}}`, "requires an upstream model"},
		{"image generation", `{"type":"ollama"}`, `{"local":{"provider":"local","model":"image:latest","capabilities":["image_generation"]}}`, "image_generation requires an openai provider"},
		{"vision without chat", `{"type":"ollama"}`, `{"local":{"provider":"local","model":"vision:latest","capabilities":["vision"]}}`, "vision requires chat"},
		{"explicit empty capabilities", `{"type":"ollama"}`, `{"local":{"provider":"local","model":"gpt-oss:20b","capabilities":[]}}`, "capabilities must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"providers":{"local":` + tc.provider + `},"models":` + tc.models + `}`
			path := filepath.Join(t.TempDir(), "clivoy.json")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected error containing %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Providers) != 1 || c.Providers["local"].Type != "ollama" {
				t.Fatal("default providers leaked into an explicit Ollama configuration")
			}
			if tc.models == `{}` {
				if len(c.Models) != 0 {
					t.Fatal("default aliases leaked into an explicit empty model map")
				}
			} else if m := c.Models["local"]; len(c.Models) != 1 || m.Provider != "local" || m.Model == "" {
				t.Fatalf("explicit Ollama alias was lost: %+v", c.Models)
			}
			if tc.name == "chat alias" && !reflect.DeepEqual(c.Models["local"].Capabilities, []string{"chat"}) {
				t.Fatal("an omitted Ollama capability must default to chat only")
			}
			if tc.name == "explicit vision alias" && !reflect.DeepEqual(c.Models["local"].Capabilities, []string{"chat", "vision"}) {
				t.Fatal("explicit Ollama vision capability was lost")
			}
			if tc.name == "custom endpoint and key" {
				p := c.Providers["local"]
				if p.BaseURL != "https://models.example.test/proxy/v1" || p.APIKeyEnv != "LOCAL_MODEL_KEY" || c.Models["local"].Model != "team/model:latest" {
					t.Fatal("custom Ollama settings were not preserved")
				}
			}
		})
	}
}

func TestAliaslessConfigurationRequiresNativeModelProvider(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "gemini", "grok", "ollama", "openai"} {
		t.Run(kind, func(t *testing.T) {
			c := Default()
			p := Provider{Type: kind}
			if kind == "openai" {
				p.BaseURL = "https://models.example.test/v1"
			}
			c.Providers = map[string]Provider{"backend": p}
			c.Models = map[string]Model{}
			err := c.Validate()
			if kind != "openai" {
				if err != nil {
					t.Fatalf("native provider rejected an empty alias map: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("generic OpenAI configuration accepted no aliases")
			}
			c.Models["configured"] = Model{Provider: "backend"}
			if err := c.Validate(); err == nil {
				t.Fatal("generic OpenAI alias accepted an empty upstream model")
			}
			c.Models["configured"] = Model{Provider: "backend", Model: "upstream-model"}
			if err := c.Validate(); err != nil {
				t.Fatalf("generic OpenAI alias stopped working: %v", err)
			}
			c.Providers["backend"] = Provider{Type: "openai"}
			if err := c.Validate(); err == nil {
				t.Fatal("generic OpenAI configuration accepted no base URL")
			}
		})
	}
}
