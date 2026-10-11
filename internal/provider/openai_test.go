package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

func TestOpenAIRequestAndCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fake-upstream-key" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["stream"]) != "false" || string(body["max_tokens"]) != "100" || len(body["tools"]) == 0 {
			t.Errorf("request settings not forwarded: %s", body)
		}
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Seoul\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer server.Close()
	p, err := NewOpenAI(server.URL+"/v1/", "fake-upstream-key")
	if err != nil {
		t.Fatal(err)
	}
	req := basicRequest()
	maxTokens := 100
	req.MaxTokens = &maxTokens
	req.Tools = []engine.Tool{{Type: "function", Function: engine.Function{Name: "weather", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	result, err := p.Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "tool_calls" || len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Arguments != `{"city":"Seoul"}` || result.Usage.InputTokens != 10 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestOpenAIStreamToolsUsageAndText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream  bool `json:"stream"`
			Options struct {
				Usage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream || !body.Options.Usage {
			t.Error("stream and usage not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, data := range []string{
			`{"choices":[{"index":0,"delta":{"content":"Checking "}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Seoul\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, ": keepalive\r\ndata: %s\r\n\r\n", data)
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	p, _ := NewOpenAI(server.URL+"/v1", "")
	var deltas []engine.Delta
	result, err := p.Generate(context.Background(), basicRequest(), func(d engine.Delta) error { deltas = append(deltas, d); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Checking " || len(deltas) != 3 || len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Arguments != `{"city":"Seoul"}` || result.Usage.OutputTokens != 5 || result.FinishReason != "tool_calls" {
		t.Fatalf("unexpected stream result: %+v; deltas=%+v", result, deltas)
	}
}

func TestOpenAIRejectsTruncatedAndInvalidStreams(t *testing.T) {
	tests := map[string]string{
		"no_done":        "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"no_finish":      "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\ndata: [DONE]\n\n",
		"bad_json":       "data: {\n\ndata: [DONE]\n\n",
		"upstream_error": "data: {\"error\":{\"message\":\"secret-value\"}}\n\n",
		"tool_index":     "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":999999999}]}}]}\n\n",
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := readOpenAIStream(context.Background(), strings.NewReader(stream), func(engine.Delta) error { return nil })
			var safe *engine.Error
			if !errors.As(err, &safe) || safe.Status != 502 || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("expected safe error, got %v", err)
			}
		})
	}
}

func TestOpenAIErrorSanitizationAndRedirect(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500, 307} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var targetHit atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHit.Store(true) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"secret-value"}}`)
			}))
			defer server.Close()
			p, _ := NewOpenAI(server.URL, "fake-key")
			_, err := p.Generate(context.Background(), basicRequest(), nil)
			var safe *engine.Error
			if !errors.As(err, &safe) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("expected safe error, got %v", err)
			}
			want := 502
			if status == 429 {
				want = 429
			}
			if safe.Status != want {
				t.Fatalf("status=%d, want %d", safe.Status, want)
			}
			if targetHit.Load() {
				t.Fatal("followed upstream redirect")
			}
		})
	}
}

func TestOpenAICancellationAndEmitterFailure(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			p, _ := NewOpenAI(server.URL, "")
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			var emit func(engine.Delta) error
			if stream {
				emit = func(engine.Delta) error { return nil }
			}
			_, err := p.Generate(ctx, basicRequest(), emit)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unexpected cancel error: %v", err)
			}
		})
	}
	t.Run("emitter", func(t *testing.T) {
		sentinel := errors.New("disconnected")
		_, err := readOpenAIStream(context.Background(), strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"), func(engine.Delta) error { return sentinel })
		if !errors.Is(err, sentinel) {
			t.Fatalf("emitter failure swallowed: %v", err)
		}
	})
}

func TestOpenAIMissingUsageAndResponseLimits(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if large {
					fmt.Fprint(w, strings.Repeat("x", maxOutputBytes+1))
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"error":null}`)
				}
			}))
			defer server.Close()
			p, _ := NewOpenAI(server.URL, "")
			result, err := p.Generate(context.Background(), basicRequest(), nil)
			if large {
				if err == nil {
					t.Fatal("oversize response accepted")
				}
			} else if err != nil || result.Text != "hello" {
				t.Fatalf("missing usage should be supported: %+v, %v", result, err)
			}
		})
	}
}
