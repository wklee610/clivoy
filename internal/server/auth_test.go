package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMultipleAPIKeysAcrossEndpoints(t *testing.T) {
	for _, path := range []string{"/v1/models", "/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/images/generations"} {
		for _, header := range []string{"Authorization", "X-Api-Key"} {
			for _, key := range []string{"legacy-key", "first-key", "second-key-longer", "wrong-key", ""} {
				t.Run(path+"/"+header+"/"+key, func(t *testing.T) {
					p := &imageTestProvider{}
					s := testServer(t, p, func(o *Options) {
						o.APIKey = "legacy-key"
						o.APIKeys = []string{"first-key", "second-key-longer"}
						m := o.Models["test-alias"]
						m.Capabilities = []string{"chat", "image_generation"}
						o.Models["test-alias"] = m
					})
					method, body := http.MethodPost, bodyFor(path, false)
					if path == "/v1/models" {
						method, body = http.MethodGet, ""
					} else if path == "/v1/images/generations" {
						body = `{"model":"test-alias","prompt":"a cat"}`
					}
					r := httptest.NewRequest(method, path, strings.NewReader(body))
					r.Header.Set("Content-Type", "application/json")
					if key != "" {
						value := key
						if header == "Authorization" {
							value = "Bearer " + key
						}
						r.Header.Set(header, value)
					}
					want := http.StatusUnauthorized
					if key != "" && key != "wrong-key" && (header == "Authorization" || path == "/v1/messages") {
						want = http.StatusOK
					}
					w := invoke(s, r)
					if w.Code != want {
						t.Fatalf("status=%d want=%d: %s", w.Code, want, w.Body.String())
					}
					if want == http.StatusUnauthorized && (p.chatCalls != 0 || p.imageCalls != 0) {
						t.Fatal("unauthorized request reached provider")
					}
					if key != "" && strings.Contains(w.Body.String(), key) {
						t.Fatal("response exposed an API key")
					}
				})
			}
		}
	}
}

func TestAPIKeyOptionsValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy string
		keys   []string
		valid  bool
	}{
		{name: "missing"},
		{name: "empty list", keys: []string{}},
		{name: "legacy whitespace", legacy: " \t\n"},
		{name: "legacy whitespace with valid list", legacy: " ", keys: []string{"key"}},
		{name: "empty list member", keys: []string{"key", ""}},
		{name: "whitespace list member", legacy: "legacy", keys: []string{"\t"}},
		{name: "legacy only", legacy: "legacy", valid: true},
		{name: "list only", keys: []string{"key", "another"}, valid: true},
		{name: "combined", legacy: "legacy", keys: []string{"key"}, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(Options{APIKey: tc.legacy, APIKeys: tc.keys, RequestTimeout: time.Second, MaxConcurrent: 1, MaxBodyBytes: 1024,
				Models: map[string]Model{"test": {Provider: &fakeProvider{}}}})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v got server=%v error=%v", tc.valid, s != nil, err)
			}
		})
	}
}

func TestAPIKeysSnapshotAndHealth(t *testing.T) {
	keys := []string{"first-key", "second-key"}
	s := testServer(t, &fakeProvider{}, func(o *Options) {
		o.APIKey = ""
		o.APIKeys = keys
	})
	keys[0], keys[1] = "replacement-one", "replacement-two"
	for _, tc := range []struct {
		key  string
		want int
	}{
		{"first-key", 200}, {"second-key", 200}, {"replacement-one", 401}, {"replacement-two", 401},
	} {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer "+tc.key)
		if got := invoke(s, r).Code; got != tc.want {
			t.Fatalf("original options mutation affected authorization: got %d want %d", got, tc.want)
		}
	}
	if s.options.APIKey != "" || s.options.APIKeys != nil {
		t.Fatal("server retained plaintext API key options")
	}
	for _, value := range []string{"", "Bearer wrong-key"} {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Header.Set("Authorization", value)
		if got := invoke(s, r).Code; got != http.StatusOK {
			t.Fatalf("health authentication changed: status=%d", got)
		}
	}
}

func TestAPIKeyHeaderPrecedence(t *testing.T) {
	s := testServer(t, &fakeProvider{}, func(o *Options) {
		o.APIKey = ""
		o.APIKeys = []string{"first-key", "second-key"}
	})
	for _, tc := range []struct {
		name, bearer, apiKey string
		want                 int
	}{
		{"valid bearer wins", "Bearer first-key", "wrong-key", 200},
		{"invalid bearer wins", "Bearer wrong-key", "second-key", 401},
		{"empty bearer falls back", "Bearer ", "second-key", 200},
		{"other authorization falls back", "Basic ignored", "second-key", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := apiRequest("/v1/messages", bodyFor("/v1/messages", false))
			r.Header.Set("Authorization", tc.bearer)
			r.Header.Set("X-Api-Key", tc.apiKey)
			if got := invoke(s, r).Code; got != tc.want {
				t.Fatalf("status=%d want=%d", got, tc.want)
			}
		})
	}
}
