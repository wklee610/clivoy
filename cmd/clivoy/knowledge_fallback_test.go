package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/config"
	"github.com/wklee610/clivoy/internal/engine"
)

type fallbackBindingBackend struct {
	calls int
}

func (*fallbackBindingBackend) Validate(engine.Request) error { return nil }
func (b *fallbackBindingBackend) Generate(_ context.Context, _ engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
	b.calls++
	return engine.Result{Text: "General answer.", FinishReason: "stop"}, nil
}

func TestKnowledgeFallbackIsScopedToModelAlias(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.Provider{"remote": {Type: "openai"}}
	base := config.DefaultKnowledgeBase()
	base.Root = t.TempDir()
	c.KnowledgeBases = map[string]config.KnowledgeBase{"shared": base}
	runtime, err := startKnowledge(context.Background(), c, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.close()
	backend := &fallbackBindingBackend{}
	choices := map[string]config.Model{
		"strict":   {Provider: "remote", Model: "upstream", Knowledge: "shared"},
		"disabled": {Provider: "remote", Model: "upstream", Knowledge: "shared", KnowledgeFallback: "none"},
		"fallback": {Provider: "remote", Model: "upstream", Knowledge: "shared", KnowledgeFallback: "model"},
		"ordinary": {Provider: "remote", Model: "upstream"},
	}
	models, err := buildKnowledgeModels(c, choices, map[string]engine.Provider{"remote": backend}, runtime.bases)
	if err != nil {
		t.Fatal(err)
	}
	request := engine.Request{Messages: []engine.Message{{Role: "user", Content: []byte(`"새 질문입니다"`)}}}
	for _, alias := range []string{"strict", "fallback", "disabled", "strict", "ordinary"} {
		before := backend.calls
		result, err := models[alias].Provider.Generate(context.Background(), request, nil)
		if err != nil {
			t.Fatal(err)
		}
		wantCalls := 0
		if alias == "fallback" || alias == "ordinary" {
			wantCalls = 1
		}
		if backend.calls-before != wantCalls {
			t.Fatalf("alias %q called backend %d times", alias, backend.calls-before)
		}
		if models[alias].Owner != "remote" || models[alias].UpstreamModel != "upstream" {
			t.Fatalf("fallback changed alias routing: %#v", models[alias])
		}
		switch alias {
		case "fallback":
			if result.Text != "지식 베이스에서 관련 문서를 찾지 못해 일반 지식으로 답변합니다.\n\nGeneral answer." {
				t.Fatalf("fallback option was not applied: %q", result.Text)
			}
		case "ordinary":
			if result.Text != "General answer." || models[alias].Provider != backend {
				t.Fatal("fallback changed the ordinary provider")
			}
		default:
			if !strings.HasPrefix(result.Text, "관련 문서를 찾지 못했습니다.") {
				t.Fatalf("fallback leaked into strict alias %q: %q", alias, result.Text)
			}
		}
	}
}
