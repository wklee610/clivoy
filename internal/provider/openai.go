package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

type openAIProvider struct {
	endpoint, apiKey string
	client           *http.Client
}

// NewOpenAI expects an API prefix, such as http://localhost:8000/v1.
// It works with vLLM, Ollama, LiteLLM, and other Chat Completions servers.
func NewOpenAI(baseURL, apiKey string) (engine.Provider, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("OpenAI base URL must be an absolute http(s) API prefix without credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	u.RawPath = ""
	return &openAIProvider{endpoint: u.String(), apiKey: apiKey, client: &http.Client{
		// A backend redirect must never forward credentials or replay prompts.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (p *openAIProvider) Validate(req engine.Request) error {
	if err := validateMessages(req, true); err != nil {
		return err
	}
	if req.Model == "" {
		return engine.Invalid("an upstream model is required")
	}
	for _, tool := range req.Tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			return engine.Invalid("only named function tools are supported")
		}
	}
	for _, message := range req.Messages {
		for _, call := range message.ToolCalls {
			if call.Type != "function" || call.Function.Name == "" {
				return engine.Invalid("only named function tool calls are supported")
			}
		}
	}
	return nil
}

func (p *openAIProvider) Generate(ctx context.Context, req engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
	if err := p.Validate(req); err != nil {
		return engine.Result{}, err
	}
	payload := struct {
		engine.Request
		Stream        bool `json:"stream"`
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options,omitempty"`
	}{Request: req, Stream: emit != nil}
	if emit != nil {
		payload.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{true}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return engine.Result{}, engine.Invalid("request could not be encoded")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		return engine.Result{}, upstreamError("could not prepare upstream request")
	}
	r.Header.Set("Content-Type", "application/json")
	if emit != nil {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	if p.apiKey != "" {
		r.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return engine.Result{}, ctx.Err()
		}
		return engine.Result{}, upstreamError("could not connect to upstream provider")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := http.StatusBadGateway
		if resp.StatusCode == http.StatusTooManyRequests {
			status = http.StatusTooManyRequests
		}
		return engine.Result{}, &engine.Error{Status: status, Type: "provider_error", Message: fmt.Sprintf("upstream provider returned HTTP %d", resp.StatusCode)}
	}
	if emit != nil {
		return readOpenAIStream(ctx, resp.Body, emit)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxOutputBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return engine.Result{}, ctx.Err()
		}
		return engine.Result{}, upstreamError("could not read upstream response")
	}
	if len(data) > maxOutputBytes {
		return engine.Result{}, upstreamError("upstream response exceeded the size limit")
	}
	var result openAIResponse
	if json.Unmarshal(data, &result) != nil || hasError(result.Error) || len(result.Choices) != 1 {
		return engine.Result{}, upstreamError("upstream returned an invalid completion")
	}
	choice := result.Choices[0]
	if choice.FinishReason == "" {
		return engine.Result{}, upstreamError("upstream response did not contain a finish reason")
	}
	content, err := textContent(choice.Message.Content)
	if err != nil {
		return engine.Result{}, upstreamError("upstream returned unsupported content")
	}
	if len(choice.Message.ToolCalls) > maxToolCalls {
		return engine.Result{}, upstreamError("upstream returned too many tool calls")
	}
	for _, call := range choice.Message.ToolCalls {
		if call.ID == "" || call.Type != "function" || call.Function.Name == "" {
			return engine.Result{}, upstreamError("upstream returned an invalid tool call")
		}
	}
	return engine.Result{Text: content, ToolCalls: choice.Message.ToolCalls, Usage: result.Usage.value(), FinishReason: choice.FinishReason}, nil
}

func hasError(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (u openAIUsage) engine() engine.Usage {
	return engine.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
}

type openAIResponse struct {
	Error   json.RawMessage `json:"error"`
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   json.RawMessage   `json:"content"`
			ToolCalls []engine.ToolCall `json:"tool_calls"`
		} `json:"message"`
		Delta struct {
			Content   *string                `json:"content"`
			ToolCalls []engine.ToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

// A nil usage block is valid on servers that do not report usage.
func (u *openAIUsage) value() engine.Usage {
	if u == nil {
		return engine.Usage{}
	}
	return u.engine()
}

type toolAccumulator struct{ id, kind, name, arguments strings.Builder }

func (t *toolAccumulator) append(d engine.ToolCallDelta) {
	t.id.WriteString(d.ID)
	t.kind.WriteString(d.Type)
	t.name.WriteString(d.Function.Name)
	t.arguments.WriteString(d.Function.Arguments)
}
func (t *toolAccumulator) call() engine.ToolCall {
	return engine.ToolCall{ID: t.id.String(), Type: t.kind.String(), Function: engine.FunctionCall{Name: t.name.String(), Arguments: t.arguments.String()}}
}

func readOpenAIStream(ctx context.Context, body io.Reader, emit func(engine.Delta) error) (engine.Result, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, maxWireBytes+1))
	scanner.Buffer(make([]byte, 4096), maxOutputBytes)
	var result engine.Result
	var text strings.Builder
	var event strings.Builder
	wireBytes := 0
	outputBytes := 0
	done := false
	finished := false
	calls := map[int]*toolAccumulator{}
	consume := func() error {
		data := strings.TrimSpace(event.String())
		event.Reset()
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			done = true
			return nil
		}
		var chunk openAIResponse
		if json.Unmarshal([]byte(data), &chunk) != nil || hasError(chunk.Error) {
			return upstreamError("upstream returned an invalid streaming event")
		}
		if chunk.Usage != nil {
			result.Usage = chunk.Usage.value()
		}
		if len(chunk.Choices) > 1 {
			return upstreamError("upstream returned multiple choices")
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return upstreamError("upstream returned an unsupported choice index")
			}
			delta := engine.Delta{ToolCalls: choice.Delta.ToolCalls}
			if choice.Delta.Content != nil {
				delta.Text = *choice.Delta.Content
			}
			if finished && (delta.Text != "" || len(delta.ToolCalls) > 0) {
				return upstreamError("upstream sent content after the finish reason")
			}
			outputBytes += len(delta.Text)
			for _, part := range delta.ToolCalls {
				if part.Index < 0 || part.Index >= maxToolCalls {
					return upstreamError("upstream tool-call index exceeded the limit")
				}
				call := calls[part.Index]
				if call == nil {
					call = &toolAccumulator{}
					calls[part.Index] = call
				}
				outputBytes += len(part.ID) + len(part.Type) + len(part.Function.Name) + len(part.Function.Arguments)
				call.append(part)
			}
			if outputBytes > maxOutputBytes {
				return upstreamError("upstream response exceeded the size limit")
			}
			text.WriteString(delta.Text)
			result.Text = text.String()
			if delta.Text != "" || len(delta.ToolCalls) > 0 {
				if err := emit(delta); err != nil {
					return err
				}
			}
			if choice.FinishReason != "" {
				result.FinishReason = choice.FinishReason
				finished = true
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		wireBytes += len(scanner.Bytes()) + 1
		if wireBytes > maxWireBytes {
			return engine.Result{}, upstreamError("upstream stream exceeded the size limit")
		}
		if line == "" {
			if err := consume(); err != nil {
				return engine.Result{}, err
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			if event.Len()+len(data)+1 > maxOutputBytes {
				return engine.Result{}, upstreamError("upstream streaming event exceeded the size limit")
			}
			event.WriteString(data)
			event.WriteByte('\n')
		}
	}
	if ctx.Err() != nil {
		return engine.Result{}, ctx.Err()
	}
	if scanner.Err() != nil {
		return engine.Result{}, upstreamError("could not read upstream stream")
	}
	if !done || !finished {
		return engine.Result{}, upstreamError("upstream stream ended before completion")
	}
	for i := 0; i < len(calls); i++ {
		accumulator, ok := calls[i]
		if !ok {
			return engine.Result{}, upstreamError("upstream returned an invalid tool call")
		}
		call := accumulator.call()
		if call.ID == "" || call.Type != "function" || call.Function.Name == "" {
			return engine.Result{}, upstreamError("upstream returned an invalid tool call")
		}
		result.ToolCalls = append(result.ToolCalls, call)
	}
	return result, nil
}
