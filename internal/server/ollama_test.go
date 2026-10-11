package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
	"github.com/wklee610/clivoy/internal/provider"
)

// A native model remains selectable even before it appears in a discovered
// catalog. Exercise the real HTTP adapter and all public chat protocols.
func TestOllamaNativeModelAcrossProtocols(t *testing.T) {
	const publicModel = "ollama/library/model:tag"
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", path, stream), func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "" {
						t.Error("incorrect local Ollama routing or gateway credentials forwarded")
					}
					var request struct {
						engine.Request
						Stream bool `json:"stream"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.Model != "library/model:tag" || request.Stream != stream || len(request.Messages) != 1 || string(request.Messages[0].Content) != `"hello"` {
						t.Errorf("native Ollama request changed: %+v", request)
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						for _, chunk := range []string{
							`{"choices":[{"index":0,"delta":{"content":"Hello from Ollama"}}]}`,
							`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
							`[DONE]`,
						} {
							fmt.Fprintf(w, "data: %s\n\n", chunk)
						}
						return
					}
					fmt.Fprint(w, `{"choices":[{"message":{"content":"Hello from Ollama"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
				}))
				defer upstream.Close()
				backend, err := provider.NewOllama(upstream.URL+"/v1", "")
				if err != nil {
					t.Fatal(err)
				}
				s := testServer(t, backend, func(o *Options) {
					o.Models = nil
					o.NativeProviders = map[string]Model{"ollama": {Provider: backend, Owner: "ollama", Capabilities: []string{"chat"}}}
				})
				inventoryRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				inventoryRequest.Header.Set("Authorization", "Bearer test-gateway-key")
				inventoryResponse := invoke(s, inventoryRequest)
				var inventory struct {
					Data []json.RawMessage `json:"data"`
				}
				if inventoryResponse.Code != http.StatusOK || json.Unmarshal(inventoryResponse.Body.Bytes(), &inventory) != nil || len(inventory.Data) != 0 || calls.Load() != 0 {
					t.Fatalf("native registration must work with an empty inventory: %d %s", inventoryResponse.Code, inventoryResponse.Body.String())
				}
				response := invoke(s, apiRequest(path, bodyForModel(path, publicModel, stream)))
				body := response.Body.String()
				if response.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(body, "Hello from Ollama") || !strings.Contains(body, `"model":"`+publicModel+`"`) {
					t.Fatalf("Ollama failed through %s: %d %s; calls=%d", path, response.Code, body, calls.Load())
				}
				if stream && response.Header().Get("Content-Type") != "text/event-stream" {
					t.Fatal("stream request did not return SSE")
				}
			})
		}
	}
}

func TestOllamaNativeImagesRequireVisionAlias(t *testing.T) {
	const nativeModel = "ollama/library/model:tag"
	dataURL := "data:image/png;base64," + samplePNG
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"` + nativeModel + `","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`},
		{"/v1/responses", `{"model":"` + nativeModel + `","input":[{"role":"user","content":[{"type":"input_image","image_url":"` + dataURL + `"}]}]}`},
		{"/v1/messages", `{"model":"` + nativeModel + `","max_tokens":100,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + samplePNG + `"}}]}]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected image request: %s %s", r.Method, r.URL.Path)
				}
				var request engine.Request
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Model != "library/model:tag" || len(request.Messages) != 1 {
					t.Errorf("unexpected vision request: %+v", request)
				} else {
					parts, err := engine.ParseContent(request.Messages[0].Content)
					if err != nil || len(parts) != 1 || parts[0].ImageURL == nil || parts[0].ImageURL.URL != dataURL {
						t.Errorf("image did not reach Ollama unchanged: %+v, %v", parts, err)
					}
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":"I see an image"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			backend, err := provider.NewOllama(upstream.URL+"/v1", "")
			if err != nil {
				t.Fatal(err)
			}
			s := testServer(t, backend, func(o *Options) {
				o.Models = map[string]Model{"ollama-vision": {Provider: backend, UpstreamModel: "library/model:tag", Owner: "ollama", Capabilities: []string{"chat", "vision"}}}
				o.NativeProviders = map[string]Model{"ollama": {Provider: backend, Owner: "ollama", Capabilities: []string{"chat"}}}
			})
			response := invoke(s, apiRequest(tc.path, tc.body))
			if response.Code != http.StatusBadRequest || calls.Load() != 0 || !strings.Contains(response.Body.String(), "vision") {
				t.Fatalf("native chat-only route must reject images before HTTP: %d %s; calls=%d", response.Code, response.Body.String(), calls.Load())
			}
			body := strings.Replace(tc.body, `"`+nativeModel+`"`, `"ollama-vision"`, 1)
			response = invoke(s, apiRequest(tc.path, body))
			if response.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(response.Body.String(), "I see an image") || !strings.Contains(response.Body.String(), `"model":"ollama-vision"`) {
				t.Fatalf("explicit Ollama vision alias failed: %d %s; calls=%d", response.Code, response.Body.String(), calls.Load())
			}
		})
	}
}
