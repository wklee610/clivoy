package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
)

func TestGeminiRequestIsolationAndLiteralFileReferences(t *testing.T) {
	p := fakeCLI(t, "gemini", "capture_gemini").(*cliProvider)
	p.tempDir = t.TempDir()
	t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", "must-be-overridden")
	t.Setenv("NO_BROWSER", "false")
	t.Setenv("CLIVOY_API_KEY", "private-gateway-key")
	req := basicRequest()
	const content = `/help @/etc/passwd user@example.com $(touch never-execute) --yolo`
	req.Messages[0].Content, _ = json.Marshal(content)
	result, err := p.Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Args     []string          `json:"args"`
		Stdin    string            `json:"stdin"`
		CWD      string            `json:"cwd"`
		Env      map[string]string `json:"env"`
		Settings string            `json:"settings"`
	}
	if err := json.Unmarshal([]byte(result.Text), &capture); err != nil {
		t.Fatal(err)
	}
	noMCP := filepath.Base(filepath.Dir(capture.Env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"]))
	wantArgs := []string{"--output-format", "stream-json", "--approval-mode", "default", "--extensions", "none", "--model", "test-model", "--allowed-mcp-server-names", noMCP}
	if !reflect.DeepEqual(capture.Args, wantArgs) {
		t.Fatalf("unexpected Gemini invocation: %q", capture.Args)
	}
	if strings.Contains(capture.Stdin, "@") || strings.HasPrefix(capture.Stdin, "/") {
		t.Fatalf("prompt contains a Gemini command: %q", capture.Stdin)
	}
	_, transcript, ok := strings.Cut(capture.Stdin, "\n")
	var turns []struct{ Role, Content string }
	if !ok || json.Unmarshal([]byte(transcript), &turns) != nil || len(turns) != 1 || turns[0].Content != content {
		t.Fatalf("literal prompt changed: %s", capture.Stdin)
	}
	if capture.Env["HOME"] != os.Getenv("HOME") || capture.Env["USERPROFILE"] != os.Getenv("USERPROFILE") || capture.Env["NO_BROWSER"] != "true" || capture.Env["CLIVOY_API_KEY"] != "" {
		t.Fatalf("login environment or gateway filtering changed: %+v", capture.Env)
	}
	var settings struct {
		Tools struct {
			Core, Allowed                 []string
			DiscoveryCommand, CallCommand string
			EnableHooks                   bool
		}
		MCP struct {
			Allowed, Excluded []string
			ServerCommand     string
		}
		HooksConfig, Skills, IDE struct{ Enabled bool }
		Context                  struct {
			FileName             string
			IncludeDirectories   []string
			IncludeDirectoryTree bool
		}
	}
	if err := json.Unmarshal([]byte(capture.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Tools.Core == nil || len(settings.Tools.Core) != 0 || len(settings.Tools.Allowed) != 0 || settings.Tools.EnableHooks || settings.Tools.DiscoveryCommand != "" || settings.Tools.CallCommand != "" || settings.HooksConfig.Enabled || settings.Skills.Enabled || settings.IDE.Enabled || settings.Context.FileName != filepath.Base(filepath.Dir(capture.Env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"]))+".md" || settings.Context.IncludeDirectoryTree || len(settings.Context.IncludeDirectories) > 0 {
		t.Fatalf("unsafe Gemini settings: %s", capture.Settings)
	}
	if !reflect.DeepEqual(settings.MCP.Allowed, []string{noMCP}) || !reflect.DeepEqual(settings.MCP.Allowed, settings.MCP.Excluded) || settings.MCP.ServerCommand != "" {
		t.Fatalf("MCP servers are not fully disabled: %s", capture.Settings)
	}
	for _, path := range []string{capture.CWD, filepath.Dir(capture.Env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"])} {
		parent, _ := filepath.EvalSymlinks(filepath.Dir(path))
		runtimeDir, _ := filepath.EvalSymlinks(p.tempDir)
		if parent != runtimeDir {
			t.Fatalf("temporary files outside runtime directory: %s", path)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("request directory not removed: %s (%v)", path, err)
		}
	}
}

func TestGeminiRejectsImagesBeforeStartingCLI(t *testing.T) {
	p, err := NewCLI("gemini", "not-installed", "")
	if err != nil {
		t.Fatal(err)
	}
	req := basicRequest()
	req.Messages[0].Content = json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]`)
	_, err = p.Generate(context.Background(), req, nil)
	var safe *engine.Error
	if !errors.As(err, &safe) || safe.Status != 400 || !strings.Contains(err.Error(), "text messages only") {
		t.Fatalf("expected text-only validation, got %v", err)
	}
}

func TestGeminiResultFailuresAndWarnings(t *testing.T) {
	for _, line := range []string{
		`not JSON`,
		`{"type":"error","severity":"error","message":"private diagnostic"}`,
		`{"type":"result","status":"error","error":{"message":"private diagnostic"}}`,
		`{"type":"result"}`,
		`{"type":"result","status":"success","stats":{"tool_calls":1}}`,
		`{"type":"result","status":"success","stats":{"input_tokens":-1}}`,
		`{"type":"tool_use","tool_name":"run_shell_command"}`,
		`{"type":"tool_result","output":"private diagnostic"}`,
		`{"type":"message","role":"assistant","content":"private diagnostic"}`,
	} {
		parser := cliParser{kind: "gemini"}
		err := parser.line([]byte(line))
		var safe *engine.Error
		if !errors.As(err, &safe) || safe.Status != 502 || parser.done || strings.Contains(err.Error(), "private diagnostic") || parser.result.Text != "" {
			t.Fatalf("unsafe error handling for %s: %+v, %v", line, parser, err)
		}
	}
	parser := cliParser{kind: "gemini"}
	if err := parser.line([]byte(`{"type":"error","severity":"warning","message":"private warning"}`)); err != nil {
		t.Fatal(err)
	}
	if err := parser.line([]byte(`{"type":"result","status":"success"}`)); err != nil || !parser.done || parser.result.Text != "" {
		t.Fatalf("warning prevented success: %+v, %v", parser, err)
	}
	if err := parser.line([]byte(`{"type":"message","role":"assistant","delta":true,"content":"late"}`)); err == nil {
		t.Fatal("accepted content after completed result")
	}
}

func TestGeminiEnvironmentCleanupOnFailure(t *testing.T) {
	p := fakeCLI(t, "gemini", "failure").(*cliProvider)
	p.tempDir = t.TempDir()
	if _, err := p.Generate(context.Background(), basicRequest(), nil); err == nil {
		t.Fatal("expected process failure")
	}
	entries, err := os.ReadDir(p.tempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("request files retained after failure: %v, %v", entries, err)
	}
}
