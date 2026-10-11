package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
)

func TestOllamaChatToolsAndStreaming(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			key := ""
			if stream {
				key = "remote-ollama-key"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/ollama/v1/chat/completions" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				wantAuth := ""
				if key != "" {
					wantAuth = "Bearer " + key
				}
				if r.Header.Get("Authorization") != wantAuth {
					t.Error("incorrect optional authentication")
				}
				var body struct {
					engine.Request
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "namespace/qwen3:8b" || body.Stream != stream || len(body.Tools) != 1 || body.Tools[0].Function.Name != "weather" {
					t.Errorf("Ollama request not preserved: %+v", body)
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					for _, data := range []string{
						`{"choices":[{"index":0,"delta":{"content":"Hello from Ollama"}}]}`,
						`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":4}}`,
						`[DONE]`,
					} {
						fmt.Fprintf(w, "data: %s\n\n", data)
					}
					return
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":4}}`)
			}))
			defer server.Close()
			p, err := NewOllama(server.URL+"/ollama/v1/", key)
			if err != nil {
				t.Fatal(err)
			}
			req := basicRequest()
			req.Model = "namespace/qwen3:8b"
			req.Tools = []engine.Tool{{Type: "function", Function: engine.Function{Name: "weather", Parameters: json.RawMessage(`{"type":"object"}`)}}}
			var emitted strings.Builder
			var emit func(engine.Delta) error
			if stream {
				emit = func(delta engine.Delta) error { emitted.WriteString(delta.Text); return nil }
			}
			result, err := p.Generate(context.Background(), req, emit)
			if err != nil {
				t.Fatal(err)
			}
			if result.Usage.InputTokens != 5 || result.Usage.OutputTokens != 4 {
				t.Fatalf("unexpected usage: %+v", result.Usage)
			}
			if stream {
				if result.Text != "Hello from Ollama" || emitted.String() != result.Text || result.FinishReason != "stop" {
					t.Fatalf("unexpected streamed result: %+v", result)
				}
			} else if result.FinishReason != "tool_calls" || len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "weather" {
				t.Fatalf("unexpected tool call: %+v", result)
			}
		})
	}
}

func TestOllamaDefaultsAndCapabilities(t *testing.T) {
	p, err := NewOllama("", "")
	if err != nil {
		t.Fatal(err)
	}
	upstream := p.(*ollamaProvider).Provider.(*openAIProvider)
	if upstream.endpoint != "http://127.0.0.1:11434/v1/chat/completions" || upstream.apiKey != "" {
		t.Fatal("local Ollama defaults changed")
	}
	if _, ok := p.(engine.ImageProvider); ok {
		t.Fatal("Ollama must not advertise image generation")
	}
	for _, baseURL := range []string{"relative/v1", "ftp://localhost/v1", "http://private-key@localhost/v1", "http://localhost/v1?key=private-key", "http://localhost/v1#private-key"} {
		_, err := NewOllama(baseURL, "")
		if err == nil || strings.Contains(err.Error(), "private-key") {
			t.Fatalf("expected sanitized URL error, got %v", err)
		}
	}
}
