package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
	"github.com/wklee610/clivoy/internal/rag"
)

const fallbackNoticeKO = "지식 베이스에서 관련 문서를 찾지 못해 일반 지식으로 답변합니다.\n\n"

func fallbackRequestBody(t *testing.T, path string, stream bool) string {
	t.Helper()
	messages := []map[string]string{
		{"role": "user", "content": "UnknownPriorTopicXYZ"},
		{"role": "assistant", "content": "Prior answer"},
		{"role": "user", "content": "UnknownProductXYZ 설정 방법은?"},
	}
	body := map[string]any{"model": "test-alias", "stream": stream, "temperature": 0.25}
	switch path {
	case "/v1/responses":
		body["instructions"] = "Use concise sentences."
		body["input"] = messages
		body["max_output_tokens"] = 100
	case "/v1/messages":
		body["system"] = "Use concise sentences."
		body["messages"] = messages
		body["max_tokens"] = 100
	default:
		body["messages"] = append([]map[string]string{{"role": "system", "content": "Use concise sentences."}}, messages...)
		body["max_tokens"] = 100
		if stream {
			body["stream_options"] = map[string]bool{"include_usage": true}
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Decode the actual protocol content, so an extra notice in final SSE metadata
// cannot hide duplicated or missing text in the deltas consumed by clients.
func knowledgeResponseText(t *testing.T, path string, raw []byte) string {
	t.Helper()
	var response struct {
		Choices []struct{ Message struct{ Content string } }
		Content []struct{ Text string }
		Output  []struct{ Content []struct{ Text string } }
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	switch path {
	case "/v1/messages":
		for _, block := range response.Content {
			text.WriteString(block.Text)
		}
	case "/v1/responses":
		for _, item := range response.Output {
			for _, block := range item.Content {
				text.WriteString(block.Text)
			}
		}
	default:
		if len(response.Choices) != 1 {
			t.Fatalf("expected one chat choice, got %d", len(response.Choices))
		}
		text.WriteString(response.Choices[0].Message.Content)
	}
	return text.String()
}

func knowledgeStreamText(t *testing.T, path, body string) (string, int, bool) {
	t.Helper()
	var text strings.Builder
	terminals, failed := 0, false
	for _, event := range readEvents(t, body) {
		if len(event.data["error"]) > 0 || event.name == "error" || event.name == "response.failed" {
			failed = true
		}
		var delta string
		switch path {
		case "/v1/messages":
			if event.name == "content_block_delta" {
				delta = decodeEventField[struct{ Text string }](t, event, "delta").Text
			}
			if event.name == "message_stop" {
				terminals++
			}
		case "/v1/responses":
			if event.name == "response.output_text.delta" {
				delta = decodeEventField[string](t, event, "delta")
			}
			if event.name == "response.output_text.done" && decodeEventField[string](t, event, "text") != text.String() {
				t.Fatal("Responses done text differs from streamed text")
			}
			if event.name == "response.completed" {
				if knowledgeResponseText(t, path, event.data["response"]) != text.String() {
					t.Fatal("Responses completed output differs from streamed text")
				}
				terminals++
			}
		default:
			if event.done {
				terminals++
			} else if len(event.data["choices"]) > 0 {
				choices := decodeEventField[[]struct{ Delta struct{ Content string } }](t, event, "choices")
				for _, choice := range choices {
					delta += choice.Delta.Content
				}
			}
		}
		if delta != "" && terminals != 0 {
			t.Fatal("text arrived after a successful terminal event")
		}
		text.WriteString(delta)
	}
	return text.String(), terminals, failed
}

func TestKnowledgeFallbackAcrossProtocols(t *testing.T) {
	index, _ := knowledgeIndex(t, true)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, mode := range []string{"json", "stream", "stream-final-only"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				const answer = "일반적인 설정 절차를 확인하세요."
				backend := &fakeProvider{generate: func(_ context.Context, req engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
					if req.Model != "real-upstream-model" || req.MaxTokens == nil || *req.MaxTokens != 100 || req.Temperature == nil || *req.Temperature != 0.25 {
						t.Fatal("fallback changed model routing or generation settings")
					}
					if len(req.Messages) != 5 || req.Messages[0].Role != "system" {
						t.Fatalf("fallback did not add exactly one system instruction: %+v", req.Messages)
					}
					var guard string
					if err := json.Unmarshal(req.Messages[0].Content, &guard); err != nil || !strings.Contains(strings.ToLower(guard), "general knowledge") {
						t.Fatalf("fallback instruction missing general knowledge boundary: %q", guard)
					}
					for i, want := range []struct{ role, text string }{
						{"system", "Use concise sentences."}, {"user", "UnknownPriorTopicXYZ"},
						{"assistant", "Prior answer"}, {"user", "UnknownProductXYZ 설정 방법은?"},
					} {
						parts, err := engine.ParseContent(req.Messages[i+1].Content)
						if err != nil || len(parts) != 1 || parts[0].Text != want.text || req.Messages[i+1].Role != want.role {
							t.Fatalf("fallback changed conversation message %d", i)
						}
					}
					if emit != nil && mode == "stream" {
						// The final result contains a suffix that the backend did not emit.
						if err := emit(engine.Delta{Text: "일반적인 "}); err != nil {
							return engine.Result{}, err
						}
					}
					return engine.Result{Text: answer, FinishReason: "stop", Usage: engine.Usage{InputTokens: 20, OutputTokens: 5}}, nil
				}}
				wrapped, err := rag.New(backend, index, 10, 6000, rag.Options{Fallback: "model"})
				if err != nil {
					t.Fatal(err)
				}
				stream := mode != "json"
				w := invoke(testServer(t, wrapped, nil), apiRequest(path, fallbackRequestBody(t, path, stream)))
				if w.Code != 200 || backend.calls.Load() != 1 {
					t.Fatalf("status=%d calls=%d body=%s", w.Code, backend.calls.Load(), w.Body.String())
				}
				var text string
				if stream {
					var terminals int
					var failed bool
					text, terminals, failed = knowledgeStreamText(t, path, w.Body.String())
					if terminals != 1 || failed {
						t.Fatalf("fallback stream did not complete exactly once: %s", w.Body.String())
					}
				} else {
					text = knowledgeResponseText(t, path, w.Body.Bytes())
				}
				if text != fallbackNoticeKO+answer {
					t.Fatalf("fallback notice/answer missing, duplicated, or fabricated citations: %q", text)
				}
				for _, token := range []string{`"input_tokens":20`, `"output_tokens":5`} {
					if path == "/v1/chat/completions" {
						token = strings.ReplaceAll(strings.ReplaceAll(token, "input_tokens", "prompt_tokens"), "output_tokens", "completion_tokens")
					}
					if !strings.Contains(w.Body.String(), token) {
						t.Fatalf("backend usage lost: missing %s", token)
					}
				}
			})
		}
	}
}

func TestKnowledgeFallbackKeepsRetrievedAnswers(t *testing.T) {
	index, _ := knowledgeIndex(t, true)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				backend := &fakeProvider{generate: func(_ context.Context, req engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
					encoded, _ := json.Marshal(req.Messages)
					if !strings.Contains(string(encoded), "12345") {
						t.Fatal("fallback option bypassed available retrieval evidence")
					}
					return engine.Result{Text: "Use port 12345.", FinishReason: "stop"}, nil
				}}
				wrapped, err := rag.New(backend, index, 10, 6000, rag.Options{Fallback: "model"})
				if err != nil {
					t.Fatal(err)
				}
				body := strings.ReplaceAll(bodyFor(path, stream), "hello", "Quasar configuration")
				w := invoke(testServer(t, wrapped, nil), apiRequest(path, body))
				if w.Code != 200 || backend.calls.Load() != 1 || !strings.Contains(w.Body.String(), "https://example.test/quasar") || strings.Contains(w.Body.String(), "general knowledge") {
					t.Fatalf("retrieved answer changed with fallback enabled: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestKnowledgeFallbackDoesNotHidePreparationFailures(t *testing.T) {
	for _, failure := range []string{"index-unavailable", "invalid-request", "cancelled"} {
		index, _ := knowledgeIndex(t, failure != "index-unavailable")
		for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", failure, path, stream), func(t *testing.T) {
					backend := &fakeProvider{}
					wantStatus := 503
					if failure == "invalid-request" {
						backend.validate = func(engine.Request) error { return engine.Invalid("Unsupported input") }
						wantStatus = 400
					} else if failure == "cancelled" {
						wantStatus = 408
					}
					wrapped, err := rag.New(backend, index, 10, 6000, rag.Options{Fallback: "model"})
					if err != nil {
						t.Fatal(err)
					}
					request := apiRequest(path, fallbackRequestBody(t, path, stream))
					if failure == "cancelled" {
						ctx, cancel := context.WithCancel(request.Context())
						cancel()
						request = request.WithContext(ctx)
					}
					w := invoke(testServer(t, wrapped, nil), request)
					if w.Code != wantStatus || backend.calls.Load() != 0 || strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
						t.Fatalf("preparation failure called fallback or started SSE: %d %s", w.Code, w.Body.String())
					}
				})
			}
		}
	}
}

func TestKnowledgeFallbackBackendFailureIsNotSuccess(t *testing.T) {
	index, _ := knowledgeIndex(t, true)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, mode := range []string{"json", "stream-before-text", "stream-after-text"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				const secret = "private-upstream-token"
				backend := &fakeProvider{generate: func(_ context.Context, _ engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
					if mode == "stream-after-text" {
						if err := emit(engine.Delta{Text: "Partial answer"}); err != nil {
							return engine.Result{}, err
						}
					}
					return engine.Result{}, errors.New(secret)
				}}
				wrapped, err := rag.New(backend, index, 10, 6000, rag.Options{Fallback: "model"})
				if err != nil {
					t.Fatal(err)
				}
				stream := mode != "json"
				w := invoke(testServer(t, wrapped, nil), apiRequest(path, fallbackRequestBody(t, path, stream)))
				if backend.calls.Load() != 1 || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), "Provider request failed") {
					t.Fatalf("fallback retried or hid/leaked backend error: %d %s", w.Code, w.Body.String())
				}
				if !stream {
					if w.Code != 502 || strings.Contains(w.Body.String(), fallbackNoticeKO) {
						t.Fatalf("backend failure returned an answer: %d %s", w.Code, w.Body.String())
					}
					return
				}
				text, terminals, failed := knowledgeStreamText(t, path, w.Body.String())
				wantText := ""
				if mode == "stream-after-text" {
					wantText = fallbackNoticeKO + "Partial answer"
				}
				if w.Code != 200 || !failed || terminals != 0 || text != wantText {
					t.Fatalf("failed fallback stream advertised success or incorrect text: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}
