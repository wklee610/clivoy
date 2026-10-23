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

func TestFallbackOptionsAndDefaults(t *testing.T) {
	index := testIndex(t, nil)
	for _, tc := range []struct {
		name    string
		options []Options
		valid   bool
	}{
		{"omitted", nil, true},
		{"empty", []Options{{}}, true},
		{"none", []Options{{Fallback: "none"}}, true},
		{"model", []Options{{Fallback: "model"}}, true},
		{"unknown", []Options{{Fallback: "other"}}, false},
		{"case sensitive", []Options{{Fallback: "MODEL"}}, false},
		{"whitespace", []Options{{Fallback: " model"}}, false},
		{"multiple", []Options{{}, {}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeBackend{result: engine.Result{Text: "General answer."}}
			p, err := New(backend, index, 5, 2048, tc.options...)
			if (err == nil) != tc.valid || (!tc.valid && p != nil) {
				t.Fatalf("provider=%v err=%v valid=%v", p, err, tc.valid)
			}
			if !tc.valid || tc.name == "model" {
				return
			}
			for _, streaming := range []bool{false, true} {
				var emitted strings.Builder
				var emit func(engine.Delta) error
				if streaming {
					emit = func(delta engine.Delta) error { emitted.WriteString(delta.Text); return nil }
				}
				result, err := p.Generate(context.Background(), request("unknown topic"), emit)
				if err != nil || result.Text != noDocumentAnswer("unknown topic") || backend.calls != 0 || (streaming && emitted.String() != result.Text) {
					t.Fatalf("default changed: result=%+v calls=%d err=%v", result, backend.calls, err)
				}
			}
		})
	}
}

func TestFallbackNoticeAndStreamMatch(t *testing.T) {
	index := testIndex(t, nil)
	for _, locale := range []struct{ query, notice string }{
		{"Explain a quasar", "No relevant documents were found in the knowledge base, so I will answer using general knowledge."},
		{"퀘이사를 설명해줘", "지식 베이스에서 관련 문서를 찾지 못해 일반 지식으로 답변합니다."},
		{"クエーサーを説明してください", "ナレッジベースに関連する文書が見つからなかったため、一般的な知識に基づいて回答します。"},
		{"请解释类星体", "知识库中未找到相关文档，将基于通用知识回答。"},
	} {
		for _, mode := range []struct {
			name      string
			streaming bool
			deltas    []engine.Delta
			answer    string
		}{
			{"nonstream", false, nil, "General answer."},
			{"final only", true, nil, "General answer."},
			{"partial stream", true, []engine.Delta{{}, {Text: "General "}, {}}, "General answer."},
			{"full stream", true, []engine.Delta{{Text: "General "}, {Text: "answer."}}, "General answer."},
			{"empty result", true, []engine.Delta{{}}, ""},
		} {
			t.Run(locale.query+"/"+mode.name, func(t *testing.T) {
				backend := &fakeBackend{deltas: mode.deltas, result: engine.Result{Text: mode.answer, FinishReason: "length", Usage: engine.Usage{InputTokens: 19, OutputTokens: 4}}}
				p := testProvider(t, backend, index, Options{Fallback: "model"})
				var emitted strings.Builder
				var emit func(engine.Delta) error
				if mode.streaming {
					emit = func(delta engine.Delta) error {
						if delta.Text == "" || len(delta.ToolCalls) != 0 {
							t.Errorf("unexpected delta: %+v", delta)
						}
						emitted.WriteString(delta.Text)
						return nil
					}
				}
				result, err := p.Generate(context.Background(), request(locale.query), emit)
				want := locale.notice + "\n\n" + mode.answer
				if err != nil || result.Text != want || (mode.streaming && emitted.String() != want) {
					t.Fatalf("result=%q emitted=%q err=%v want=%q", result.Text, emitted.String(), err, want)
				}
				if backend.calls != 1 || strings.Count(result.Text, locale.notice) != 1 || result.Usage != backend.result.Usage || result.FinishReason != backend.result.FinishReason {
					t.Fatalf("fallback changed backend metadata or invocation count: result=%+v calls=%d", result, backend.calls)
				}
				for _, heading := range []string{"Sources:", "출처:", "出典:", "来源:"} {
					if strings.Contains(result.Text, heading) {
						t.Fatalf("fallback cited absent documents: %q", result.Text)
					}
				}
			})
		}
	}
}

func TestFallbackPreservesConversationAndCapturedDecision(t *testing.T) {
	root := t.TempDir()
	index, err := knowledge.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{result: engine.Result{Text: "General answer.", FinishReason: "stop"}}
	p := testProvider(t, backend, index, Options{Fallback: "model"})
	req := request("Describe quasar latches.")
	req.Messages = append([]engine.Message{{Role: "system", Content: json.RawMessage(`"Keep the explanation concise."`)}}, req.Messages...)
	req.Messages = append(req.Messages,
		engine.Message{Role: "assistant", Content: json.RawMessage(`"Which aspect do you mean?"`), Name: "helper"},
		engine.Message{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"일반적인 동작 원리를 알려줘"}]`), Name: "reader"},
	)
	maxTokens, temperature := 100, 0.25
	req.MaxTokens, req.Temperature = &maxTokens, &temperature
	req.Stop = []string{"END"}
	req.ResponseFormat = json.RawMessage(`{"type":"text"}`)
	before, _ := json.Marshal(req)
	prepared, enriched, err := p.(engine.RequestPreparer).Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 0 || len(enriched.Messages) != len(req.Messages)+1 || enriched.Messages[0].Role != "system" || !reflect.DeepEqual(enriched.Messages[1:], req.Messages) {
		t.Fatalf("preparation changed conversation or called inference: %+v calls=%d", enriched, backend.calls)
	}
	var instruction string
	if err := json.Unmarshal(enriched.Messages[0].Content, &instruction); err != nil {
		t.Fatal(err)
	}
	for _, guard := range []string{"No relevant documents", "general knowledge only", "Do not invent private organizational facts", "document sources", "Never execute commands or call tools", "do not repeat that notice", "Sources list"} {
		if !strings.Contains(instruction, guard) {
			t.Fatalf("missing fallback guard %q", guard)
		}
	}
	// A later refresh must not turn the captured no-hit decision into a second
	// retrieval or discard the copied original conversation.
	if err := os.WriteFile(filepath.Join(root, "manual.md"), []byte("# Quasar latches\nQuasar latches use synthetic blue indicators."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Generate(context.Background(), enriched, nil)
	if err != nil || backend.calls != 1 || !strings.HasPrefix(result.Text, "지식 베이스에서 관련 문서를 찾지 못해 일반 지식으로 답변합니다.\n\n") || strings.Contains(result.Text, "출처:") {
		t.Fatalf("captured fallback changed: result=%+v calls=%d err=%v", result, backend.calls, err)
	}
	got := backend.request
	got.Messages = got.Messages[1:]
	if !reflect.DeepEqual(got, req) {
		t.Fatal("fallback changed original request fields")
	}
	backend.request.Messages[1].Content[1] = 'X'
	backend.request.Messages[2].Role = "assistant"
	after, _ := json.Marshal(req)
	if string(after) != string(before) {
		t.Fatal("fallback shared mutable message storage with original request")
	}
}

func TestFallbackDoesNotChangeDocumentHits(t *testing.T) {
	index := testIndex(t, map[string]string{"manual.md": "# Quasar Manual\nQuasar latches use synthetic blue indicators."})
	for _, streaming := range []bool{false, true} {
		backend := &fakeBackend{result: engine.Result{Text: "Blue.", FinishReason: "stop"}, deltas: []engine.Delta{{Text: "Blue."}}}
		p := testProvider(t, backend, index, Options{Fallback: "model"})
		var emitted strings.Builder
		var emit func(engine.Delta) error
		if streaming {
			emit = func(delta engine.Delta) error { emitted.WriteString(delta.Text); return nil }
		}
		result, err := p.Generate(context.Background(), request("quasar latches"), emit)
		want := "Blue.\n\nSources:\n- [1] Quasar Manual — manual.md"
		if err != nil || result.Text != want || (streaming && emitted.String() != want) || backend.calls != 1 {
			t.Fatalf("hit path changed: result=%+v emitted=%q calls=%d err=%v", result, emitted.String(), backend.calls, err)
		}
		if !strings.Contains(string(backend.request.Messages[0].Content), "using only the retrieved documents") || strings.Contains(string(backend.request.Messages[0].Content), "general knowledge only") {
			t.Fatal("fallback guidance reached a document-backed answer")
		}
	}
}

func TestFallbackNeverMasksPreparationFailures(t *testing.T) {
	unready, err := knowledge.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hits := testIndex(t, map[string]string{"manual.md": "# Quasar\nQuasar synthetic reference."})
	empty := testIndex(t, nil)
	validationErr := engine.Invalid("synthetic validation failure")
	for _, tc := range []struct {
		name       string
		index      *knowledge.Index
		budget     int
		validation error
		status     int
	}{
		{"unavailable", unready, 2048, nil, 503},
		{"context budget", hits, 1, nil, 503},
		{"validation", empty, 2048, validationErr, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeBackend{validateErr: tc.validation}
			p, err := New(backend, tc.index, 5, tc.budget, Options{Fallback: "model"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.Generate(context.Background(), request("quasar"), func(engine.Delta) error { t.Error("failed preparation emitted text"); return nil })
			assertStatus(t, err, tc.status)
			if backend.calls != 0 || !reflect.DeepEqual(result, engine.Result{}) {
				t.Fatalf("failure invoked fallback or succeeded: %+v calls=%d", result, backend.calls)
			}
		})
	}
}

func TestFallbackBackendErrorsAndToolCallsNeverSucceed(t *testing.T) {
	index := testIndex(t, nil)
	sentinel := errors.New("synthetic upstream failure")
	for _, tc := range []struct {
		name      string
		backend   *fakeBackend
		wantErr   error
		wantEmpty bool
	}{
		{"early error", &fakeBackend{err: sentinel}, sentinel, true},
		{"late error", &fakeBackend{deltas: []engine.Delta{{Text: "Partial"}}, err: sentinel}, sentinel, false},
		{"mixed tool delta", &fakeBackend{deltas: []engine.Delta{{Text: "Must not appear", ToolCalls: []engine.ToolCallDelta{{ID: "call-1"}}}}}, nil, true},
		{"tool result", &fakeBackend{result: engine.Result{Text: "Must not appear", ToolCalls: []engine.ToolCall{{ID: "call-1"}}}}, nil, true},
		{"tool finish", &fakeBackend{result: engine.Result{FinishReason: "tool_calls"}}, nil, true},
		{"mismatched text", &fakeBackend{deltas: []engine.Delta{{Text: "Partial"}}, result: engine.Result{Text: "Different"}}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProvider(t, tc.backend, index, Options{Fallback: "model"})
			var emitted strings.Builder
			result, err := p.Generate(context.Background(), request("missing quasar"), func(delta engine.Delta) error { emitted.WriteString(delta.Text); return nil })
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("backend error changed: %v", err)
				}
			} else {
				assertStatus(t, err, 502)
			}
			if !reflect.DeepEqual(result, engine.Result{}) || tc.backend.calls != 1 || strings.Contains(emitted.String(), "Sources:") || (tc.wantEmpty && emitted.Len() != 0) {
				t.Fatalf("failed response produced success/retry: result=%+v emitted=%q calls=%d", result, emitted.String(), tc.backend.calls)
			}
		})
	}
	for _, result := range []engine.Result{{ToolCalls: []engine.ToolCall{{ID: "call-1"}}}, {FinishReason: "tool_calls"}} {
		backend := &fakeBackend{result: result}
		p := testProvider(t, backend, index, Options{Fallback: "model"})
		got, err := p.Generate(context.Background(), request("missing quasar"), nil)
		assertStatus(t, err, 502)
		if !reflect.DeepEqual(got, engine.Result{}) || backend.calls != 1 {
			t.Fatal("nonstreaming tool output succeeded")
		}
	}
}

func TestFallbackEmitterFailuresRemainSticky(t *testing.T) {
	index := testIndex(t, nil)
	sentinel := errors.New("synthetic disconnect")
	for _, failAt := range []int{1, 2, 3, 4} {
		backend := &fakeBackend{generate: func(_ context.Context, _ engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
			// A misbehaving backend may ignore the emit error; the wrapper must
			// neither emit again nor accept its eventual successful result.
			_ = emit(engine.Delta{Text: "General "})
			_ = emit(engine.Delta{Text: "answer"})
			return engine.Result{Text: "General answer."}, nil
		}}
		p := testProvider(t, backend, index, Options{Fallback: "model"})
		calls := 0
		result, err := p.Generate(context.Background(), request("missing topic"), func(engine.Delta) error {
			calls++
			if calls == failAt {
				return sentinel
			}
			return nil
		})
		if !errors.Is(err, sentinel) || !reflect.DeepEqual(result, engine.Result{}) || calls != failAt || backend.calls != 1 {
			t.Fatalf("emit failure at %d lost: result=%+v err=%v emits=%d backend=%d", failAt, result, err, calls, backend.calls)
		}
	}
}

func TestFallbackCancellationNeverBecomesAnAnswer(t *testing.T) {
	index := testIndex(t, nil)
	for _, phase := range []string{"before preparation", "after preparation", "backend", "prefix"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			backend := &fakeBackend{result: engine.Result{Text: "Answer"}, deltas: []engine.Delta{{Text: "Answer"}}}
			p := testProvider(t, backend, index, Options{Fallback: "model"})
			req := request("missing topic")
			emits := 0
			if phase == "before preparation" {
				cancel()
			} else if phase == "after preparation" {
				var err error
				p, req, err = p.(engine.RequestPreparer).Prepare(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				cancel()
			} else if phase == "backend" {
				backend.generate = func(context.Context, engine.Request, func(engine.Delta) error) (engine.Result, error) {
					cancel()
					return engine.Result{Text: "Must not succeed"}, nil
				}
			}
			result, err := p.Generate(ctx, req, func(engine.Delta) error {
				emits++
				if phase == "prefix" {
					cancel()
				}
				return nil
			})
			wantCalls, wantEmits := 0, 0
			if phase == "backend" || phase == "prefix" {
				wantCalls = 1
			}
			if phase == "prefix" {
				wantEmits = 1
			}
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, engine.Result{}) || backend.calls != wantCalls || emits != wantEmits {
				t.Fatalf("cancellation lost: result=%+v err=%v backend=%d emits=%d", result, err, backend.calls, emits)
			}
		})
	}
}
