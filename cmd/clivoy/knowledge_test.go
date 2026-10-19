package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/config"
	"github.com/wklee610/clivoy/internal/engine"
)

func TestKnowledgeRuntimeRefreshRecoveryAndShutdown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "notes")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	base := config.DefaultKnowledgeBase()
	base.Root, base.RefreshInterval = root, "1s"
	c := config.Default()
	c.KnowledgeBases = map[string]config.KnowledgeBase{"default": base}
	r, err := startKnowledge(context.Background(), c, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	index := r.bases["default"].Index
	if index.Stats().UpdatedAt.IsZero() || index.Stats().Documents != 0 {
		t.Fatal("empty directory was not indexed")
	}
	note := filepath.Join(root, "guide.md")
	if err := os.WriteFile(note, []byte("# Quasar\nQuasar uses port 12345.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	waitKnowledge(t, func() bool { return index.Stats().Documents == 1 })
	if err := os.Rename(root, root+".offline"); err != nil {
		t.Fatal(err)
	}
	waitKnowledge(t, r.bases["default"].RefreshFailed)
	hits, err := index.Search(context.Background(), "Quasar", 10, 6000)
	if err != nil || len(hits) == 0 {
		t.Fatalf("last successful snapshot lost: %v %v", hits, err)
	}
	if err := os.Rename(root+".offline", root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(note); err != nil {
		t.Fatal(err)
	}
	waitKnowledge(t, func() bool { return !r.bases["default"].RefreshFailed() && index.Stats().Documents == 0 })
	r.close()
	updated := index.Stats().UpdatedAt
	if err := os.WriteFile(note, []byte("# Quasar\nNew document after shutdown."), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if !index.Stats().UpdatedAt.Equal(updated) {
		t.Fatal("refresh continued after shutdown")
	}
}

func waitKnowledge(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("knowledge refresh did not reach expected state")
}

func TestKnowledgeStartupRejectsUnavailableRoot(t *testing.T) {
	c := config.Default()
	base := config.DefaultKnowledgeBase()
	base.Root = filepath.Join(t.TempDir(), "missing")
	c.KnowledgeBases = map[string]config.KnowledgeBase{"default": base}
	if r, err := startKnowledge(context.Background(), c, io.Discard); err == nil {
		r.close()
		t.Fatal("unavailable initial index was accepted")
	}
}

type knowledgeBackend struct{}

func (*knowledgeBackend) Validate(engine.Request) error { return nil }
func (*knowledgeBackend) Generate(_ context.Context, request engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
	return engine.Result{Text: string(request.Messages[0].Content), FinishReason: "stop"}, nil
}

func TestKnowledgeModelBindingsAreExplicit(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.Provider{"remote": {Type: "openai"}}
	for _, name := range []string{"alpha", "beta"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("# "+name+"\n"+name+" document."), 0600); err != nil {
			t.Fatal(err)
		}
		base := config.DefaultKnowledgeBase()
		base.Root = root
		if c.KnowledgeBases == nil {
			c.KnowledgeBases = map[string]config.KnowledgeBase{}
		}
		c.KnowledgeBases[name] = base
	}
	r, err := startKnowledge(context.Background(), c, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	backend := &knowledgeBackend{}
	choices := map[string]config.Model{
		"normal": {Provider: "remote", Model: "upstream"},
		"alpha":  {Provider: "remote", Model: "upstream", Knowledge: "alpha"},
		"beta":   {Provider: "remote", Model: "upstream", Knowledge: "beta"},
	}
	models, err := buildKnowledgeModels(c, choices, map[string]engine.Provider{"remote": backend}, r.bases)
	if err != nil {
		t.Fatal(err)
	}
	if models["normal"].Provider != backend || models["normal"].UpstreamModel != "upstream" {
		t.Fatal("ordinary model changed")
	}
	for _, name := range []string{"alpha", "beta"} {
		request := engine.Request{Messages: []engine.Message{{Role: "user", Content: []byte(`"alpha beta"`)}}}
		result, err := models[name].Provider.Generate(context.Background(), request, nil)
		if err != nil || !strings.Contains(result.Text, name+" document") {
			t.Fatalf("%s binding missing: %s %v", name, result.Text, err)
		}
		other := "alpha"
		if name == "alpha" {
			other = "beta"
		}
		if strings.Contains(result.Text, other+" document") {
			t.Fatal("document crossed knowledge base binding")
		}
	}
}
