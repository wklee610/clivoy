package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wklee610/clivoy/internal/clidiscovery"
	"github.com/wklee610/clivoy/internal/engine"
)

type cliProvider struct {
	kind, binary, workdir string
	commandArgs           []string
	tempDir               string
	excludedEnv           []string
}

// NewCLI uses credentials already available to the installed CLI. An empty
// workdir gives every request a fresh temporary working directory.
func NewCLI(kind string, command []string, workdir string, excludedEnv ...string) (engine.Provider, error) {
	return NewCLIWithTempDir(kind, command, workdir, "", excludedEnv...)
}

// NewCLIWithTempDir keeps Clivoy-owned request directories and image attachments
// in the chosen runtime directory. It does not relocate the CLI's credentials.
func NewCLIWithTempDir(kind string, command []string, workdir, tempDir string, excludedEnv ...string) (engine.Provider, error) {
	if kind != "codex" && kind != "claude" && kind != "gemini" && kind != "grok" {
		return nil, fmt.Errorf("unsupported CLI provider %q", kind)
	}
	if kind == "grok" && workdir != "" {
		return nil, errors.New("Grok CLI requires an isolated temporary working directory; omit workdir")
	}
	var binary string
	var commandArgs []string
	if len(command) > 0 {
		if strings.TrimSpace(command[0]) == "" {
			return nil, errors.New("CLI command executable must not be empty")
		}
		for _, arg := range command {
			if strings.ContainsRune(arg, '\x00') {
				return nil, errors.New("CLI command must not contain NUL bytes")
			}
		}
		binary = command[0]
		commandArgs = append([]string(nil), command[1:]...)
	}
	// Discover the CLI at request time so later installations and upgrades are
	// picked up. Explicit relative paths stay rooted here.
	var err error
	if strings.ContainsAny(binary, `/\\`) {
		binary, err = filepath.Abs(binary)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve %s CLI executable", kind)
		}
	}
	if workdir != "" {
		workdir, err = filepath.Abs(workdir)
		if err != nil {
			return nil, errors.New("cannot resolve CLI working directory")
		}
		info, statErr := os.Stat(workdir)
		if statErr != nil || !info.IsDir() {
			return nil, errors.New("CLI working directory must be an existing directory")
		}
	}
	if tempDir != "" {
		tempDir, err = filepath.Abs(tempDir)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve CLI temporary directory")
		}
	}
	return &cliProvider{kind: kind, binary: binary, commandArgs: commandArgs, workdir: workdir, tempDir: tempDir, excludedEnv: append([]string{"CLIVOY_API_KEY", "CLIVOY_HOME"}, excludedEnv...)}, nil
}

func (p *cliProvider) environment() []string {
	blocked := map[string]bool{"CLIVOY_API_KEY": true}
	for _, key := range p.excludedEnv {
		blocked[strings.ToUpper(key)] = true
	}
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[strings.ToUpper(key)] {
			env = append(env, entry)
		}
	}
	return env
}

func (p *cliProvider) Validate(req engine.Request) error {
	if err := validateMessages(req, false); err != nil {
		return err
	}
	if len(req.Tools) != 0 || len(req.ToolChoice) != 0 || req.ParallelToolCalls != nil {
		return engine.Invalid("CLI providers do not support API tool calling; use an OpenAI-compatible HTTP provider")
	}
	for _, m := range req.Messages {
		if p.kind == "gemini" || p.kind == "grok" {
			parts, _ := engine.ParseContent(m.Content)
			for _, part := range parts {
				if part.Type == "image_url" {
					return engine.Invalid(p.kind + " CLI currently supports text messages only")
				}
			}
		}
		if m.Role == "tool" || len(m.ToolCalls) > 0 || m.ToolCallID != "" {
			return engine.Invalid("CLI providers do not support tool-call conversation history")
		}
	}
	if req.Temperature != nil || req.TopP != nil || len(req.Stop) > 0 {
		return engine.Invalid("CLI providers do not support temperature, top_p, or stop")
	}
	if len(req.ResponseFormat) > 0 {
		return engine.Invalid("CLI providers do not support response_format")
	}
	return nil
}

func (p *cliProvider) hasCommandFlag(flags ...string) bool {
	for _, arg := range p.commandArgs {
		for _, flag := range flags {
			if arg == flag {
				return true
			}
		}
	}
	return false
}

func (p *cliProvider) args(req engine.Request) []string {
	if p.kind == "grok" {
		return p.grokArgs(req)
	}
	var args []string
	if p.kind == "codex" {
		args = append([]string{"exec"}, p.commandArgs...)
		args = append(args, "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "--color", "never", "-c", `web_search="disabled"`)
		if !p.hasCommandFlag("--yolo", "--dangerously-bypass-approvals-and-sandbox") {
			args = append(args, "--sandbox", "read-only", "-c", `approval_policy="never"`)
		}
	} else if p.kind == "gemini" {
		args = append([]string(nil), p.commandArgs...)
		args = append(args, "--output-format", "stream-json", "--approval-mode", "default", "--extensions", "none")
	} else {
		args = append([]string(nil), p.commandArgs...)
		args = append(args, "--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--disable-slash-commands", "--no-session-persistence", "--no-chrome", "--settings", `{"disableAllHooks":true}`)
		if !p.hasCommandFlag("--dangerously-skip-permissions") {
			args = append(args, "--tools", "", "--permission-mode", "dontAsk")
		}
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if p.kind == "codex" {
		args = append(args, "-")
	}
	return args
}

func (p *cliProvider) Generate(ctx context.Context, req engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
	if err := p.Validate(req); err != nil {
		return engine.Result{}, err
	}
	if ctx.Err() != nil {
		return engine.Result{}, ctx.Err()
	}
	resolved, lookupErr := clidiscovery.Resolve(p.kind, p.binary)
	if lookupErr != nil {
		return engine.Result{}, &engine.Error{Status: 503, Type: "provider_error", Message: p.kind + " CLI could not be resolved; run clivoy doctor and check the provider command setting"}
	}
	workdir := p.workdir
	if workdir == "" {
		var err error
		workdir, err = os.MkdirTemp(p.tempDir, "clivoy-*")
		if err != nil {
			return engine.Result{}, upstreamError("cannot create CLI working directory")
		}
		defer os.RemoveAll(workdir)
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args, input, cleanup, err := p.prepareInput(req)
	if err != nil {
		return engine.Result{}, err
	}
	defer cleanup()
	env := p.environment()
	if p.kind == "gemini" {
		var cleanupEnvironment func()
		var noMCP string
		env, noMCP, cleanupEnvironment, err = p.prepareGeminiEnvironment(env)
		if err != nil {
			return engine.Result{}, err
		}
		defer cleanupEnvironment()
		args = append(args, "--allowed-mcp-server-names", noMCP)
	} else if p.kind == "grok" {
		var cleanupEnvironment func()
		env, cleanupEnvironment, err = p.prepareGrokEnvironment(env)
		if err != nil {
			return engine.Result{}, err
		}
		defer cleanupEnvironment()
	}
	cmd := exec.CommandContext(childCtx, resolved.Path, append(resolved.Args, args...)...)
	cmd.Env = resolved.Environment(env)
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	parser := &cliParser{kind: p.kind, emit: emit}
	out := &lineWriter{parse: parser.line, cancel: cancel}
	stderr := &limitedDiscard{limit: maxStderrBytes, cancel: cancel}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return engine.Result{}, ctx.Err()
		}
		return engine.Result{}, upstreamError("could not start " + p.kind + " CLI")
	}
	err = cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		// A descendant can retain stdout after its parent exits. Terminate the
		// remaining group as well as bounding the pipe-copy wait.
		_ = cmd.Cancel()
	}
	if ctx.Err() != nil {
		return engine.Result{}, ctx.Err()
	}
	if out.err != nil {
		return engine.Result{}, out.err
	}
	if stderr.exceeded {
		return engine.Result{}, upstreamError("CLI diagnostic output exceeded the size limit")
	}
	if err != nil {
		return engine.Result{}, upstreamError(p.kind + " CLI failed; check local CLI login, selected model, and configuration")
	}
	if err := out.finish(); err != nil {
		return engine.Result{}, err
	}
	if !parser.done {
		return engine.Result{}, upstreamError("CLI output ended before a completed result")
	}
	return parser.result, nil
}

// os/exec invokes each writer from one copy goroutine; state is read only after Wait.
type limitedDiscard struct {
	limit, n int
	exceeded bool
	cancel   context.CancelFunc
}

func (w *limitedDiscard) Write(b []byte) (int, error) {
	w.n += len(b)
	if w.n > w.limit {
		w.exceeded = true
		w.cancel()
		return 0, errors.New("diagnostic output limit")
	}
	return len(b), nil
}

type lineWriter struct {
	pending []byte
	n       int
	parse   func([]byte) error
	cancel  context.CancelFunc
	err     error
}

func (w *lineWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.n += len(b)
	if w.n > maxWireBytes {
		return w.fail(upstreamError("CLI output exceeded the size limit"))
	}
	w.pending = append(w.pending, b...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		if i > maxOutputBytes {
			return w.fail(upstreamError("CLI event exceeded the size limit"))
		}
		line := bytes.TrimSpace(w.pending[:i])
		if len(line) > 0 {
			if err := w.parse(line); err != nil {
				return w.fail(err)
			}
		}
		w.pending = w.pending[i+1:]
	}
	if len(w.pending) > maxOutputBytes {
		return w.fail(upstreamError("CLI event exceeded the size limit"))
	}
	return len(b), nil
}
func (w *lineWriter) fail(err error) (int, error) { w.err = err; w.cancel(); return 0, err }
func (w *lineWriter) finish() error {
	if len(bytes.TrimSpace(w.pending)) > 0 {
		return w.parse(bytes.TrimSpace(w.pending))
	}
	return nil
}

type cliParser struct {
	kind   string
	emit   func(engine.Delta) error
	result engine.Result
	done   bool
	text   strings.Builder

	claudeMessageStart int
}

func (p *cliParser) add(s string) error {
	if p.text.Len()+len(s) > maxOutputBytes {
		return upstreamError("CLI response exceeded the size limit")
	}
	p.text.WriteString(s)
	p.result.Text = p.text.String()
	if s != "" && p.emit != nil {
		return p.emit(engine.Delta{Text: s})
	}
	return nil
}
func (p *cliParser) line(line []byte) error {
	if p.kind == "gemini" {
		return p.geminiLine(line)
	}
	if p.kind == "grok" {
		return p.grokLine(line)
	}
	var event struct {
		Type            string  `json:"type"`
		Subtype         string  `json:"subtype"`
		IsError         bool    `json:"is_error"`
		Result          string  `json:"result"`
		ParentToolUseID *string `json:"parent_tool_use_id"`
		Item            struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
		Usage struct {
			InputTokens   int `json:"input_tokens"`
			OutputTokens  int `json:"output_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		} `json:"event"`
	}
	if json.Unmarshal(line, &event) != nil {
		return upstreamError("CLI returned invalid JSON output")
	}
	if event.Type == "error" || event.Type == "turn.failed" {
		return upstreamError("CLI could not complete the request; check local CLI login, selected model, and configuration")
	}
	if p.kind == "codex" {
		switch event.Type {
		case "item.completed":
			if event.Item.Type == "agent_message" {
				if p.result.Text != "" {
					if err := p.add("\n\n"); err != nil {
						return err
					}
				}
				return p.add(event.Item.Text)
			}
		case "turn.completed":
			p.done = true
			p.result.FinishReason = "stop"
			p.result.Usage = engine.Usage{InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens}
		}
		return nil
	}
	if event.ParentToolUseID != nil {
		return nil
	}
	switch event.Type {
	case "stream_event":
		if event.Event.Type == "message_start" {
			p.claudeMessageStart = p.text.Len()
		}
		if event.Event.Type == "content_block_delta" && event.Event.Delta.Type == "text_delta" {
			return p.add(event.Event.Delta.Text)
		}
		if event.Event.Type == "message_delta" && event.Event.Delta.StopReason == "max_tokens" {
			p.result.FinishReason = "length"
		}
	case "result":
		if event.IsError || (event.Subtype != "" && event.Subtype != "success") {
			if strings.HasPrefix(strings.TrimSpace(event.Result), "Not logged in") {
				return upstreamError("Claude CLI is not logged in; run claude locally and sign in")
			}
			return upstreamError("Claude CLI could not complete the request; check local CLI login, selected model, and configuration")
		}
		// A tool loop can stream several assistant messages. The final result
		// duplicates only the last message, so retain earlier messages and emit
		// only the missing suffix of the current one.
		currentMessage := p.result.Text[p.claudeMessageStart:]
		if !strings.HasPrefix(event.Result, currentMessage) {
			return upstreamError("Claude CLI final output did not match the streamed response")
		}
		if err := p.add(strings.TrimPrefix(event.Result, currentMessage)); err != nil {
			return err
		}
		p.done = true
		if p.result.FinishReason == "" {
			p.result.FinishReason = "stop"
		}
		p.result.Usage = engine.Usage{InputTokens: event.Usage.InputTokens + event.Usage.CacheRead + event.Usage.CacheCreation, OutputTokens: event.Usage.OutputTokens}
	}
	return nil
}
