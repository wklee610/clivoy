package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

type fakeProvider struct {
	validate func(engine.Request) error
	generate func(context.Context, engine.Request, func(engine.Delta) error) (engine.Result, error)
	calls    atomic.Int32
}

func (p *fakeProvider) Validate(r engine.Request) error {
	if p.validate != nil {
		return p.validate(r)
	}
	return nil
}
func (p *fakeProvider) Generate(ctx context.Context, r engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
	p.calls.Add(1)
	if p.generate != nil {
		return p.generate(ctx, r, emit)
	}
	return engine.Result{Text: "hello", Usage: engine.Usage{InputTokens: 7, OutputTokens: 2}, FinishReason: "stop"}, nil
}
func testServer(t *testing.T, p engine.Provider, edit func(*Options)) *Server {
	t.Helper()
	o := Options{APIKey: "test-gateway-key", RequestTimeout: 2 * time.Second, MaxConcurrent: 1, MaxQueue: 1, MaxBodyBytes: 1 << 20, Models: map[string]Model{"test-alias": {Provider: p, UpstreamModel: "real-upstream-model", Owner: "test"}}}
	if edit != nil {
		edit(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func bodyFor(path string, stream bool) string {
	streamField := ""
	if stream {
		streamField = `,"stream":true`
	}
	switch path {
	case "/v1/messages":
		return `{"model":"test-alias","max_tokens":100,"messages":[{"role":"user","content":"hello"}]` + streamField + `}`
	case "/v1/responses":
		return `{"model":"test-alias","input":"hello"` + streamField + `}`
	default:
		return `{"model":"test-alias","messages":[{"role":"user","content":"hello"}]` + streamField + `}`
	}
}
func apiRequest(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer test-gateway-key")
	return r
}
func invoke(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func asyncInvoke(s *Server, r *http.Request) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- invoke(s, r) }()
	return done
}
func awaitResponse(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-done:
		return w
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not finish")
		return nil
	}
}
func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not reached")
}

func TestAuthenticationAcrossProtocols(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		for _, auth := range []string{"missing", "wrong", "bearer", "api-key"} {
			t.Run(path+"/"+auth, func(t *testing.T) {
				p := &fakeProvider{}
				s := testServer(t, p, nil)
				r := apiRequest(path, bodyFor(path, false))
				r.Header.Del("Authorization")
				switch auth {
				case "wrong":
					r.Header.Set("Authorization", "Bearer wrong")
				case "bearer":
					r.Header.Set("Authorization", "Bearer test-gateway-key")
				case "api-key":
					r.Header.Set("X-Api-Key", "test-gateway-key")
				}
				w := invoke(s, r)
				valid := auth == "bearer" || (auth == "api-key" && path == "/v1/messages")
				want := http.StatusUnauthorized
				if valid {
					want = http.StatusOK
				}
				if w.Code != want {
					t.Fatalf("status=%d want=%d: %s", w.Code, want, w.Body.String())
				}
				if !valid && p.calls.Load() != 0 {
					t.Fatal("unauthorized request reached provider")
				}
				if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" {
					t.Fatal("missing response headers")
				}
				var data map[string]any
				if json.Unmarshal(w.Body.Bytes(), &data) != nil {
					t.Fatal("response is not JSON")
				}
				if !valid && path == "/v1/messages" && data["type"] != "error" {
					t.Fatal("Anthropic authentication error has wrong envelope")
				}
			})
		}
	}
}

func TestModelAliasAndUsage(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			p := &fakeProvider{generate: func(_ context.Context, r engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
				if r.Model != "real-upstream-model" {
					t.Errorf("alias was not resolved: %q", r.Model)
				}
				return engine.Result{Text: "hello", Usage: engine.Usage{InputTokens: 7, OutputTokens: 2}, FinishReason: "stop"}, nil
			}}
			w := invoke(testServer(t, p, nil), apiRequest(path, bodyFor(path, false)))
			if w.Code != 200 {
				t.Fatalf("request failed: %s", w.Body.String())
			}
			var response map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &response)
			if response["model"] != "test-alias" {
				t.Fatal("upstream model leaked in public alias field")
			}
			usage := response["usage"].(map[string]any)
			input, output := "input_tokens", "output_tokens"
			if path == "/v1/chat/completions" {
				input, output = "prompt_tokens", "completion_tokens"
			}
			if usage[input] != float64(7) || usage[output] != float64(2) {
				t.Fatalf("usage not translated: %v", usage)
			}
		})
	}
}

func TestQueueFullAndConcurrencyBound(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	var active, maxActive atomic.Int32
	p := &fakeProvider{generate: func(ctx context.Context, _ engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-gate:
			return engine.Result{Text: "ok"}, nil
		case <-ctx.Done():
			return engine.Result{}, ctx.Err()
		}
	}}
	s := testServer(t, p, nil)
	path := "/v1/chat/completions"
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	first := asyncInvoke(s, apiRequest(path, bodyFor(path, false)).WithContext(firstCtx))
	waitFor(t, func() bool { return p.calls.Load() == 1 })
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	second := asyncInvoke(s, apiRequest(path, bodyFor(path, false)).WithContext(secondCtx))
	waitFor(t, func() bool { return len(s.queue) == 1 })
	w := invoke(s, apiRequest(path, bodyFor(path, false)))
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("full queue should reject with retry advice: %d %s", w.Code, w.Body.String())
	}
	if p.calls.Load() != 1 {
		t.Fatal("queued/rejected request started a provider")
	}
	firstCancel()
	if got := awaitResponse(t, first).Code; got != 408 {
		t.Fatalf("cancelled request returned %d", got)
	}
	waitFor(t, func() bool { return p.calls.Load() == 2 })
	secondCancel()
	awaitResponse(t, second)
	if maxActive.Load() != 1 || len(s.slots) != 0 || len(s.queue) != 0 {
		t.Fatal("concurrency bound or permit release failed")
	}
}

func TestQueuedTimeoutDoesNotGenerate(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	p := &fakeProvider{generate: func(ctx context.Context, _ engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
		select {
		case <-gate:
			return engine.Result{}, nil
		case <-ctx.Done():
			return engine.Result{}, ctx.Err()
		}
	}}
	s := testServer(t, p, nil)
	path := "/v1/chat/completions"
	activeCtx, activeCancel := context.WithCancel(context.Background())
	defer activeCancel()
	active := asyncInvoke(s, apiRequest(path, bodyFor(path, false)).WithContext(activeCtx))
	waitFor(t, func() bool { return p.calls.Load() == 1 })
	queuedCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	w := invoke(s, apiRequest(path, bodyFor(path, false)).WithContext(queuedCtx))
	if w.Code != 504 || p.calls.Load() != 1 || len(s.queue) != 0 {
		t.Fatalf("queued timeout started a provider or retained queue capacity: status=%d calls=%d", w.Code, p.calls.Load())
	}
	activeCancel()
	awaitResponse(t, active)
}

func TestActiveRequestTimeoutReleasesSlot(t *testing.T) {
	p := &fakeProvider{generate: func(ctx context.Context, _ engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
		<-ctx.Done()
		return engine.Result{}, ctx.Err()
	}}
	s := testServer(t, p, func(o *Options) { o.RequestTimeout = 25 * time.Millisecond })
	w := invoke(s, apiRequest("/v1/chat/completions", bodyFor("/v1/chat/completions", false)))
	if w.Code != 504 || len(s.slots) != 0 {
		t.Fatalf("timeout failed to release provider slot: %d", w.Code)
	}
}

func TestDisconnectCancelsProvider(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			p := &fakeProvider{generate: func(ctx context.Context, _ engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
				close(started)
				<-ctx.Done()
				close(stopped)
				return engine.Result{}, ctx.Err()
			}}
			ts := httptest.NewServer(testServer(t, p, nil))
			defer ts.Close()
			r, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(bodyFor(path, true)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer test-gateway-key")
			resp, err := ts.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				resp.Body.Close()
				t.Fatal("provider was not started")
			}
			_ = resp.Body.Close()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("client disconnect did not cancel provider")
			}
		})
	}
}

func TestInvalidRequestsDoNotReachProvider(t *testing.T) {
	tests := []struct {
		name, path, body string
		status           int
	}{
		{"unknown-field", "/v1/chat/completions", `{"model":"test-alias","messages":[{"role":"user","content":"hi"}],"unsupported":true}`, 400},
		{"chat-image", "/v1/chat/completions", `{"model":"test-alias","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}]}`, 400},
		{"anthropic-image", "/v1/messages", `{"model":"test-alias","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/x.png"}}]}]}`, 400},
		{"responses-image", "/v1/responses", `{"model":"test-alias","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/x.png"}]}]}`, 400},
		{"missing-max-tokens", "/v1/messages", `{"model":"test-alias","messages":[{"role":"user","content":"hi"}]}`, 400},
		{"zero-max-tokens", "/v1/messages", `{"model":"test-alias","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`, 400},
		{"negative-max-tokens", "/v1/chat/completions", `{"model":"test-alias","max_tokens":-1,"messages":[{"role":"user","content":"hi"}]}`, 400},
		{"conflicting-max-tokens", "/v1/chat/completions", `{"model":"test-alias","max_tokens":1,"max_completion_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, 400},
		{"responses-max-tokens", "/v1/responses", `{"model":"test-alias","max_output_tokens":0,"input":"hi"}`, 400},
		{"storage", "/v1/responses", `{"model":"test-alias","store":true,"input":"hi"}`, 400},
		{"missing-message", "/v1/chat/completions", `{"model":"test-alias","messages":[]}`, 400},
		{"unknown-model", "/v1/chat/completions", `{"model":"missing","messages":[{"role":"user","content":"hi"}]}`, 404},
		{"multiple-json", "/v1/chat/completions", bodyFor("/v1/chat/completions", false) + ` {}`, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakeProvider{}
			w := invoke(testServer(t, p, nil), apiRequest(tt.path, tt.body))
			if w.Code != tt.status || p.calls.Load() != 0 {
				t.Fatalf("invalid request accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBodyLimitAndContentType(t *testing.T) {
	p := &fakeProvider{}
	s := testServer(t, p, func(o *Options) { o.MaxBodyBytes = 64 })
	w := invoke(s, apiRequest("/v1/chat/completions", bodyFor("/v1/chat/completions", false)))
	if w.Code != 413 {
		t.Fatalf("body limit returned %d: %s", w.Code, w.Body.String())
	}
	r := apiRequest("/v1/messages", bodyFor("/v1/messages", false))
	r.Header.Set("Content-Type", "text/plain")
	if got := invoke(s, r).Code; got != 415 {
		t.Fatalf("unsupported content type returned %d", got)
	}
	if p.calls.Load() != 0 {
		t.Fatal("invalid body reached provider")
	}
}

func TestProviderErrorsNeverExposeRawSecrets(t *testing.T) {
	const secret = "upstream-secret-token-and-private-path"
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		for _, stream := range []bool{false, true} {
			name := path
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				p := &fakeProvider{generate: func(context.Context, engine.Request, func(engine.Delta) error) (engine.Result, error) {
					return engine.Result{}, errors.New(secret)
				}}
				w := invoke(testServer(t, p, nil), apiRequest(path, bodyFor(path, stream)))
				if strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), "Provider request failed") {
					t.Fatalf("unsafe provider error: %s", w.Body.String())
				}
				if !stream && w.Code != 502 {
					t.Fatalf("non-stream failure status=%d", w.Code)
				}
				if stream && (strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "event: message_stop") || strings.Contains(w.Body.String(), "event: response.completed")) {
					t.Fatal("failed stream advertised successful completion")
				}
			})
		}
	}
	for _, fromValidate := range []bool{false, true} {
		p := &fakeProvider{}
		if fromValidate {
			p.validate = func(engine.Request) error { return errors.New(secret) }
		} else {
			p.generate = func(context.Context, engine.Request, func(engine.Delta) error) (engine.Result, error) {
				return engine.Result{ToolCalls: []engine.ToolCall{{ID: "call", Type: "function", Function: engine.FunctionCall{Name: "fn", Arguments: secret}}}}, nil
			}
		}
		w := invoke(testServer(t, p, nil), apiRequest("/v1/chat/completions", bodyFor("/v1/chat/completions", false)))
		if w.Code != 502 || strings.Contains(w.Body.String(), secret) {
			t.Fatalf("unsafe validation error: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestHealthAndModelInventory(t *testing.T) {
	p := &fakeProvider{}
	s := testServer(t, p, nil)
	w := invoke(s, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 200 {
		t.Fatal("health check requires authentication")
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if invoke(s, r).Code != 401 {
		t.Fatal("model inventory exposed without authentication")
	}
	r.Header.Set("Authorization", "Bearer test-gateway-key")
	w = invoke(s, r)
	data, _ := io.ReadAll(w.Result().Body)
	if w.Code != 200 || !strings.Contains(string(data), "test-alias") || strings.Contains(string(data), "real-upstream-model") {
		t.Fatalf("unexpected model inventory: %s", data)
	}
}
