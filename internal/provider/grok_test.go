package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
)

// Use the test binary as a subprocess; never invoke an installed Grok or make
// an inference request. A separate marker leaves the shared CLI fixture alone.
func init() {
	if os.Getenv("CLIVOY_TEST_GROK_CHILD") == "" {
		return
	}
	promptPath := ""
	for i, arg := range os.Args {
		if arg == "--prompt-file" && i+1 < len(os.Args) {
			promptPath = os.Args[i+1]
		}
	}
	prompt, _ := os.ReadFile(promptPath)
	cwd, _ := os.Getwd()
	auth, _ := os.ReadFile(os.Getenv("GROK_AUTH_PATH"))
	if os.Getenv("CLIVOY_TEST_GROK_CHILD") == "refresh" {
		_ = os.WriteFile(os.Getenv("GROK_AUTH_PATH"), []byte(`{"refreshed":true}`), 0600)
	}
	data, _ := json.Marshal(map[string]any{
		"args": os.Args[1:], "prompt": string(prompt), "prompt_path": promptPath,
		"cwd": cwd, "grok_home": os.Getenv("GROK_HOME"), "auth": string(auth), "auth_path": os.Getenv("GROK_AUTH_PATH"),
		"xai_key": os.Getenv("XAI_API_KEY"), "gateway_key": os.Getenv("CLIVOY_API_KEY"),
		"grok_config": os.Getenv("GROK_CONFIG"), "grok_agent": os.Getenv("GROK_AGENT"),
	})
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "text", "data": string(data)})
	fmt.Println(`{"type":"end","stopReason":"end_turn","usage":{"input_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":1,"output_tokens":7}}`)
	os.Exit(0)
}

func TestGrokCLIIsolatesRequestAndPreservesNativeLoginRefresh(t *testing.T) {
	t.Setenv("CLIVOY_TEST_GROK_CHILD", "refresh")
	t.Setenv("CLIVOY_API_KEY", "gateway-secret")
	t.Setenv("XAI_API_KEY", "upstream-test-key")
	t.Setenv("GROK_CONFIG", `{"features":{"backend_tools":true}}`)
	t.Setenv("GROK_AGENT", "/should/not/load.md")
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_AUTH_PATH", "")
	login := `{"test-login":{"key":"synthetic-credential"}}`
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(login), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("do-not-import = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewCLI("grok", []string{binary}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := basicRequest()
	req.Model = "grok-4.7"
	req.Messages[0].Content = json.RawMessage(`"$(touch never-run); @/private/file"`)
	var streamed strings.Builder
	result, err := backend.Generate(context.Background(), req, func(d engine.Delta) error {
		streamed.WriteString(d.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Args       []string `json:"args"`
		Prompt     string   `json:"prompt"`
		PromptPath string   `json:"prompt_path"`
		CWD        string   `json:"cwd"`
		GrokHome   string   `json:"grok_home"`
		Auth       string   `json:"auth"`
		AuthPath   string   `json:"auth_path"`
		XAIKey     string   `json:"xai_key"`
		GatewayKey string   `json:"gateway_key"`
		GrokConfig string   `json:"grok_config"`
		GrokAgent  string   `json:"grok_agent"`
	}
	if err := json.Unmarshal([]byte(result.Text), &capture); err != nil {
		t.Fatal(err)
	}
	if result.Text != streamed.String() || result.Usage.InputTokens != 6 || result.Usage.OutputTokens != 7 || result.FinishReason != "stop" {
		t.Fatalf("incorrect streamed result: %+v", result)
	}
	joined := strings.Join(capture.Args, " ")
	for _, expected := range []string{
		"--model grok-4.7", "--tools read_file", "--disallowed-tools read_file,search_tool,use_tool",
		"--permission-mode dontAsk", "--sandbox clivoy-read-only", "--max-turns 1", "--verbatim", "--no-leader",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing restrictive Grok argument %q", expected)
		}
	}
	if strings.Contains(joined, "never-run") || !strings.Contains(capture.Prompt, "$(touch never-run)") {
		t.Fatal("request text was not confined to the prompt file")
	}
	if strings.Contains(capture.Prompt, "@") || !strings.Contains(capture.Prompt, `\u0040/private/file`) {
		t.Fatal("Grok prompt could expand an API caller's local file reference")
	}
	if capture.Auth != login || capture.XAIKey != "upstream-test-key" || capture.GatewayKey != "" || capture.GrokConfig != "" || capture.GrokAgent != "" {
		t.Fatal("Grok credential/environment isolation failed")
	}
	if capture.GrokHome == home {
		t.Fatal("original Grok home was used by the child")
	}
	canonicalHome, _ := filepath.EvalSymlinks(home)
	if capture.AuthPath != filepath.Join(canonicalHome, "auth.json") {
		t.Fatal("native login file was relocated")
	}
	for _, path := range []string{capture.PromptPath, capture.GrokHome, capture.CWD} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("temporary Grok request path survived: %s (%v)", path, err)
		}
	}
	refreshed, _ := os.ReadFile(filepath.Join(home, "auth.json"))
	if string(refreshed) != `{"refreshed":true}` {
		t.Fatal("native credential refresh was discarded")
	}
}

func TestGrokEnvironmentWithoutStoredLogin(t *testing.T) {
	home := t.TempDir()
	p := &cliProvider{kind: "grok", tempDir: t.TempDir()}
	env, cleanup, err := p.prepareGrokEnvironment([]string{"GROK_HOME=" + home, "XAI_API_KEY=test", "GROK_CONFIG_PATH=/unsafe/config", "GROK_CURSOR_HOOKS_ENABLED=1"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	values := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	if values["XAI_API_KEY"] != "test" || values["GROK_CONFIG_PATH"] != "" || values["GROK_CURSOR_HOOKS_ENABLED"] != "0" || values["GROK_BACKEND_SEARCH"] != "0" {
		t.Fatal("unsafe or missing environment override")
	}
	configBytes, err := os.ReadFile(filepath.Join(values["GROK_HOME"], "config.toml"))
	if err != nil || string(configBytes) != grokIsolatedConfig {
		t.Fatalf("isolated config missing: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(values["GROK_HOME"])
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("Grok home is not private: %v", err)
		}
	}
}

func TestGrokEnvironmentKeepsExplicitNativeAuthPath(t *testing.T) {
	home, authDir := t.TempDir(), t.TempDir()
	authPath := filepath.Join(authDir, "custom-auth.json")
	p := &cliProvider{kind: "grok", tempDir: t.TempDir()}
	env, cleanup, err := p.prepareGrokEnvironment([]string{"GROK_HOME=" + home, "GROK_AUTH_PATH=" + authPath})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	values := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	canonical, _ := filepath.EvalSymlinks(authDir)
	if values["GROK_AUTH_PATH"] != filepath.Join(canonical, "custom-auth.json") {
		t.Fatal("explicit native authentication path was not preserved")
	}
	profile, err := os.ReadFile(filepath.Join(values["GROK_HOME"], "sandbox.toml"))
	encodedDir, _ := json.Marshal(canonical)
	if err != nil || !strings.Contains(string(profile), `extends = "read-only"`) || !strings.Contains(string(profile), "read_write = ["+string(encodedDir)+"]") {
		t.Fatalf("native auth refresh directory missing from sandbox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(values["GROK_HOME"], "auth.json")); !os.IsNotExist(err) {
		t.Fatal("credentials must not be copied into the temporary home")
	}
}

func TestGrokEnvironmentRejectsNonRegularAuthBeforeOpening(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "auth.json"), 0700); err != nil {
		t.Fatal(err)
	}
	p := &cliProvider{kind: "grok", tempDir: t.TempDir()}
	if _, cleanup, err := p.prepareGrokEnvironment([]string{"GROK_HOME=" + home}); err == nil || cleanup != nil {
		t.Fatal("non-regular auth path accepted")
	}
}

func TestGrokIsolationRejectsManagedPolicy(t *testing.T) {
	for _, file := range []string{"requirements.toml", "managed_config.toml"} {
		t.Run(file, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, file), []byte("secret-invalid-content"), 0600); err != nil {
				t.Fatal(err)
			}
			p := &cliProvider{kind: "grok", tempDir: t.TempDir()}
			_, cleanup, err := p.prepareGrokEnvironment([]string{"GROK_HOME=" + home})
			if cleanup != nil {
				cleanup()
			}
			if err == nil || strings.Contains(err.Error(), "secret-invalid-content") {
				t.Fatalf("expected sanitized isolation failure, got %v", err)
			}
			entries, _ := os.ReadDir(p.tempDir)
			if len(entries) != 0 {
				t.Fatal("failed preparation left temporary files")
			}
		})
	}
}

func TestGrokParserStreamingUsageAndFinish(t *testing.T) {
	var emitted strings.Builder
	p := &cliParser{kind: "grok", emit: func(d engine.Delta) error { emitted.WriteString(d.Text); return nil }}
	for _, line := range []string{
		`{"type":"status","data":{"message":"starting"}}`,
		`{"type":"thought","data":"do not expose this"}`,
		`{"type":"text","data":"Hel"}`,
		`{"type":"text","data":"lo"}`,
		`{"type":"usage","usage":{"input_tokens":2,"cache_read_input_tokens":5,"output_tokens":3}}`,
		`{"type":"end","stopReason":"max_tokens","usage":{"input_tokens":4,"cache_read_input_tokens":6,"cache_creation_input_tokens":1,"output_tokens":8}}`,
	} {
		if err := p.line([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if !p.done || p.result.Text != "Hello" || emitted.String() != "Hello" || p.result.FinishReason != "length" || p.result.Usage.InputTokens != 11 || p.result.Usage.OutputTokens != 8 {
		t.Fatalf("unexpected Grok parse: %+v", p.result)
	}
}

func TestGrokParserRejectsFailuresAndMissingCompletion(t *testing.T) {
	for _, line := range []string{
		`{`, `{}`, `{"type":"error","message":"private-credential"}`,
		`{"type":"text","data":null}`, `{"type":"text","data":{"unsupported":true}}`,
		`{"type":"tool_call","toolName":"read_file"}`, `{"type":"tool_call_update"}`,
		`{"type":"end"}`, `{"type":"end","stopReason":"cancelled"}`,
		`{"type":"end","stopReason":"max_turns"}`, `{"type":"usage","usage":{"input_tokens":-1}}`,
	} {
		p := &cliParser{kind: "grok"}
		if err := p.line([]byte(line)); err == nil || strings.Contains(err.Error(), "private-credential") {
			t.Errorf("expected sanitized error for %s: %v", line, err)
		}
	}
	p := &cliParser{kind: "grok"}
	if err := p.line([]byte(`{"type":"text","data":"partial"}`)); err != nil || p.done {
		t.Fatal("partial text incorrectly marked complete")
	}
	if err := p.line([]byte(`{"type":"end","stopReason":"end_turn"}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.line([]byte(`{"type":"text","data":"late"}`)); err == nil {
		t.Fatal("content after completion accepted")
	}
	want := errors.New("emitter stopped")
	p = &cliParser{kind: "grok", emit: func(engine.Delta) error { return want }}
	if err := p.line([]byte(`{"type":"text","data":"text"}`)); !errors.Is(err, want) {
		t.Fatalf("emitter error lost: %v", err)
	}
}
