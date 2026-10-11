package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

func bodyForModel(path, model string, stream bool) string {
	return strings.Replace(bodyFor(path, stream), `"test-alias"`, string(rawString(model)), 1)
}

func TestNativeModelRoutingAcrossProtocols(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, requested, upstream, provider string
				alias                               bool
			}{
				{"codex", "codex/native-model", "native-model", "codex", false},
				{"claude", "claude/claude-model[1m]", "claude-model[1m]", "claude", false},
				{"gemini", "gemini/flash", "flash", "gemini", false},
				{"grok", "grok/grok-4.7", "grok-4.7", "grok", false},
				{"ollama", "ollama/library/model:tag", "library/model:tag", "ollama", false},
				{"custom-provider", "coding/Org/model_1.alpha:beta[1m]", "Org/model_1.alpha:beta[1m]", "coding", false},
				{"maximum-length", "codex/" + strings.Repeat("a", 256), strings.Repeat("a", 256), "codex", false},
				{"exact-alias", "codex/native-model", "configured-upstream", "codex", true},
				{"exact-alias-before-native-validation", "codex/--configured alias", "configured-upstream", "codex", true},
			} {
				name := path + "/" + tc.name
				if stream {
					name += "/stream"
				}
				t.Run(name, func(t *testing.T) {
					validated := 0
					selected := &fakeProvider{
						validate: func(r engine.Request) error {
							validated++
							if r.Model != tc.upstream {
								t.Errorf("validated model=%q want=%q", r.Model, tc.upstream)
							}
							return nil
						},
						generate: func(_ context.Context, r engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
							if r.Model != tc.upstream {
								t.Errorf("generated model=%q want=%q", r.Model, tc.upstream)
							}
							if emit != nil {
								if err := emit(engine.Delta{Text: "hello"}); err != nil {
									return engine.Result{}, err
								}
							}
							return engine.Result{Text: "hello", FinishReason: "stop"}, nil
						},
					}
					unused := &fakeProvider{}
					s := testServer(t, unused, func(o *Options) {
						o.NativeProviders = map[string]Model{tc.provider: {Provider: selected, UpstreamModel: "ignored-binding-default"}}
						if tc.alias {
							o.Models[tc.requested] = Model{Provider: selected, UpstreamModel: tc.upstream}
							o.NativeProviders[tc.provider] = Model{Provider: unused}
						}
					})
					w := invoke(s, apiRequest(path, bodyForModel(path, tc.requested, stream)))
					if w.Code != http.StatusOK || validated != 1 || selected.calls.Load() != 1 || unused.calls.Load() != 0 {
						t.Fatalf("wrong routing: status=%d validated=%d selected=%d unused=%d body=%s", w.Code, validated, selected.calls.Load(), unused.calls.Load(), w.Body.String())
					}
					if stream && w.Header().Get("Content-Type") != "text/event-stream" {
						t.Fatal("native model request did not stream")
					}
					if !strings.Contains(w.Body.String(), `"model":`+string(rawString(tc.requested))) {
						t.Fatal("response did not preserve the requested model identifier")
					}
				})
			}
		}
	}
}

func TestNativeModelRejectsMalformedIDsBeforeProvider(t *testing.T) {
	invalid := []string{"", "-model", "_model", ".model", "/model", "[model]", "model name", " model", "model\n", "model\t", "model\x00", "mødél", "model\\path", "model;command", "model&command", "model|command", "model>file", "model<file", "model$HOME", "model`command`", "model\"quoted", "model'quoted", "model(command)", strings.Repeat("a", 257)}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, native := range invalid {
			t.Run(path+"/"+native, func(t *testing.T) {
				p := &fakeProvider{validate: func(engine.Request) error {
					t.Error("invalid native model reached provider validation")
					return nil
				}}
				s := testServer(t, p, func(o *Options) {
					o.NativeProviders = map[string]Model{"codex": {Provider: p}}
				})
				w := invoke(s, apiRequest(path, bodyForModel(path, "codex/"+native, false)))
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_request_error") || p.calls.Load() != 0 || len(s.slots) != 0 || len(s.queue) != 0 {
					t.Fatalf("invalid native model was not rejected: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestNativeModelRoutingUsesLongestConfiguredProviderName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		short    bool
		alias    bool
		upstream string
	}{
		{"slash-provider-unlisted-model", false, false, "org/native-model"},
		{"nested-provider-longest-prefix", true, false, "org/native-model"},
		{"exact-alias-before-longest-prefix", true, true, "configured-upstream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := &fakeProvider{generate: func(_ context.Context, r engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
				if r.Model != tc.upstream {
					t.Errorf("model=%q want=%q", r.Model, tc.upstream)
				}
				return engine.Result{Text: "hello", FinishReason: "stop"}, nil
			}}
			unused := &fakeProvider{}
			const requested = "team/codex/org/native-model"
			s := testServer(t, unused, func(o *Options) {
				o.NativeProviders = map[string]Model{"team/codex": {Provider: selected}}
				if tc.short {
					o.NativeProviders["team"] = Model{Provider: unused}
				}
				if tc.alias {
					o.Models[requested] = Model{Provider: selected, UpstreamModel: tc.upstream}
					o.NativeProviders["team/codex"] = Model{Provider: unused}
				}
			})
			w := invoke(s, apiRequest("/v1/chat/completions", bodyForModel("/v1/chat/completions", requested, false)))
			if w.Code != http.StatusOK || selected.calls.Load() != 1 || unused.calls.Load() != 0 {
				t.Fatalf("provider prefix resolution failed: status=%d selected=%d unused=%d body=%s", w.Code, selected.calls.Load(), unused.calls.Load(), w.Body.String())
			}
		})
	}
}

func TestNativeModelsRequireExplicitRegistrationAndDoNotGenerateImages(t *testing.T) {
	p := &imageTestProvider{}
	s := testServer(t, p, func(o *Options) {
		o.Models["http"] = Model{Provider: p, Owner: "http"}
		o.NativeProviders = map[string]Model{"codex": {Provider: p, Capabilities: []string{"chat", "vision"}}}
	})
	for _, name := range []string{"http/model", "unknown/model", "unknown/", "claude/model", "codex"} {
		w := invoke(s, apiRequest("/v1/chat/completions", bodyForModel("/v1/chat/completions", name, false)))
		if w.Code != http.StatusNotFound {
			t.Fatalf("unregistered model %q status=%d: %s", name, w.Code, w.Body.String())
		}
	}
	w := invoke(s, apiRequest("/v1/images/generations", `{"model":"codex/native-model","prompt":"a bird"}`))
	if w.Code != http.StatusNotFound || p.chatCalls != 0 || p.imageCalls != 0 {
		t.Fatalf("native routing escaped chat endpoints: status=%d chat=%d image=%d", w.Code, p.chatCalls, p.imageCalls)
	}
}

func TestNativeModelCapabilitiesArePreserved(t *testing.T) {
	for _, vision := range []bool{false, true} {
		p := &fakeProvider{}
		s := testServer(t, p, func(o *Options) {
			caps := []string{"chat"}
			if vision {
				caps = append(caps, "vision")
			}
			o.NativeProviders = map[string]Model{"codex": {Provider: p, Capabilities: caps}}
		})
		body := `{"model":"codex/native-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + samplePNG + `"}}]}]}`
		w := invoke(s, apiRequest("/v1/chat/completions", body))
		want, calls := http.StatusBadRequest, int32(0)
		if vision {
			want, calls = http.StatusOK, 1
		}
		if w.Code != want || p.calls.Load() != calls {
			t.Fatalf("vision=%v status=%d calls=%d: %s", vision, w.Code, p.calls.Load(), w.Body.String())
		}
	}
}

func TestNativeModelAuthenticationAndQueueRemainEnforced(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		p := &fakeProvider{}
		s := testServer(t, p, func(o *Options) {
			o.MaxQueue = 0
			o.NativeProviders = map[string]Model{"codex": {Provider: p}}
		})
		r := apiRequest(path, bodyForModel(path, "codex/native-model", false))
		r.Header.Del("Authorization")
		if w := invoke(s, r); w.Code != http.StatusUnauthorized {
			t.Fatalf("native request bypassed authentication: %d", w.Code)
		}
		s.slots <- struct{}{}
		w := invoke(s, apiRequest(path, bodyForModel(path, "codex/native-model", false)))
		<-s.slots
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" || p.calls.Load() != 0 {
			t.Fatalf("native request bypassed concurrency: %d calls=%d", w.Code, p.calls.Load())
		}
	}
}

func TestNewValidatesBothModelMaps(t *testing.T) {
	p := &fakeProvider{}
	for _, source := range []string{"aliases", "cli"} {
		for _, tc := range []struct {
			name  string
			model Model
		}{
			{"missing-provider", Model{}},
			{"empty-capabilities", Model{Provider: p, Capabilities: []string{}}},
			{"unknown-capability", Model{Provider: p, Capabilities: []string{"other"}}},
			{"duplicate-capability", Model{Provider: p, Capabilities: []string{"chat", "chat"}}},
			{"vision-without-chat", Model{Provider: p, Capabilities: []string{"vision"}}},
			{"image-without-provider", Model{Provider: p, Capabilities: []string{"image_generation"}}},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				o := Options{APIKey: "test-gateway-key", RequestTimeout: time.Second, MaxConcurrent: 1, MaxBodyBytes: 1024, Models: map[string]Model{"valid": {Provider: p}}}
				if source == "aliases" {
					o.Models["invalid"] = tc.model
				} else {
					o.NativeProviders = map[string]Model{"invalid": tc.model}
				}
				if _, err := New(o); err == nil {
					t.Fatal("invalid model binding was accepted")
				}
			})
		}
	}
}

func TestNewClonesNativeBindingsAndListsOnlySuppliedModels(t *testing.T) {
	p := &fakeProvider{}
	capabilities := []string{"chat", "vision"}
	aliases := map[string]Model{"codex/discovered": {Provider: p, Owner: "codex", Capabilities: capabilities}}
	bindings := map[string]Model{"codex": {Provider: p, Owner: "codex", Capabilities: capabilities}}
	s := testServer(t, p, func(o *Options) { o.Models, o.NativeProviders = aliases, bindings })
	capabilities[0] = "changed"
	delete(aliases, "codex/discovered")
	delete(bindings, "codex")
	m, err := s.resolveChatModel("codex/new-native")
	if err != nil || m.Provider != p || m.Owner != "codex" || !m.supports("chat") || !m.supports("vision") {
		t.Fatalf("native bindings were not cloned: %+v %v", m, err)
	}
	r := apiRequest("/v1/models", "")
	r.Method = http.MethodGet
	w := invoke(s, r)
	var result struct {
		Data []struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Data) != 1 || result.Data[0].ID != "codex/discovered" || strings.Join(result.Data[0].Capabilities, ",") != "chat,vision" {
		t.Fatalf("model inventory was changed by native routing: %d %s", w.Code, w.Body.String())
	}
	cliOnly := testServer(t, p, func(o *Options) {
		o.Models = nil
		o.NativeProviders = map[string]Model{"codex": {Provider: p}}
	})
	if w := invoke(cliOnly, apiRequest("/v1/chat/completions", bodyForModel("/v1/chat/completions", "codex/new-native", false))); w.Code != http.StatusOK {
		t.Fatalf("CLI-only configuration failed: %d %s", w.Code, w.Body.String())
	}
}
