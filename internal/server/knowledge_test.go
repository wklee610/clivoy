package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
	"github.com/wklee610/clivoy/internal/knowledge"
	"github.com/wklee610/clivoy/internal/rag"
)

func knowledgeIndex(t *testing.T, ready bool) (*knowledge.Index, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("https://example.test/quasar\n\n# Quasar guide\n\nQuasar listens on port 12345.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := knowledge.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		if err := index.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return index, root
}

func TestKnowledgeAcrossProtocols(t *testing.T) {
	index, _ := knowledgeIndex(t, true)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				backend := &fakeProvider{generate: func(_ context.Context, req engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
					encoded, _ := json.Marshal(req.Messages)
					if !strings.Contains(string(encoded), "12345") || req.Model != "real-upstream-model" {
						t.Fatalf("retrieval or model routing missing: %s, model=%s", encoded, req.Model)
					}
					if emit != nil {
						if err := emit(engine.Delta{Text: "Use port 12345."}); err != nil {
							return engine.Result{}, err
						}
					}
					return engine.Result{Text: "Use port 12345.", FinishReason: "stop", Usage: engine.Usage{InputTokens: 20, OutputTokens: 5}}, nil
				}}
				wrapped, err := rag.New(backend, index, 10, 6000)
				if err != nil {
					t.Fatal(err)
				}
				s := testServer(t, wrapped, nil)
				body := strings.ReplaceAll(bodyFor(path, stream), "hello", "Quasar configuration")
				w := invoke(s, apiRequest(path, body))
				if w.Code != 200 || backend.calls.Load() != 1 {
					t.Fatalf("status=%d calls=%d body=%s", w.Code, backend.calls.Load(), w.Body.String())
				}
				for _, want := range []string{"Use port 12345.", "https://example.test/quasar"} {
					if !strings.Contains(w.Body.String(), want) {
						t.Fatalf("missing %q in %s", want, w.Body.String())
					}
				}
				if stream {
					terminal := "[DONE]"
					if path == "/v1/responses" {
						terminal = "response.completed"
					} else if path == "/v1/messages" {
						terminal = "message_stop"
					}
					if strings.LastIndex(w.Body.String(), terminal) < strings.Index(w.Body.String(), "https://example.test/quasar") {
						t.Fatalf("sources followed terminal event: %s", w.Body.String())
					}
				}
			})
		}
	}
}

func TestKnowledgeSearchFailurePrecedesStreamHeaders(t *testing.T) {
	index, _ := knowledgeIndex(t, false)
	backend := &fakeProvider{}
	wrapped, err := rag.New(backend, index, 10, 6000)
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(t, wrapped, nil)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		w := invoke(s, apiRequest(path, bodyFor(path, true)))
		if w.Code != 503 || strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || backend.calls.Load() != 0 {
			t.Fatalf("retrieval error started a successful stream: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestKnowledgeNoMatchAcrossProtocols(t *testing.T) {
	index, _ := knowledgeIndex(t, true)
	backend := &fakeProvider{}
	wrapped, err := rag.New(backend, index, 10, 6000)
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(t, wrapped, nil)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			w := invoke(s, apiRequest(path, strings.ReplaceAll(bodyFor(path, stream), "hello", "UnknownProductXYZ")))
			if w.Code != 200 || backend.calls.Load() != 0 || strings.Contains(w.Body.String(), "https://example.test/quasar") {
				t.Fatalf("no-match called backend or cited irrelevant source: %d %s", w.Code, w.Body.String())
			}
		}
	}
}

func TestKnowledgeInspectionRoutes(t *testing.T) {
	index, root := knowledgeIndex(t, true)
	s := testServer(t, &fakeProvider{}, func(o *Options) {
		o.KnowledgeBases = map[string]KnowledgeBase{"default": {Index: index, TopK: 10, MaxContextTokens: 6000, RefreshInterval: "30s", RefreshFailed: func() bool { return true }}}
	})
	for _, path := range []string{"/v1/search", "/v1/knowledge/status"} {
		r := apiRequest(path, `{"query":"Quasar"}`)
		if path == "/v1/knowledge/status" {
			r.Method = http.MethodGet
		}
		r.Header.Del("Authorization")
		if w := invoke(s, r); w.Code != 401 {
			t.Fatalf("unauthenticated source access: %d", w.Code)
		}
		r = apiRequest(path, `{"query":"Quasar"}`)
		if path == "/v1/knowledge/status" {
			r.Method = http.MethodGet
		}
		w := invoke(s, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), root) {
			t.Fatalf("inspection failed or exposed absolute root: %d %s", w.Code, w.Body.String())
		}
		if path == "/v1/search" && !strings.Contains(w.Body.String(), "12345") {
			t.Fatalf("search did not return evidence: %s", w.Body.String())
		}
		if path == "/v1/knowledge/status" && (!strings.Contains(w.Body.String(), `"refresh_failed":true`) || !strings.Contains(w.Body.String(), `"documents":1`)) {
			t.Fatalf("last good index status missing: %s", w.Body.String())
		}
	}
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"query":"Quasar","knowledge":"missing"}`, 404},
		{`{"query":"Quasar","limit":11}`, 400},
		{`{"query":"Quasar","limit":-1}`, 400},
		{`{"query":" "}`, 400},
		{`{"query":"Quasar","root":"/arbitrary"}`, 400},
	} {
		if w := invoke(s, apiRequest("/v1/search", tc.body)); w.Code != tc.code {
			t.Fatalf("%s status=%d body=%s", tc.body, w.Code, w.Body.String())
		}
	}
}
