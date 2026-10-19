package server

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
	"github.com/wklee610/clivoy/internal/knowledge"
)

// KnowledgeBase shares one index between model aliases and inspection routes.
// RefreshFailed must be safe to call concurrently with background refreshes.
type KnowledgeBase struct {
	Index            *knowledge.Index
	TopK             int
	MaxContextTokens int
	RefreshInterval  string
	RefreshFailed    func() bool
}

func cloneKnowledgeBases(source map[string]KnowledgeBase) (map[string]KnowledgeBase, error) {
	result := make(map[string]KnowledgeBase, len(source))
	for name, base := range source {
		if strings.TrimSpace(name) == "" || base.Index == nil || base.TopK < 1 || base.TopK > 50 || base.MaxContextTokens < 256 || base.MaxContextTokens > 32768 {
			return nil, fmt.Errorf("invalid knowledge base %q", name)
		}
		result[name] = base
	}
	return result, nil
}

func (s *Server) serveKnowledge(w http.ResponseWriter, r *http.Request) {
	method := http.MethodPost
	if r.URL.Path == "/v1/knowledge/status" {
		method = http.MethodGet
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		s.writeError(w, "openai", &engine.Error{Status: 405, Type: "invalid_request_error", Message: "Method not allowed"})
		return
	}
	if method == http.MethodGet {
		names := make([]string, 0, len(s.options.KnowledgeBases))
		for name := range s.options.KnowledgeBases {
			names = append(names, name)
		}
		sort.Strings(names)
		data := make([]any, 0, len(names))
		for _, name := range names {
			base := s.options.KnowledgeBases[name]
			stats := base.Index.Stats()
			failed := base.RefreshFailed != nil && base.RefreshFailed()
			data = append(data, map[string]any{
				"id": name, "documents": stats.Documents, "chunks": stats.Chunks,
				"skipped": stats.Skipped, "updated_at": stats.UpdatedAt,
				"ready": !stats.UpdatedAt.IsZero(), "refresh_failed": failed,
				"refresh_interval": base.RefreshInterval,
			})
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": data})
		return
	}
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		s.writeError(w, "openai", &engine.Error{Status: 415, Type: "invalid_request_error", Message: "Content-Type must be application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.options.MaxBodyBytes)
	var request struct {
		Knowledge string `json:"knowledge"`
		Query     string `json:"query"`
		Limit     int    `json:"limit,omitempty"`
	}
	if err := decode(r, &request); err != nil {
		s.writeError(w, "openai", err)
		return
	}
	if request.Knowledge == "" {
		request.Knowledge = "default"
	}
	base, ok := s.options.KnowledgeBases[request.Knowledge]
	if !ok {
		s.writeError(w, "openai", &engine.Error{Status: 404, Type: "not_found_error", Message: "Unknown knowledge base"})
		return
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || len(request.Query) > 8192 || request.Limit < 0 || request.Limit > base.TopK {
		s.writeError(w, "openai", engine.Invalid("query must contain 1-8192 bytes; limit must not exceed the configured top_k"))
		return
	}
	if request.Limit == 0 {
		request.Limit = base.TopK
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.options.RequestTimeout)
	defer cancel()
	if err := s.acquire(ctx); err != nil {
		s.writeError(w, "openai", err)
		return
	}
	defer func() { <-s.slots }()
	hits, err := base.Index.Search(ctx, request.Query, request.Limit, base.MaxContextTokens*3)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else {
			err = &engine.Error{Status: 503, Type: "retrieval_error", Message: "Knowledge search is unavailable"}
		}
		s.writeError(w, "openai", err)
		return
	}
	writeJSON(w, 200, map[string]any{"object": "list", "knowledge": request.Knowledge, "data": hits})
}
