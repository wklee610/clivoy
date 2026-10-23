package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func loadKnowledgeConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clivoy.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestKnowledgeDefaultsAndLegacyConfiguration(t *testing.T) {
	c, err := loadKnowledgeConfig(t, `{"providers":{"local":{"type":"codex"}},"knowledge_bases":{"notes":{"root":"notes"}},"models":{"infra":{"provider":"local","knowledge":"notes"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultKnowledgeBase()
	want.Root = "notes"
	if len(c.KnowledgeBases) != 1 || c.KnowledgeBases["notes"] != want {
		t.Fatalf("knowledge defaults = %#v", c.KnowledgeBases)
	}
	if m := c.Models["infra"]; m.Knowledge != "notes" || !reflect.DeepEqual(m.Capabilities, []string{"chat"}) {
		t.Fatalf("knowledge model = %#v", m)
	}
	legacy, err := loadKnowledgeConfig(t, `{"providers":{"local":{"type":"codex","command":"codex"}},"models":{"answer":{"provider":"local"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.KnowledgeBases != nil || len(legacy.Models) != 1 || legacy.Models["answer"].Knowledge != "" || Default().KnowledgeBases != nil {
		t.Fatal("knowledge configuration leaked into existing defaults or legacy config")
	}
	encoded, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(encoded), "knowledge") {
		t.Fatalf("legacy encoding added knowledge settings: %s, %v", encoded, err)
	}
}

func TestKnowledgeConfigurationValidation(t *testing.T) {
	for _, tc := range []struct{ name, base, model, wantError string }{
		{"minimums", `{"root":"notes","top_k":1,"max_context_tokens":256,"refresh_interval":"1s"}`, `{"provider":"local","knowledge":"notes"}`, ""},
		{"maximums", `{"root":"notes","top_k":50,"max_context_tokens":32768,"refresh_interval":"24h"}`, `{"provider":"local","knowledge":"notes","capabilities":["chat"]}`, ""},
		{"zero top k", `{"root":"notes","top_k":0}`, `{"provider":"local"}`, "top_k"},
		{"negative top k", `{"root":"notes","top_k":-1}`, `{"provider":"local"}`, "top_k"},
		{"large top k", `{"root":"notes","top_k":51}`, `{"provider":"local"}`, "top_k"},
		{"zero tokens", `{"root":"notes","max_context_tokens":0}`, `{"provider":"local"}`, "max_context_tokens"},
		{"small tokens", `{"root":"notes","max_context_tokens":255}`, `{"provider":"local"}`, "max_context_tokens"},
		{"large tokens", `{"root":"notes","max_context_tokens":32769}`, `{"provider":"local"}`, "max_context_tokens"},
		{"empty refresh", `{"root":"notes","refresh_interval":""}`, `{"provider":"local"}`, "refresh_interval"},
		{"zero refresh", `{"root":"notes","refresh_interval":"0s"}`, `{"provider":"local"}`, "refresh_interval"},
		{"small refresh", `{"root":"notes","refresh_interval":"999ms"}`, `{"provider":"local"}`, "refresh_interval"},
		{"large refresh", `{"root":"notes","refresh_interval":"25h"}`, `{"provider":"local"}`, "refresh_interval"},
		{"invalid refresh", `{"root":"notes","refresh_interval":"daily"}`, `{"provider":"local"}`, "refresh_interval"},
		{"missing root", `{}`, `{"provider":"local"}`, "root"},
		{"blank root", `{"root":" \t"}`, `{"provider":"local"}`, "root"},
		{"nul root", `{"root":"notes\u0000"}`, `{"provider":"local"}`, "root"},
		{"null base", `null`, `{"provider":"local"}`, "object"},
		{"null root", `{"root":null}`, `{"provider":"local"}`, "null"},
		{"null top k", `{"root":"notes","top_k":null}`, `{"provider":"local"}`, "null"},
		{"null tokens", `{"root":"notes","max_context_tokens":null}`, `{"provider":"local"}`, "null"},
		{"null refresh", `{"root":"notes","refresh_interval":null}`, `{"provider":"local"}`, "null"},
		{"unknown setting", `{"root":"notes","topk":4}`, `{"provider":"local"}`, "unknown field"},
		{"unknown reference", `{"root":"notes"}`, `{"provider":"local","knowledge":"missing"}`, "unknown knowledge base"},
		{"unknown model field", `{"root":"notes"}`, `{"provider":"local","knowlege":"notes"}`, "unknown field"},
		{"vision", `{"root":"notes"}`, `{"provider":"local","knowledge":"notes","capabilities":["chat","vision"]}`, "text chat only"},
		{"images", `{"root":"notes"}`, `{"provider":"local","knowledge":"notes","capabilities":["image_generation"]}`, "text chat only"},
		{"empty capabilities", `{"root":"notes"}`, `{"provider":"local","knowledge":"notes","capabilities":[]}`, "text chat only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadKnowledgeConfig(t, `{"providers":{"local":{"type":"codex"}},"knowledge_bases":{"notes":`+tc.base+`},"models":{"infra":`+tc.model+`}}`)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("want error containing %q, got %v", tc.wantError, err)
			}
		})
	}
	c := Default()
	base := DefaultKnowledgeBase()
	base.Root = "notes"
	c.KnowledgeBases = map[string]KnowledgeBase{" \t": base}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted blank knowledge base name")
	}
}

func TestResolveKnowledgeRoot(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	configDir := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "absolute notes")
	cases := []struct{ name, root, want string }{
		{"relative", "knowledge/default", filepath.Join(configDir, "knowledge", "default")},
		{"absolute", absolute, absolute},
		{"home", "~/notes", filepath.Join(home, "notes")},
		{"home itself", "~", home},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, struct{ name, root, want string }{"windows home", `~\notes`, filepath.Join(home, "notes")})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"providers":       map[string]any{"local": map[string]string{"type": "codex"}},
				"knowledge_bases": map[string]any{"notes": map[string]string{"root": tc.root}},
			})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(configDir, "clivoy.json")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			if got, err := c.ResolveKnowledgeRoot("notes"); err != nil || got != tc.want {
				t.Fatalf("root = %q, want %q, err = %v", got, tc.want, err)
			}
			if _, err := c.ResolveKnowledgeRoot("missing"); err == nil {
				t.Fatal("resolved unknown knowledge base")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(configDir, "knowledge")); !os.IsNotExist(err) {
		t.Fatalf("resolving a root created document directories: %v", err)
	}
}

func TestKnowledgeFallbackConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, fields, want, wantError string
	}{
		{"omitted", `"knowledge":"notes"`, "", ""},
		{"disabled", `"knowledge":"notes","knowledge_fallback":"none"`, "none", ""},
		{"enabled", `"knowledge":"notes","knowledge_fallback":"model"`, "model", ""},
		{"case insensitive key", `"knowledge":"notes","KNOWLEDGE_FALLBACK":"model"`, "model", ""},
		{"unknown value", `"knowledge":"notes","knowledge_fallback":"retry"`, "", "knowledge_fallback"},
		{"uppercase value", `"knowledge":"notes","knowledge_fallback":"MODEL"`, "", "knowledge_fallback"},
		{"blank", `"knowledge":"notes","knowledge_fallback":" "`, "", "knowledge_fallback"},
		{"empty", `"knowledge":"notes","knowledge_fallback":""`, "", "knowledge_fallback"},
		{"null", `"knowledge":"notes","knowledge_fallback":null`, "", "knowledge_fallback"},
		{"duplicate null", `"knowledge":"notes","knowledge_fallback":"model","knowledge_fallback":null`, "", "knowledge_fallback"},
		{"case variant null", `"knowledge":"notes","knowledge_fallback":"model","Knowledge_Fallback":null`, "", "knowledge_fallback"},
		{"case variant empty", `"knowledge":"notes","KNOWLEDGE_FALLBACK":"","knowledge_fallback":"model"`, "", "knowledge_fallback"},
		{"boolean", `"knowledge":"notes","knowledge_fallback":true`, "", "knowledge_fallback"},
		{"missing knowledge", `"knowledge_fallback":"model"`, "", "requires knowledge"},
		{"disabled without knowledge", `"knowledge_fallback":"none"`, "", "requires knowledge"},
		{"unknown knowledge", `"knowledge":"missing","knowledge_fallback":"model"`, "", "unknown knowledge base"},
		{"vision", `"knowledge":"notes","knowledge_fallback":"model","capabilities":["chat","vision"]`, "", "text chat only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"providers":{"local":{"type":"codex"}},"knowledge_bases":{"notes":{"root":"notes"}},"models":{"infra":{"provider":"local",` + tc.fields + `}}}`
			c, err := loadKnowledgeConfig(t, body)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("want error containing %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Models["infra"].KnowledgeFallback; got != tc.want {
				t.Fatalf("fallback = %q, want %q", got, tc.want)
			}
			encoded, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := loadKnowledgeConfig(t, string(encoded))
			if err != nil || reloaded.Models["infra"].KnowledgeFallback != tc.want {
				t.Fatalf("fallback failed config round trip: %v", err)
			}
		})
	}
	for _, model := range []Model{
		{Provider: "codex", Knowledge: "notes", KnowledgeFallback: "invalid"},
		{Provider: "codex", KnowledgeFallback: "model"},
	} {
		c := Default()
		base := DefaultKnowledgeBase()
		base.Root = "notes"
		c.KnowledgeBases = map[string]KnowledgeBase{"notes": base}
		c.Models = map[string]Model{"infra": model}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "knowledge_fallback") {
			t.Fatalf("programmatic invalid fallback accepted: %v", err)
		}
	}
}
