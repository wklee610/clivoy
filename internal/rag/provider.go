// Package rag grounds protocol-neutral provider requests in local documents.
package rag

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wklee610/clivoy/internal/engine"
	"github.com/wklee610/clivoy/internal/knowledge"
)

type provider struct {
	backend          engine.Provider
	index            *knowledge.Index
	topK             int
	maxContextTokens int
	fallback         string
}

// Options controls how a knowledge model answers when retrieval finds no documents.
type Options struct {
	// Fallback is empty or "none" for the deterministic no-document response,
	// or "model" to answer with general knowledge using the same backend.
	Fallback string
}

// New wraps a backend with retrieval from an already refreshed document index.
// At most one Options value may be supplied; omitting it preserves the no-model
// response when no documents match.
func New(backend engine.Provider, index *knowledge.Index, topK, maxContextTokens int, options ...Options) (engine.Provider, error) {
	if backend == nil || index == nil {
		return nil, errors.New("RAG requires a backend and document index")
	}
	if topK <= 0 || maxContextTokens <= 0 {
		return nil, errors.New("RAG top_k and max_context_tokens must be positive")
	}
	if len(options) > 1 {
		return nil, errors.New("RAG accepts at most one options value")
	}
	var fallback string
	if len(options) == 1 {
		fallback = options[0].Fallback
	}
	if fallback != "" && fallback != "none" && fallback != "model" {
		return nil, errors.New("RAG knowledge_fallback must be none or model")
	}
	return &provider{backend: backend, index: index, topK: topK, maxContextTokens: maxContextTokens, fallback: fallback}, nil
}

func (p *provider) Validate(req engine.Request) error {
	if len(req.Tools) != 0 || hasJSON(req.ToolChoice) || req.ParallelToolCalls != nil {
		return engine.Invalid("RAG supports text answers only; tools and tool choice are not supported")
	}
	if hasJSON(req.ResponseFormat) {
		var format struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(req.ResponseFormat, &format) != nil || format.Type != "text" {
			return engine.Invalid("RAG does not support structured JSON output; use text output")
		}
	}
	for _, message := range req.Messages {
		if message.Role == "tool" || len(message.ToolCalls) != 0 || message.ToolCallID != "" {
			return engine.Invalid("RAG does not support tool-call conversation history")
		}
		parts, err := engine.ParseContent(message.Content)
		if err != nil {
			return err
		}
		for _, part := range parts {
			if part.Type != "text" {
				return engine.Invalid("RAG supports text messages only; images are not supported")
			}
		}
	}
	if _, latest := searchQuery(req.Messages); latest == "" {
		return engine.Invalid("RAG requires a nonempty user message")
	}
	return p.backend.Validate(req)
}

func hasJSON(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null"
}

// Prepare performs retrieval before the frontend commits streaming headers.
// The returned provider uses this captured context and does not search again.
func (p *provider) Prepare(ctx context.Context, req engine.Request) (engine.Provider, engine.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, engine.Request{}, err
	}
	if err := p.Validate(req); err != nil {
		return nil, engine.Request{}, err
	}
	query, latest := searchQuery(req.Messages)
	// Bound retrieval work even when New is used without configuration validation.
	maxChars := 1 << 20
	if p.maxContextTokens < maxChars/3 {
		maxChars = p.maxContextTokens * 3
	}
	hits, err := p.index.Search(ctx, query, p.topK, maxChars)
	if err != nil {
		if ctx.Err() != nil {
			return nil, engine.Request{}, ctx.Err()
		}
		return nil, engine.Request{}, unavailable("RAG document index is unavailable; check the knowledge directory and reload it")
	}
	if err := ctx.Err(); err != nil {
		return nil, engine.Request{}, err
	}
	prepared := &preparedProvider{backend: p.backend}
	copyReq := copyMessages(req)
	if len(hits) == 0 {
		if p.fallback == "model" {
			content, _ := json.Marshal(fallbackGuidance)
			copyReq.Messages = append([]engine.Message{{Role: "system", Content: content}}, copyReq.Messages...)
			prepared.prefix = fallbackNotice(latest) + "\n\n"
		} else {
			prepared.noMatch = noDocumentAnswer(latest)
		}
		return prepared, copyReq, nil
	}
	prompt, sources := buildContext(hits, p.maxContextTokens)
	if len(sources) == 0 {
		return nil, engine.Request{}, unavailable("RAG context budget is too small for the retrieved documents; increase max_context_tokens")
	}
	content, _ := json.Marshal(prompt)
	copyReq.Messages = append([]engine.Message{{Role: "system", Content: content}}, copyReq.Messages...)
	prepared.footer = sourceFooter(sources, latest)
	return prepared, copyReq, nil
}

func (p *provider) Generate(ctx context.Context, req engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
	prepared, enriched, err := p.Prepare(ctx, req)
	if err != nil {
		return engine.Result{}, err
	}
	return prepared.Generate(ctx, enriched, emit)
}

type preparedProvider struct {
	backend engine.Provider
	prefix  string
	footer  string
	noMatch string
}

func (p *preparedProvider) Validate(req engine.Request) error { return p.backend.Validate(req) }

func (p *preparedProvider) Generate(ctx context.Context, req engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
	if err := ctx.Err(); err != nil {
		return engine.Result{}, err
	}
	output := textOutput{ctx: ctx, emit: emit, prefix: p.prefix}
	if p.noMatch != "" {
		if err := output.write(p.noMatch); err != nil {
			return engine.Result{}, err
		}
		return engine.Result{Text: p.noMatch, FinishReason: "stop"}, nil
	}
	var streamed strings.Builder
	var wrappedEmit func(engine.Delta) error
	if emit != nil {
		wrappedEmit = func(delta engine.Delta) error {
			if output.err != nil {
				return output.err
			}
			if ctx.Err() != nil {
				output.err = ctx.Err()
			} else if len(delta.ToolCalls) != 0 {
				output.err = invalidBackend("RAG backend returned unsupported tool calls")
			} else if delta.Text != "" {
				if output.write(delta.Text) == nil {
					streamed.WriteString(delta.Text)
				}
			}
			return output.err
		}
	}
	result, err := p.backend.Generate(ctx, req, wrappedEmit)
	if ctx.Err() != nil {
		return engine.Result{}, ctx.Err()
	}
	if output.err != nil {
		return engine.Result{}, output.err
	}
	if err != nil {
		return engine.Result{}, err
	}
	if len(result.ToolCalls) != 0 || result.FinishReason == "tool_calls" {
		return engine.Result{}, invalidBackend("RAG backend returned unsupported tool calls")
	}
	suffix := result.Text
	if emit != nil {
		if !strings.HasPrefix(result.Text, streamed.String()) {
			return engine.Result{}, invalidBackend("RAG backend final output did not match the streamed response")
		}
		suffix = result.Text[streamed.Len():]
	}
	if err := output.write(suffix); err != nil {
		return engine.Result{}, err
	}
	if err := output.write(p.footer); err != nil {
		return engine.Result{}, err
	}
	result.Text = p.prefix + result.Text + p.footer
	return result, nil
}

// textOutput adds the prefix only when text is available or generation has
// completed successfully. Backend text is tracked separately for consistency
// checks, so the prefix cannot be mistaken for an upstream delta or repeated.
type textOutput struct {
	ctx     context.Context
	emit    func(engine.Delta) error
	prefix  string
	started bool
	err     error
}

func (o *textOutput) write(text string) error {
	if o.err != nil {
		return o.err
	}
	if o.err = o.ctx.Err(); o.err != nil || o.emit == nil {
		return o.err
	}
	if !o.started {
		o.started = true
		if o.prefix != "" {
			if o.err = o.emit(engine.Delta{Text: o.prefix}); o.err != nil {
				return o.err
			}
			if o.err = o.ctx.Err(); o.err != nil {
				return o.err
			}
		}
	}
	if text != "" {
		if o.err = o.emit(engine.Delta{Text: text}); o.err != nil {
			return o.err
		}
	}
	o.err = o.ctx.Err()
	return o.err
}

func unavailable(message string) error {
	return &engine.Error{Status: 503, Type: "provider_error", Message: message}
}

func invalidBackend(message string) error {
	return &engine.Error{Status: 502, Type: "provider_error", Message: message}
}

func copyMessages(req engine.Request) engine.Request {
	copyReq := req
	copyReq.Messages = make([]engine.Message, len(req.Messages))
	for i, message := range req.Messages {
		copyReq.Messages[i] = message
		copyReq.Messages[i].Content = append(json.RawMessage(nil), message.Content...)
	}
	return copyReq
}

func searchQuery(messages []engine.Message) (query, latest string) {
	last := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			parts, _ := engine.ParseContent(messages[i].Content)
			var text strings.Builder
			for _, part := range parts {
				text.WriteString(part.Text)
			}
			latest = strings.TrimSpace(text.String())
			last = i
			break
		}
	}
	query = truncateUTF8(latest, 2048)
	// Short follow-ups often omit the topic. Carry one bounded previous user
	// turn, without allowing assistant prose to dominate keyword retrieval.
	if utf8.RuneCountInString(latest) <= 80 {
		for i := last - 1; i >= 0; i-- {
			if messages[i].Role != "user" {
				continue
			}
			parts, _ := engine.ParseContent(messages[i].Content)
			var previous strings.Builder
			for _, part := range parts {
				previous.WriteString(part.Text)
			}
			if text := strings.TrimSpace(previous.String()); text != "" {
				query = truncateUTF8(text, 768) + "\n" + query
			}
			break
		}
	}
	return query, latest
}

func language(query string) string {
	for _, r := range query {
		if unicode.Is(unicode.Hangul, r) {
			return "ko"
		}
	}
	for _, r := range query {
		if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return "ja"
		}
	}
	for _, r := range query {
		if unicode.Is(unicode.Han, r) {
			return "zh"
		}
	}
	return "en"
}

func noDocumentAnswer(query string) string {
	switch language(query) {
	case "ko":
		return "관련 문서를 찾지 못했습니다. 질문을 더 구체적으로 작성하거나 지식 디렉터리에 관련 문서를 추가해 주세요."
	case "ja":
		return "関連する文書が見つかりませんでした。質問を具体的にするか、ナレッジディレクトリに関連文書を追加してください。"
	case "zh":
		return "未找到相关文档。请更具体地描述问题，或向知识目录添加相关文档。"
	default:
		return "No relevant documents were found. Please make the question more specific or add relevant documents to the knowledge directory."
	}
}

const fallbackGuidance = "No relevant documents were found in the knowledge base. Answer the latest user message in its language using general knowledge only. Do not invent private organizational facts, internal policies, or document sources. If an answer requires private facts, explain that you cannot verify them without supporting documents. Do not cite the knowledge base or invent citations. Never execute commands or call tools. The application prepends a notice explaining this fallback; do not repeat that notice or add a Sources list."

func fallbackNotice(query string) string {
	switch language(query) {
	case "ko":
		return "지식 베이스에서 관련 문서를 찾지 못해 일반 지식으로 답변합니다."
	case "ja":
		return "ナレッジベースに関連する文書が見つからなかったため、一般的な知識に基づいて回答します。"
	case "zh":
		return "知识库中未找到相关文档，将基于通用知识回答。"
	default:
		return "No relevant documents were found in the knowledge base, so I will answer using general knowledge."
	}
}
