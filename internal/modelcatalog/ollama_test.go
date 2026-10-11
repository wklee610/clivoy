package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoverOllamaReadOnlyCatalog(t *testing.T) {
	for _, key := range []string{"", "private-ollama-key"} {
		t.Run(fmt.Sprint(key != ""), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodGet || r.URL.Path != "/ollama/v1/models" || len(body) != 0 {
					t.Errorf("model discovery must only GET metadata: %s %s", r.Method, r.URL.Path)
				}
				wantAuth := ""
				if key != "" {
					wantAuth = "Bearer " + key
				}
				if r.Header.Get("Authorization") != wantAuth || r.Header.Get("Accept") != "application/json" {
					t.Error("incorrect request headers")
				}
				fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3:8b","owned_by":"library"},{"id":"namespace/model:latest"},{"id":"qwen3:8b"},{"id":"invalid model"},{"id":""}]}`)
			}))
			defer server.Close()
			got, err := DiscoverOllama(context.Background(), server.URL+"/ollama/v1/", key)
			want := []Model{
				{ID: "namespace/model:latest", Name: "namespace/model:latest", Capabilities: []string{"chat"}},
				{ID: "qwen3:8b", Name: "qwen3:8b", Capabilities: []string{"chat"}},
			}
			if err != nil || !reflect.DeepEqual(got, want) || requests.Load() != 1 {
				t.Fatalf("unexpected catalog: %+v, %v; requests=%d", got, err, requests.Load())
			}
		})
	}
}

func TestDiscoverOllamaEmptyAndMalformedCatalogs(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `null`, `{}`, `{"data":null}`, `{"data":{}}`, `{"data":[{"id":123}]}`, `{"data":[]} {}`, `private-secret-not-json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			models, err := DiscoverOllama(context.Background(), server.URL+"/v1", "")
			if body == `{"data":[]}` {
				if err != nil || len(models) != 0 {
					t.Fatalf("empty Ollama installation should be valid: %+v, %v", models, err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("expected sanitized metadata error, got %v", err)
			}
		})
	}
}

func TestDiscoverOllamaBounds(t *testing.T) {
	for _, limit := range []string{"bytes", "models"} {
		t.Run(limit, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if limit == "bytes" {
					fmt.Fprint(w, strings.Repeat(" ", maxCatalogBytes+1))
					return
				}
				models := make([]map[string]string, maxOllamaModels+1)
				for i := range models {
					models[i] = map[string]string{"id": fmt.Sprintf("model-%d", i)}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
			}))
			defer server.Close()
			_, err := DiscoverOllama(context.Background(), server.URL, "")
			if err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("expected %s limit error, got %v", limit, err)
			}
		})
	}
}

func TestDiscoverOllamaErrorsAndRedirectsAreSafe(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var followed atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL+"/private-secret")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"private-secret"}`)
			}))
			defer server.Close()
			_, err := DiscoverOllama(context.Background(), server.URL+"/private-prefix/v1", "private-secret")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), "private-") || followed.Load() {
				t.Fatalf("unsafe discovery failure or followed redirect: %v", err)
			}
		})
	}
	for _, baseURL := range []string{"/relative", "file:///tmp/private-secret", "http://private-secret@localhost/v1", "http://localhost/v1?key=private-secret", "http://localhost/v1#private-secret"} {
		_, err := DiscoverOllama(context.Background(), baseURL, "private-secret")
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("expected sanitized URL error, got %v", err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	_, err := DiscoverOllama(context.Background(), server.URL+"/private-secret", "private-secret")
	if err == nil || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("expected sanitized connection failure, got %v", err)
	}
}

func TestDiscoverOllamaContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := DiscoverOllama(ctx, server.URL, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline was not honored: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = DiscoverOllama(ctx, server.URL, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation was not honored: %v", err)
	}
}
