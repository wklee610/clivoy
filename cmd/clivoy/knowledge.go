package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wklee610/clivoy/internal/config"
	"github.com/wklee610/clivoy/internal/engine"
	"github.com/wklee610/clivoy/internal/knowledge"
	"github.com/wklee610/clivoy/internal/provider"
	"github.com/wklee610/clivoy/internal/rag"
	"github.com/wklee610/clivoy/internal/server"
)

type knowledgeRuntime struct {
	bases  map[string]server.KnowledgeBase
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (r *knowledgeRuntime) close() {
	r.cancel()
	r.wg.Wait()
}

// One refresh loop and immutable index per configured base are shared by all
// model aliases. Stop loops before the server's log writer is closed.
func startKnowledge(ctx context.Context, c config.Config, log io.Writer) (*knowledgeRuntime, error) {
	ctx, cancel := context.WithCancel(ctx)
	r := &knowledgeRuntime{bases: make(map[string]server.KnowledgeBase), cancel: cancel}
	names := make([]string, 0, len(c.KnowledgeBases))
	for name := range c.KnowledgeBases {
		names = append(names, name)
	}
	sort.Strings(names)
	var logMu sync.Mutex
	for _, name := range names {
		base := c.KnowledgeBases[name]
		root, err := c.ResolveKnowledgeRoot(name)
		if err != nil {
			r.close()
			return nil, err
		}
		index, err := knowledge.New(root)
		if err != nil {
			r.close()
			return nil, fmt.Errorf("knowledge base %q: %w", name, err)
		}
		refreshCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		err = index.Refresh(refreshCtx)
		stop()
		if err != nil {
			r.close()
			return nil, fmt.Errorf("index knowledge base %q: %w", name, err)
		}
		var failed atomic.Bool
		r.bases[name] = server.KnowledgeBase{
			Index: index, TopK: base.TopK, MaxContextTokens: base.MaxContextTokens,
			RefreshInterval: base.RefreshInterval, RefreshFailed: failed.Load,
		}
		interval, err := time.ParseDuration(base.RefreshInterval)
		if err != nil || interval <= 0 {
			r.close()
			return nil, fmt.Errorf("knowledge base %q: invalid refresh_interval", name)
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					refreshCtx, stop := context.WithTimeout(ctx, 30*time.Second)
					err := index.Refresh(refreshCtx)
					stop()
					if ctx.Err() != nil {
						return
					}
					previous := failed.Swap(err != nil)
					if (err != nil) != previous {
						state := "recovered"
						if err != nil {
							state = "failed; retaining last successful index"
						}
						logMu.Lock()
						fmt.Fprintf(log, "Knowledge base %q refresh %s.\n", name, state)
						logMu.Unlock()
					}
				}
			}
		}()
	}
	return r, nil
}

func buildKnowledgeModels(c config.Config, choices map[string]config.Model, backends map[string]engine.Provider, bases map[string]server.KnowledgeBase) (map[string]server.Model, error) {
	models := make(map[string]server.Model, len(choices))
	knowledgeCLIs := make(map[string]engine.Provider)
	for alias, model := range choices {
		backend := backends[model.Provider]
		if model.Knowledge != "" {
			base, ok := bases[model.Knowledge]
			if !ok {
				return nil, fmt.Errorf("model %q: unknown knowledge base", alias)
			}
			p := c.Providers[model.Provider]
			if p.IsCLI() {
				var err error
				backend = knowledgeCLIs[model.Provider]
				if backend == nil {
					backend, err = provider.NewKnowledgeCLI(p.Type, p.Command, excludedCLIEnvironment(c, p.Type)...)
					if err != nil {
						return nil, fmt.Errorf("knowledge model %q: %w", alias, err)
					}
					knowledgeCLIs[model.Provider] = backend
				}
			}
			var err error
			backend, err = rag.New(backend, base.Index, base.TopK, base.MaxContextTokens, rag.Options{Fallback: model.KnowledgeFallback})
			if err != nil {
				return nil, fmt.Errorf("knowledge model %q: %w", alias, err)
			}
		}
		models[alias] = server.Model{Provider: backend, UpstreamModel: model.Model, Owner: model.Provider, Capabilities: model.Capabilities}
	}
	return models, nil
}
