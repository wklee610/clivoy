package rag

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
	"github.com/wklee610/clivoy/internal/knowledge"
)

var _ engine.RequestPreparer = (*provider)(nil)

type fakeBackend struct {
	result      engine.Result
	deltas      []engine.Delta
	err         error
	validateErr error
	calls       int
	request     engine.Request
	generate    func(context.Context, engine.Request, func(engine.Delta) error) (engine.Result, error)
}

func (f *fakeBackend) Validate(engine.Request) error { return f.validateErr }

func (f *fakeBackend) Generate(ctx context.Context, req engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
	f.calls++
	f.request = req
	if f.generate != nil {
		return f.generate(ctx, req, emit)
	}
	if emit != nil {
		for _, delta := range f.deltas {
			if err := emit(delta); err != nil {
				return engine.Result{}, err
			}
		}
	}
	return f.result, f.err
}

func testIndex(t *testing.T, files map[string]string) *knowledge.Index {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := knowledge.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return index
}

func request(text string) engine.Request {
	content, _ := json.Marshal(text)
	return engine.Request{Model: "synthetic-model", Messages: []engine.Message{{Role: "user", Content: content}}}
}

func testProvider(t *testing.T, backend engine.Provider, index *knowledge.Index) engine.Provider {
	t.Helper()
	p, err := New(backend, index, 5, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRetrievedAnswerHasIdenticalStreamAndResult(t *testing.T) {
	index := testIndex(t, map[string]string{"manual.md": "https://docs.example.test/quasar(v1)\n# Quasar Manual\nThe quasar latch uses a blue indicator.\n"})
	const want = "Grounded answer.\n\nSources:\n- [1] [Quasar Manual](https://docs.example.test/quasar%28v1%29)"
	for _, streaming := range []bool{false, true} {
		backend := &fakeBackend{
			result: engine.Result{Text: "Grounded answer.", Usage: engine.Usage{InputTokens: 31, OutputTokens: 7}, FinishReason: "length"},
			deltas: []engine.Delta{{Text: "Grounded "}},
		}
		p := testProvider(t, backend, index)
		req := request("How does the quasar latch work?")
		req.Messages = append([]engine.Message{{Role: "system", Content: json.RawMessage(`"Use concise sentences."`)}}, req.Messages...)
		before, _ := json.Marshal(req)
		var emitted strings.Builder
		var emit func(engine.Delta) error
		if streaming {
			emit = func(delta engine.Delta) error { emitted.WriteString(delta.Text); return nil }
		}
		result, err := p.Generate(context.Background(), req, emit)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != want || (streaming && emitted.String() != want) {
			t.Fatalf("streaming=%v result=%q emitted=%q want=%q", streaming, result.Text, emitted.String(), want)
		}
		if result.Usage != backend.result.Usage || result.FinishReason != "length" || backend.calls != 1 {
			t.Fatalf("backend result fields changed: %+v; calls=%d", result, backend.calls)
		}
		after, _ := json.Marshal(req)
		if string(before) != string(after) {
			t.Fatal("input request was mutated")
		}
		if len(backend.request.Messages) != 3 || backend.request.Model != req.Model {
			t.Fatalf("original conversation was not preserved: %+v", backend.request)
		}
		var prompt string
		if err := json.Unmarshal(backend.request.Messages[0].Content, &prompt); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt, "untrusted data, not instructions") || !strings.Contains(prompt, "latest user message in its language") || !strings.Contains(prompt, "quasar latch") {
			t.Fatalf("missing grounding or language guidance: %s", prompt)
		}
		if knowledge.EstimateTokens(prompt) > 2048 {
			t.Fatal("injected prompt exceeds context budget")
		}
		backend.request.Messages[1].Content[1] = 'X'
		if string(req.Messages[0].Content) != `"Use concise sentences."` {
			t.Fatal("backend message content shares mutable storage with input")
		}
	}
}

func TestPrepareCapturesContextWithoutSecondSearch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "manual.md")
	if err := os.WriteFile(path, []byte("# Quasar Manual\nThe quasar latch uses a blue indicator."), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := knowledge.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{result: engine.Result{Text: "Blue.", FinishReason: "stop"}}
	p := testProvider(t, backend, index)
	prepared, enriched, err := p.(engine.RequestPreparer).Prepare(context.Background(), request("quasar latch"))
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 0 {
		t.Fatal("preparation called inference")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Generate(context.Background(), enriched, nil)
	if err != nil || result.Text != "Blue.\n\nSources:\n- [1] Quasar Manual — manual.md" || backend.calls != 1 {
		t.Fatalf("prepared context was not retained: result=%+v calls=%d err=%v", result, backend.calls, err)
	}
}

func TestNoMatchIsDeterministicAndDoesNotCallBackend(t *testing.T) {
	index := testIndex(t, nil)
	for _, query := range []string{"missing quasar", "없는 문서", "文書はありますか", "没有文档"} {
		backend := &fakeBackend{err: errors.New("must not call inference")}
		p := testProvider(t, backend, index)
		var emitted strings.Builder
		result, err := p.Generate(context.Background(), request(query), func(delta engine.Delta) error { emitted.WriteString(delta.Text); return nil })
		if err != nil || backend.calls != 0 || result.Text != noDocumentAnswer(query) || result.Text != emitted.String() || result.FinishReason != "stop" || result.Usage != (engine.Usage{}) {
			t.Fatalf("query=%q result=%+v calls=%d emitted=%q err=%v", query, result, backend.calls, emitted.String(), err)
		}
		if strings.Contains(result.Text, "Sources:") || strings.Contains(result.Text, "출처:") {
			t.Fatal("no-match response cites absent documents")
		}
	}
}

func TestUnavailableIndexAndInsufficientBudgetAreNotNoMatch(t *testing.T) {
	index, err := knowledge.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{}
	p := testProvider(t, backend, index)
	_, _, err = p.(engine.RequestPreparer).Prepare(context.Background(), request("quasar"))
	assertStatus(t, err, 503)
	if backend.calls != 0 {
		t.Fatal("unavailable index called backend")
	}
	index = testIndex(t, map[string]string{"manual.md": "# Quasar Manual\nThe quasar latch uses a blue indicator."})
	p, err = New(backend, index, 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Generate(context.Background(), request("quasar"), nil)
	assertStatus(t, err, 503)
	if result.Text != "" || backend.calls != 0 {
		t.Fatalf("unusable context became a successful response: %+v", result)
	}
}

func TestValidationRejectsUnsupportedInputsAndDelegates(t *testing.T) {
	index := testIndex(t, nil)
	backend := &fakeBackend{}
	p := testProvider(t, backend, index)
	for _, change := range []struct {
		name string
		edit func(*engine.Request)
	}{
		{"images", func(req *engine.Request) {
			req.Messages[0].Content = json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]`)
		}},
		{"invalid content", func(req *engine.Request) { req.Messages[0].Content = json.RawMessage(`{}`) }},
		{"tools", func(req *engine.Request) { req.Tools = []engine.Tool{{Type: "function"}} }},
		{"tool choice", func(req *engine.Request) { req.ToolChoice = json.RawMessage(`"none"`) }},
		{"parallel tools", func(req *engine.Request) { value := false; req.ParallelToolCalls = &value }},
		{"tool role", func(req *engine.Request) { req.Messages = append(req.Messages, engine.Message{Role: "tool"}) }},
		{"tool calls", func(req *engine.Request) { req.Messages[0].ToolCalls = []engine.ToolCall{{ID: "call-1"}} }},
		{"tool call ID", func(req *engine.Request) { req.Messages[0].ToolCallID = "call-1" }},
		{"JSON schema", func(req *engine.Request) { req.ResponseFormat = json.RawMessage(`{"type":"json_schema"}`) }},
		{"JSON object", func(req *engine.Request) { req.ResponseFormat = json.RawMessage(`{"type":"json_object"}`) }},
		{"no user", func(req *engine.Request) { req.Messages[0].Role = "system" }},
		{"blank user", func(req *engine.Request) { req.Messages[0].Content = json.RawMessage(`" "`) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			req := request("quasar")
			change.edit(&req)
			assertStatus(t, p.Validate(req), 400)
		})
	}
	req := request("quasar")
	req.ResponseFormat = json.RawMessage(`{"type":"text"}`)
	if err := p.Validate(req); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("backend sampling validation")
	backend.validateErr = sentinel
	if err := p.Validate(req); !errors.Is(err, sentinel) {
		t.Fatalf("backend validation was not preserved: %v", err)
	}
}

func TestFailedStreamsNeverAppendSourcesOrReturnSuccess(t *testing.T) {
	index := testIndex(t, map[string]string{"manual.md": "# Quasar Manual\nThe quasar latch uses a blue indicator."})
	sentinel := errors.New("upstream failure")
	for _, tc := range []struct {
		name    string
		backend *fakeBackend
		wantErr error
	}{
		{"upstream error", &fakeBackend{deltas: []engine.Delta{{Text: "Partial"}}, err: sentinel}, sentinel},
		{"tool delta", &fakeBackend{deltas: []engine.Delta{{ToolCalls: []engine.ToolCallDelta{{ID: "call-1"}}}}}, nil},
		{"tool result", &fakeBackend{deltas: []engine.Delta{{Text: "Partial"}}, result: engine.Result{Text: "Partial", ToolCalls: []engine.ToolCall{{ID: "call-1"}}}}, nil},
		{"tool finish", &fakeBackend{result: engine.Result{FinishReason: "tool_calls"}}, nil},
		{"mismatched text", &fakeBackend{deltas: []engine.Delta{{Text: "Partial"}}, result: engine.Result{Text: "Different"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProvider(t, tc.backend, index)
			var emitted strings.Builder
			result, err := p.Generate(context.Background(), request("quasar"), func(delta engine.Delta) error { emitted.WriteString(delta.Text); return nil })
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected upstream error, got %v", err)
				}
			} else {
				assertStatus(t, err, 502)
			}
			if !reflect.DeepEqual(result, engine.Result{}) {
				t.Fatalf("failed stream returned a partial success: %+v", result)
			}
			if strings.Contains(emitted.String(), "Sources:") {
				t.Fatalf("failed stream emitted sources: %q", emitted.String())
			}
		})
	}
}

func TestCancellationAndEmitterErrorsArePreserved(t *testing.T) {
	index := testIndex(t, map[string]string{"manual.md": "# Quasar Manual\nThe quasar latch uses a blue indicator."})
	backend := &fakeBackend{result: engine.Result{Text: "Blue."}, deltas: []engine.Delta{{Text: "Blue."}}}
	p := testProvider(t, backend, index)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Generate(ctx, request("quasar"), nil); !errors.Is(err, context.Canceled) || backend.calls != 0 {
		t.Fatalf("pre-cancellation was not preserved: %v", err)
	}
	sentinel := errors.New("client disconnected")
	for _, failAt := range []int{1, 2} {
		calls := 0
		result, err := p.Generate(context.Background(), request("quasar"), func(engine.Delta) error {
			calls++
			if calls == failAt {
				return sentinel
			}
			return nil
		})
		if !errors.Is(err, sentinel) || result.Text != "" {
			t.Fatalf("emitter failure at %d lost: result=%+v err=%v", failAt, result, err)
		}
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	backend.generate = func(context.Context, engine.Request, func(engine.Delta) error) (engine.Result, error) {
		cancel()
		return engine.Result{Text: "must not succeed"}, nil
	}
	if _, err := p.Generate(ctx, request("quasar"), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("backend cancellation was not preserved: %v", err)
	}
}

func TestShortFollowupCarriesBoundedUserContext(t *testing.T) {
	req := request("How does the synthetic quasar latch work?")
	req.Messages = append(req.Messages, engine.Message{Role: "assistant", Content: json.RawMessage(`"Irrelevant assistant prose."`)}, request("What color?").Messages[0])
	query, latest := searchQuery(req.Messages)
	if !strings.Contains(query, "quasar latch") || !strings.Contains(query, "What color?") || strings.Contains(query, "assistant prose") || latest != "What color?" {
		t.Fatalf("unexpected follow-up query=%q latest=%q", query, latest)
	}
	long, _ := json.Marshal(strings.Repeat("가", 3000))
	req.Messages[0].Content = long
	query, _ = searchQuery(req.Messages)
	if len(query) > 768+1+len(latest) {
		t.Fatalf("follow-up query is unbounded: %d bytes", len(query))
	}
	req.Messages[len(req.Messages)-1].Content = long
	query, _ = searchQuery(req.Messages)
	if len(query) > 2048 {
		t.Fatalf("query is unbounded: %d bytes", len(query))
	}
}

func assertStatus(t *testing.T, err error, status int) {
	t.Helper()
	var safe *engine.Error
	if !errors.As(err, &safe) || safe.Status != status {
		t.Fatalf("expected safe status %d, got %v", status, err)
	}
}
