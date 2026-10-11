package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/state"
)

func TestLoadDefaultReadsManagedConfigWithoutCreatingState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "clivoy")
	t.Setenv("CLIVOY_HOME", root)
	if _, err := Load(""); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "clivoy init") {
		t.Fatalf("missing default config: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("Load created state: %v", err)
	}
	paths, err := state.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	c := Default()
	c.Listen = "127.0.0.1:9999"
	c.AuthFile = filepath.Base(paths.Auth)
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Auth, []byte(`{"keys":["managed-test-key"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(c.APIKeyEnv, "")
	t.Chdir(t.TempDir())
	loaded, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Listen != c.Listen {
		t.Fatal("Load did not read the managed config")
	}
	if key, err := loaded.ResolveAPIKey(); err != nil || key != "managed-test-key" {
		t.Fatalf("managed key: %q, %v", key, err)
	}
	if dataAfter, err := os.ReadFile(paths.Config); err != nil || string(dataAfter) != string(data) {
		t.Fatal("Load changed the config")
	}
}

func TestExplicitConfigDoesNotRequireStateHome(t *testing.T) {
	t.Setenv("CLIVOY_HOME", "invalid-relative-home")
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.LogFile = "-"
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if log, err := loaded.ResolveLogFile(); err != nil || log != "-" {
		t.Fatalf("explicit logging opt-out: %q, %v", log, err)
	}
	if _, err := Load(""); err == nil {
		t.Fatal("default load accepted a relative CLIVOY_HOME")
	}
}

func TestResolveLogFile(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("CLIVOY_HOME", stateRoot)
	configDirectory := t.TempDir()
	absoluteLog := filepath.Join(t.TempDir(), "custom.log")
	for _, tc := range []struct{ name, setting, expected string }{
		{"default", "", filepath.Join(stateRoot, "logs", "clivoy.log")},
		{"disabled", "-", "-"},
		{"relative", filepath.Join("logs", "custom.log"), filepath.Join(configDirectory, "logs", "custom.log")},
		{"absolute", absoluteLog, absoluteLog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.LogFile = tc.setting
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(configDirectory, "config.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			if actual, err := loaded.ResolveLogFile(); err != nil || actual != tc.expected {
				t.Fatalf("log destination=%q expected=%q error=%v", actual, tc.expected, err)
			}
		})
	}
	if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("resolving log destination changed state: %v", err)
	}
	c := Default()
	c.LogFile = " \n\t"
	if err := c.Validate(); err == nil {
		t.Fatal("accepted whitespace-only log destination")
	}
	if _, err := c.ResolveLogFile(); err == nil {
		t.Fatal("resolved whitespace-only log destination")
	}
}

func TestLoadReplacesProvidersAndRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"configured maps replace defaults", `{"providers":{"test":{"type":"openai","base_url":"http://localhost:8000/v1"}},"models":{"alias":{"provider":"test","model":"upstream"}}}`, true},
		{"unknown setting", `{"max_concurent":2}`, false},
		{"unknown provider", `{"providers":{"test":{"type":"openai","base_url":"http://localhost/v1"}},"models":{"alias":{"provider":"missing"}}}`, false},
		{"unknown type", `{"providers":{"test":{"type":"shell"}},"models":{"alias":{"provider":"test"}}}`, false},
		{"zero concurrency", `{"max_concurrent":0,"providers":{"test":{"type":"codex"}},"models":{"alias":{"provider":"test"}}}`, false},
		{"invalid duration", `{"request_timeout":"0s","providers":{"test":{"type":"codex"}},"models":{"alias":{"provider":"test"}}}`, false},
		{"multiple objects", `{} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid {
				if len(c.Providers) != 1 || len(c.Models) != 1 {
					t.Fatal("default maps leaked into explicit config")
				}
				if c.RequestTimeout != "5m" {
					t.Fatal("scalar defaults were lost")
				}
			}
		})
	}
}

func TestResolveAPIKeyRelativeToConfigAndEnvironmentPrecedence(t *testing.T) {
	c := Default()
	c.APIKeyFile = "server.key"
	configDirectory := t.TempDir()
	keyPath := filepath.Join(configDirectory, c.APIKeyFile)
	if err := os.WriteFile(keyPath, []byte("  file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDirectory, "clivoy.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Setenv(c.APIKeyEnv, "")
	if key, err := loaded.ResolveAPIKey(); err != nil || key != "file-token" {
		t.Fatalf("relative key: %q, %v", key, err)
	}
	t.Setenv(c.APIKeyEnv, "  env-token\n")
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if key, err := loaded.ResolveAPIKey(); err != nil || key != "env-token" {
		t.Fatalf("environment override: %q, %v", key, err)
	}
	t.Setenv(c.APIKeyEnv, " \n")
	if _, err := loaded.ResolveAPIKey(); err == nil {
		t.Fatal("missing file with no nonempty environment key was accepted")
	}
}

func TestResolveAPIKeyRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"empty", "oversized", "directory", "symlink", "group-access", "other-access"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && (kind == "group-access" || kind == "other-access") {
				t.Skip("Windows uses ACLs rather than Unix mode bits")
			}
			c := Default()
			t.Setenv(c.APIKeyEnv, "")
			c.APIKeyFile = filepath.Join(t.TempDir(), "server.key")
			switch kind {
			case "directory":
				if err := os.Mkdir(c.APIKeyFile, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.WriteFile(c.APIKeyFile+".target", []byte("file-token"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(c.APIKeyFile+".target", c.APIKeyFile); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			default:
				body := "file-token"
				mode := os.FileMode(0600)
				if kind == "empty" {
					body = " \n\t"
				} else if kind == "oversized" {
					body = strings.Repeat("x", 4097)
				} else if kind == "group-access" {
					mode = 0640
				} else if kind == "other-access" {
					mode = 0604
				}
				if err := os.WriteFile(c.APIKeyFile, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(c.APIKeyFile, mode); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.ResolveAPIKey(); err == nil {
				t.Fatalf("accepted %s key file", kind)
			}
		})
	}
}

func TestFileOnlyAPIKeyConfiguration(t *testing.T) {
	c := Default()
	c.APIKeyEnv = ""
	c.APIKeyFile = filepath.Join(t.TempDir(), "server.key")
	if err := os.WriteFile(c.APIKeyFile, []byte("file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if key, err := c.ResolveAPIKey(); err != nil || key != "file-token" {
		t.Fatalf("file-only key: %q, %v", key, err)
	}
	c.APIKeyFile = ""
	if err := c.Validate(); err == nil {
		t.Fatal("accepted configuration with no key source")
	}
}

func TestResolveAPIKeysRelativeAuthFileAndPrecedence(t *testing.T) {
	c := Default()
	if c.AuthFile != "" {
		t.Fatal("default config unexpectedly opts into an auth file")
	}
	c.AuthFile, c.APIKeyFile = "auth.json", "legacy.key"
	dir := t.TempDir()
	authPath := filepath.Join(dir, c.AuthFile)
	if err := os.WriteFile(authPath, []byte(`{"keys":["first-token","second-token"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, c.APIKeyFile), []byte("legacy-token"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Setenv(c.APIKeyEnv, " \n\t")
	if keys, err := loaded.ResolveAPIKeys(); err != nil || !reflect.DeepEqual(keys, []string{"first-token", "second-token"}) {
		t.Fatalf("auth file keys did not retain order: %v", err)
	}
	if key, err := loaded.ResolveAPIKey(); err != nil || key != "first-token" {
		t.Fatalf("single-key compatibility did not select the first key: %v", err)
	}
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv(c.APIKeyEnv, "  override-token\n")
	if keys, err := loaded.ResolveAPIKeys(); err != nil || !reflect.DeepEqual(keys, []string{"override-token"}) {
		t.Fatalf("environment override must replace all file keys: %v", err)
	}
	t.Setenv(c.APIKeyEnv, "")
	if _, err := loaded.ResolveAPIKeys(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing auth file fell back to the legacy file: %v", err)
	}
	loaded.AuthFile = ""
	if keys, err := loaded.ResolveAPIKeys(); err != nil || !reflect.DeepEqual(keys, []string{"legacy-token"}) {
		t.Fatalf("legacy fallback failed when auth_file was omitted: %v", err)
	}
}

func TestResolveAPIKeysRejectsInvalidAuthJSONWithoutLegacyFallback(t *testing.T) {
	const secret = "private-test-token"
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"whitespace", " \n\t"},
		{"malformed", `{"keys":["` + secret + `"`},
		{"wrong top-level type", `["` + secret + `"]`},
		{"null object", `null`},
		{"missing keys", `{}`},
		{"null keys", `{"keys":null}`},
		{"empty keys", `{"keys":[]}`},
		{"empty key", `{"keys":["` + secret + `",""]}`},
		{"whitespace key", `{"keys":["` + secret + `"," \n\t"]}`},
		{"surrounding whitespace", `{"keys":[" ` + secret + `"]}`},
		{"embedded whitespace", `{"keys":["private test-token"]}`},
		{"unicode whitespace", `{"keys":["private\u00a0test-token"]}`},
		{"null control character", `{"keys":["private\u0000test-token"]}`},
		{"ASCII delete character", `{"keys":["private\u007ftest-token"]}`},
		{"unicode control character", `{"keys":["private\u009ftest-token"]}`},
		{"null key", `{"keys":["` + secret + `",null]}`},
		{"wrong keys type", `{"keys":"` + secret + `"}`},
		{"wrong key type", `{"keys":["` + secret + `",42]}`},
		{"unknown field", `{"keys":["` + secret + `"],"` + secret + `":true}`},
		{"duplicate keys field", `{"keys":["` + secret + `"],"keys":["other"]}`},
		{"wrong field case", `{"Keys":["` + secret + `"]}`},
		{"second object", `{"keys":["` + secret + `"]} {}`},
		{"trailing junk", `{"keys":["` + secret + `"]} ` + secret},
		{"too many keys", `{"keys":[` + strings.Repeat(`"test-token",`, 128) + `"test-token"]}`},
		{"oversized", `{"keys":["` + secret + `"]}` + strings.Repeat(" ", 64<<10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			t.Setenv(c.APIKeyEnv, "")
			c.AuthFile = filepath.Join(t.TempDir(), "auth.json")
			c.APIKeyFile = filepath.Join(t.TempDir(), "legacy.key")
			if err := os.WriteFile(c.AuthFile, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.APIKeyFile, []byte("legacy-token"), 0600); err != nil {
				t.Fatal(err)
			}
			keys, err := c.ResolveAPIKeys()
			if err == nil || len(keys) != 0 {
				t.Fatal("invalid auth file was accepted or used the legacy fallback")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("auth file error exposed key material")
			}
		})
	}
}

func TestResolveAPIKeysRejectsUnsafeAuthFiles(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "group-access", "other-access"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && (kind == "group-access" || kind == "other-access") {
				t.Skip("Windows uses ACLs rather than Unix mode bits")
			}
			c := Default()
			t.Setenv(c.APIKeyEnv, "")
			c.AuthFile = filepath.Join(t.TempDir(), "auth.json")
			c.APIKeyFile = filepath.Join(t.TempDir(), "legacy.key")
			if err := os.WriteFile(c.APIKeyFile, []byte("legacy-token"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(c.AuthFile, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.WriteFile(c.AuthFile+".target", []byte(`{"keys":["file-token"]}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(c.AuthFile+".target", c.AuthFile); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			default:
				if err := os.WriteFile(c.AuthFile, []byte(`{"keys":["file-token"]}`), 0600); err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0640)
				if kind == "other-access" {
					mode = 0604
				}
				if err := os.Chmod(c.AuthFile, mode); err != nil {
					t.Fatal(err)
				}
			}
			if keys, err := c.ResolveAPIKeys(); err == nil || len(keys) != 0 {
				t.Fatalf("accepted %s auth file or used legacy fallback", kind)
			}
		})
	}
}

func TestAuthFileOnlyAndValidBoundaries(t *testing.T) {
	c := Default()
	c.APIKeyEnv = ""
	c.AuthFile = filepath.Join(t.TempDir(), "auth.json")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	// Keys have no required prefix or individual length limit. File and count
	// limits remain inclusive, and JSON formatting may contain whitespace.
	keys := []string{strings.Repeat("x", 5000), "a", "custom:key/+="}
	for len(keys) < 128 {
		keys = append(keys, "another-test-token")
	}
	data, err := json.Marshal(map[string][]string{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte(strings.Repeat(" ", (64<<10)-len(data)))...)
	if err := os.WriteFile(c.AuthFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	if actual, err := c.ResolveAPIKeys(); err != nil || !reflect.DeepEqual(actual, keys) {
		t.Fatalf("valid auth keys at the file/count limits were rejected: %v", err)
	}
	c.AuthFile = " \t"
	if err := c.Validate(); err == nil {
		t.Fatal("accepted a whitespace auth path")
	}
	c.AuthFile = ""
	if err := c.Validate(); err == nil {
		t.Fatal("accepted config without an authentication source")
	}
}

func TestModelCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, provider, capabilities string
		valid                        bool
	}{
		{"omitted", "codex", "", true},
		{"chat", "claude", `,"capabilities":["chat"]`, true},
		{"Gemini chat", "gemini", `,"capabilities":["chat"]`, true},
		{"Grok chat", "grok", `,"capabilities":["chat"]`, true},
		{"Gemini cannot analyze images", "gemini", `,"capabilities":["chat","vision"]`, false},
		{"Grok CLI cannot analyze images", "grok", `,"capabilities":["chat","vision"]`, false},
		{"vision", "codex", `,"capabilities":["chat","vision"]`, true},
		{"image backend", "openai", `,"capabilities":["image_generation"]`, true},
		{"all HTTP", "openai", `,"capabilities":["chat","vision","image_generation"]`, true},
		{"empty list", "codex", `,"capabilities":[]`, false},
		{"null", "codex", `,"capabilities":null`, false},
		{"empty value", "codex", `,"capabilities":[""]`, false},
		{"unknown", "codex", `,"capabilities":["audio"]`, false},
		{"duplicate", "codex", `,"capabilities":["chat","chat"]`, false},
		{"vision requires chat", "codex", `,"capabilities":["vision"]`, false},
		{"CLI cannot generate images", "codex", `,"capabilities":["image_generation"]`, false},
		{"unknown model field", "codex", `,"capabilties":["chat"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := `{"type":"` + tc.provider + `"}`
			if tc.provider == "openai" {
				provider = `{"type":"openai","base_url":"http://localhost:8000/v1"}`
			}
			body := `{"providers":{"test":` + provider + `},"models":{"alias":{"provider":"test","model":"test-model"` + tc.capabilities + `}}}`
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.name == "omitted" && !reflect.DeepEqual(c.Models["alias"].Capabilities, []string{"chat"}) {
				t.Fatalf("unexpected default: %v", c.Models["alias"].Capabilities)
			}
		})
	}
	c := Default()
	for _, name := range []string{"codex", "claude"} {
		if !reflect.DeepEqual(c.Models[name].Capabilities, []string{"chat", "vision"}) {
			t.Fatalf("default %s capabilities: %v", name, c.Models[name].Capabilities)
		}
	}
	for _, name := range []string{"gemini", "grok"} {
		if !c.Providers[name].IsCLI() || !reflect.DeepEqual(c.Models[name].Capabilities, []string{"chat"}) {
			t.Fatalf("default %s must be a text CLI: %+v", name, c.Models[name])
		}
	}
	if c.MaxBodyBytes != 32<<20 {
		t.Fatalf("default max body bytes: %d", c.MaxBodyBytes)
	}
	c.MaxBodyBytes = (64 << 20) + 1
	if err := c.Validate(); err == nil {
		t.Fatal("body size above 64 MiB accepted")
	}
}

func TestExampleConfigurations(t *testing.T) {
	paths, err := filepath.Glob("../../examples/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("examples: %v", err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := Load(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGrokCLIRejectsPersistentProjectDirectory(t *testing.T) {
	c := Default()
	p := c.Providers["grok"]
	p.WorkDir = t.TempDir()
	c.Providers["grok"] = p
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "omit work_dir") {
		t.Fatalf("Grok must not load project hooks or MCP settings: %v", err)
	}
}
