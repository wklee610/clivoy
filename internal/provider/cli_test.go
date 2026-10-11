package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

// The test executable is also the fake CLI, so no model or installed CLI is
// called and command execution is tested without a shell on every platform.
func init() {
	mode := os.Getenv("CLIVOY_TEST_PROVIDER_CHILD")
	if mode == "" {
		return
	}
	input, _ := io.ReadAll(os.Stdin)
	write := func(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
	switch mode {
	case "codex":
		write(map[string]any{"type": "item.completed", "item": map[string]string{"type": "reasoning", "text": "private thoughts"}})
		write(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": "Hello"}})
		write(map[string]any{"type": "turn.completed", "usage": map[string]int{"input_tokens": 12, "output_tokens": 3}})
	case "claude":
		fmt.Println(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}}`)
		fmt.Println(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}`)
		fmt.Println(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`)
		fmt.Println(`{"type":"result","subtype":"success","result":"Hello","usage":{"input_tokens":2,"cache_read_input_tokens":10,"output_tokens":3}}`)
	case "gemini":
		fmt.Println(`{"type":"init","session_id":"private-session","model":"gemini-test"}`)
		fmt.Println(`{"type":"message","role":"user","content":"private prompt"}`)
		fmt.Println(`{"type":"message","role":"assistant","content":"Hel","delta":true}`)
		fmt.Println(`{"type":"message","role":"assistant","content":"lo","delta":true}`)
		fmt.Println(`{"type":"result","status":"success","stats":{"input_tokens":12,"output_tokens":3,"cached":10,"tool_calls":0}}`)
	case "claude_not_logged_in":
		fmt.Fprintln(os.Stderr, "secret-provider-token-and-private-diagnostics")
		fmt.Println(`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login","num_turns":1}`)
		os.Exit(1)
	case "capture", "capture_claude", "capture_gemini":
		cwd, _ := os.Getwd()
		selectedEnv := map[string]string{}
		for _, key := range []string{"CLIVOY_API_KEY", "CLIVOY_TEST_CUSTOM_KEY", "clivoy_test_http_key", "ANTHROPIC_API_KEY", "CLIVOY_TEST_KEEP", "HOME", "USERPROFILE", "NO_BROWSER", "GEMINI_CLI_SYSTEM_SETTINGS_PATH"} {
			selectedEnv[key] = os.Getenv(key)
		}
		var images []map[string]string
		for i, arg := range os.Args {
			if arg == "--image" && i+1 < len(os.Args) {
				data, _ := os.ReadFile(os.Args[i+1])
				images = append(images, map[string]string{"path": os.Args[i+1], "data": base64.StdEncoding.EncodeToString(data)})
			}
		}
		var settings []byte
		if mode == "capture_gemini" {
			settings, _ = os.ReadFile(os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH"))
		}
		capture, _ := json.Marshal(map[string]any{"args": os.Args[1:], "stdin": string(input), "cwd": cwd, "env": selectedEnv, "images": images, "settings": string(settings)})
		if mode == "capture_claude" {
			write(map[string]any{"type": "result", "subtype": "success", "result": string(capture)})
		} else if mode == "capture_gemini" {
			write(map[string]any{"type": "message", "role": "assistant", "content": string(capture), "delta": true})
			fmt.Println(`{"type":"result","status":"success"}`)
		} else {
			write(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": string(capture)}})
			fmt.Println(`{"type":"turn.completed"}`)
		}
	case "image_failure", "image_block":
		for i, arg := range os.Args {
			if arg == "--image" && i+1 < len(os.Args) {
				_ = os.WriteFile(os.Getenv("CLIVOY_TEST_IMAGE_PATH"), []byte(os.Args[i+1]), 0600)
			}
		}
		if mode == "image_block" {
			time.Sleep(30 * time.Second)
		} else {
			os.Exit(17)
		}
	case "failure":
		fmt.Fprintln(os.Stderr, "secret-provider-token-and-private-diagnostics")
		os.Exit(17)
	case "truncated":
		fmt.Println(`{"type":"item.completed","item":{"type":"agent_message","text":"partial"}}`)
	case "oversize":
		fmt.Print(strings.Repeat("x", maxOutputBytes+1))
	case "stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", maxStderrBytes+1))
		time.Sleep(10 * time.Second)
	case "block":
		time.Sleep(30 * time.Second)
	case "tree":
		binary, _ := os.Executable()
		child := exec.Command(binary)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CLIVOY_TEST_PROVIDER_CHILD=") {
				child.Env = append(child.Env, entry)
			}
		}
		child.Env = append(child.Env, "CLIVOY_TEST_PROVIDER_CHILD=heartbeat")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("CLIVOY_TEST_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
		time.Sleep(30 * time.Second)
	case "heartbeat":
		for i := 0; i < 1500; i++ {
			_ = os.WriteFile(os.Getenv("CLIVOY_TEST_HEARTBEAT"), []byte(strconv.Itoa(i)), 0600)
			time.Sleep(20 * time.Millisecond)
		}
	}
	os.Exit(0)
}

func fakeCLI(t *testing.T, kind, mode string) engine.Provider {
	t.Helper()
	t.Setenv("CLIVOY_TEST_PROVIDER_CHILD", mode)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewCLI(kind, binary, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func basicRequest() engine.Request {
	return engine.Request{Model: "test-model", Messages: []engine.Message{{Role: "user", Content: json.RawMessage(`"Hello"`)}}}
}

func TestCLIOutputAndUsage(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "gemini"} {
		t.Run(kind, func(t *testing.T) {
			p := fakeCLI(t, kind, kind)
			var deltas strings.Builder
			result, err := p.Generate(context.Background(), basicRequest(), func(d engine.Delta) error { deltas.WriteString(d.Text); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if result.Text != "Hello" || deltas.String() != "Hello" || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 3 || result.FinishReason != "stop" {
				t.Fatalf("unexpected result: %+v, deltas=%q", result, deltas.String())
			}
		})
	}
}

func TestCLIStdinArgumentsAndTemporaryDirectory(t *testing.T) {
	p := fakeCLI(t, "codex", "capture")
	req := basicRequest()
	req.Messages[0].Content = json.RawMessage(`"$(touch should-not-exist); --dangerously-bypass-approvals-and-sandbox"`)
	limit := 123
	req.MaxTokens = &limit
	result, err := p.Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Args  []string `json:"args"`
		Stdin string   `json:"stdin"`
		CWD   string   `json:"cwd"`
	}
	if err := json.Unmarshal([]byte(result.Text), &capture); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(capture.Args, " ")
	for _, want := range []string{"--sandbox read-only", `approval_policy="never"`, "--ephemeral", "--ignore-user-config", "--ignore-rules"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}
	if strings.Contains(joined, "touch") || !strings.Contains(capture.Stdin, "touch should-not-exist") || !strings.Contains(capture.Stdin, "approximately 123 tokens") {
		t.Fatalf("prompt not passed only through stdin: %+v", capture)
	}
	if _, err := os.Stat(capture.CWD); !os.IsNotExist(err) {
		t.Fatalf("temporary directory was not removed: %v", err)
	}
	claude := (&cliProvider{kind: "claude"}).args(req)
	for _, flag := range []string{"--tools", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence"} {
		found := false
		for _, arg := range claude {
			if arg == flag {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s", flag)
		}
	}
	for i, arg := range claude {
		if arg == "--tools" && claude[i+1] != "" {
			t.Fatal("Claude tools not disabled")
		}
	}
}

func TestCLIFailureAndLimits(t *testing.T) {
	for _, mode := range []string{"failure", "truncated", "oversize", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			p := fakeCLI(t, "codex", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := p.Generate(ctx, basicRequest(), nil)
			var safe *engine.Error
			if !errors.As(err, &safe) || safe.Status != 502 {
				t.Fatalf("expected provider error, got %v", err)
			}
			if strings.Contains(err.Error(), "secret-provider") {
				t.Fatal("stderr leaked")
			}
		})
	}
}

func TestClaudeNotLoggedInResultBeforeProcessExit(t *testing.T) {
	p := fakeCLI(t, "claude", "claude_not_logged_in")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var emitted strings.Builder
	_, err := p.Generate(ctx, basicRequest(), func(d engine.Delta) error { emitted.WriteString(d.Text); return nil })
	var safe *engine.Error
	if !errors.As(err, &safe) || safe.Status != 502 || safe.Message != "Claude CLI is not logged in; run claude locally and sign in" {
		t.Fatalf("expected fixed login guidance despite nonzero exit, got %v", err)
	}
	if emitted.Len() != 0 {
		t.Fatalf("error result emitted as response text: %q", emitted.String())
	}
}

func TestClaudeResultErrorGuidance(t *testing.T) {
	const loginMessage = "Claude CLI is not logged in; run claude locally and sign in"
	const genericMessage = "Claude CLI could not complete the request; check local CLI login, selected model, and configuration"
	for _, tc := range []struct {
		name, subtype, result, want string
		isError                     bool
	}{
		{"login with success subtype", "success", "Not logged in · Please run /login", loginMessage, true},
		{"login prefix with private detail", "error_during_execution", "  Not logged in: secret-provider-token", loginMessage, false},
		{"error flag", "success", "secret-provider-token", genericMessage, true},
		{"error subtype", "error_during_execution", "secret-provider-token", genericMessage, false},
		{"unknown subtype", "future_error", "secret-provider-token", genericMessage, false},
		{"embedded login phrase", "success", "Private failure: Not logged in", genericMessage, true},
		{"successful text with login phrase", "success", "Not logged in is an example message", "", false},
		{"legacy success", "", "Hello", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var emitted strings.Builder
			parser := cliParser{kind: "claude", emit: func(d engine.Delta) error { emitted.WriteString(d.Text); return nil }}
			line, err := json.Marshal(map[string]any{"type": "result", "subtype": tc.subtype, "is_error": tc.isError, "result": tc.result, "errors": []string{"secret-provider-token"}})
			if err != nil {
				t.Fatal(err)
			}
			err = parser.line(line)
			if tc.want == "" {
				if err != nil || !parser.done || parser.result.Text != tc.result || emitted.String() != tc.result {
					t.Fatalf("successful result changed: result=%+v, done=%v, emitted=%q, err=%v", parser.result, parser.done, emitted.String(), err)
				}
				return
			}
			var safe *engine.Error
			if !errors.As(err, &safe) || safe.Status != 502 || safe.Message != tc.want {
				t.Fatalf("expected fixed error %q, got %v", tc.want, err)
			}
			if parser.done || parser.result.Text != "" || emitted.Len() != 0 {
				t.Fatal("error result was treated as successful output")
			}
		})
	}
}

func TestCLICancellationAndEmitterFailure(t *testing.T) {
	t.Run("context", func(t *testing.T) {
		p := fakeCLI(t, "codex", "block")
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := p.Generate(ctx, basicRequest(), nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected error: %v", err)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("cancellation did not promptly stop CLI")
		}
	})
	t.Run("emit", func(t *testing.T) {
		p := fakeCLI(t, "codex", "codex")
		sentinel := errors.New("client disconnected")
		_, err := p.Generate(context.Background(), basicRequest(), func(engine.Delta) error { return sentinel })
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected emitter error: %v", err)
		}
	})
}

func TestCLIRejectsUnsupportedOptions(t *testing.T) {
	p := &cliProvider{kind: "codex"}
	one := 1.0
	requests := []engine.Request{basicRequest(), basicRequest(), basicRequest(), basicRequest()}
	requests[0].Temperature = &one
	requests[1].Tools = []engine.Tool{{Type: "function", Function: engine.Function{Name: "weather"}}}
	requests[2].Messages[0].Role = "tool"
	requests[3].Messages[0].Content = json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.com/p.png"}}]`)
	for _, req := range requests {
		var e *engine.Error
		if !errors.As(p.Validate(req), &e) || e.Status != 400 {
			t.Fatalf("unsupported request accepted: %+v", req)
		}
	}
	if got, err := textContent(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)); err != nil || got != "ab" {
		t.Fatalf("text parts: %s, %v", got, err)
	}
}

func TestMissingCLIIsLazy(t *testing.T) {
	p, err := NewCLI("codex", "clivoy-intentionally-missing-test-binary", "")
	if err != nil {
		t.Fatalf("configuration should not require every CLI to be installed: %v", err)
	}
	_, err = p.Generate(context.Background(), basicRequest(), nil)
	var safe *engine.Error
	if !errors.As(err, &safe) || safe.Status != 503 {
		t.Fatalf("missing executable should return safe unavailable error: %v", err)
	}
}

func TestCLIExcludesGatewaySecretsAndKeepsCLIAuthentication(t *testing.T) {
	t.Setenv("CLIVOY_TEST_PROVIDER_CHILD", "capture")
	t.Setenv("CLIVOY_API_KEY", "fake-gateway-secret")
	t.Setenv("CLIVOY_TEST_CUSTOM_KEY", "fake-custom-secret")
	t.Setenv("clivoy_test_http_key", "fake-http-secret")
	t.Setenv("ANTHROPIC_API_KEY", "fake-existing-cli-auth")
	t.Setenv("CLIVOY_TEST_KEEP", "ordinary-environment")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewCLI("codex", binary, "", "CLIVOY_TEST_CUSTOM_KEY", "CLIVOY_TEST_HTTP_KEY")
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Generate(context.Background(), basicRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Env map[string]string `json:"env"`
	}
	if err = json.Unmarshal([]byte(result.Text), &capture); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"CLIVOY_API_KEY", "CLIVOY_TEST_CUSTOM_KEY", "clivoy_test_http_key"} {
		if capture.Env[key] != "" {
			t.Errorf("gateway secret %s inherited by CLI", key)
		}
	}
	if capture.Env["ANTHROPIC_API_KEY"] != "fake-existing-cli-auth" || capture.Env["CLIVOY_TEST_KEEP"] != "ordinary-environment" {
		t.Fatal("CLI authentication or ordinary environment was discarded")
	}
}
