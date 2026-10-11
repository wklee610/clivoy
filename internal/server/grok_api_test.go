package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/config"
	"github.com/wklee610/clivoy/internal/engine"
	"github.com/wklee610/clivoy/internal/provider"
)

// Exercise the documented Grok API configuration through every public chat
// protocol against an offline xAI-shaped upstream, never using real credentials.
func TestGrokAPIExampleAcrossProtocols(t *testing.T) {
	c, err := config.Load("../../examples/clivoy.grok.json")
	if err != nil {
		t.Fatal(err)
	}
	model := c.Models["grok-api"]
	xai := c.Providers[model.Provider]
	if xai.Type != "openai" || xai.BaseURL != "https://api.x.ai/v1" || xai.APIKeyEnv != "XAI_API_KEY" {
		t.Fatal("Grok API example does not select the official xAI API")
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", path, stream), func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer offline-xai-key" {
						t.Error("incorrect xAI routing or credentials")
					}
					var request struct {
						engine.Request
						Stream        bool `json:"stream"`
						StreamOptions struct {
							IncludeUsage bool `json:"include_usage"`
						} `json:"stream_options"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.Model != model.Model || request.Stream != stream || len(request.Messages) != 1 || string(request.Messages[0].Content) != `"hello"` {
						t.Errorf("request was not converted to xAI Chat Completions: %+v", request)
					}
					if stream {
						if !request.StreamOptions.IncludeUsage {
							t.Error("stream must request usage")
						}
						w.Header().Set("Content-Type", "text/event-stream")
						for _, chunk := range []string{
							`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"internal-reasoning"}}]}`,
							`{"choices":[{"index":0,"delta":{"content":"Hello from Grok"}}]}`,
							`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"completion_tokens_details":{"reasoning_tokens":2}}}`,
							`[DONE]`,
						} {
							fmt.Fprintf(w, "data: %s\n\n", chunk)
						}
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello from Grok","reasoning_content":"internal-reasoning"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2}}}`)
					}
				}))
				defer upstream.Close()
				backend, err := provider.NewOpenAI(upstream.URL+"/v1", "offline-xai-key")
				if err != nil {
					t.Fatal(err)
				}
				s := testServer(t, backend, func(o *Options) {
					o.Models = map[string]Model{"grok-api": {Provider: backend, UpstreamModel: model.Model, Owner: model.Provider, Capabilities: model.Capabilities}}
				})
				response := invoke(s, apiRequest(path, bodyForModel(path, "grok-api", stream)))
				body := response.Body.String()
				if response.Code != http.StatusOK || calls != 1 || !strings.Contains(body, "Hello from Grok") || !strings.Contains(body, `"model":"grok-api"`) {
					t.Fatalf("Grok API failed through %s: %d %s", path, response.Code, body)
				}
				if strings.Contains(body, "internal-reasoning") || strings.Contains(body, "offline-xai-key") {
					t.Fatal("private upstream fields escaped into the public response")
				}
				if stream && response.Header().Get("Content-Type") != "text/event-stream" {
					t.Fatal("stream request did not return SSE")
				}
			})
		}
	}
}
