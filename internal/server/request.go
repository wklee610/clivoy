package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

type parsedRequest struct {
	Request      engine.Request
	Protocol     string
	Stream       bool
	IncludeUsage bool
	Metadata     map[string]string
	Instructions string
	TextFormat   json.RawMessage
}

type chatRequest struct {
	Model               string           `json:"model"`
	Messages            []engine.Message `json:"messages"`
	Tools               []engine.Tool    `json:"tools,omitempty"`
	ToolChoice          json.RawMessage  `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool            `json:"parallel_tool_calls,omitempty"`
	MaxTokens           *int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64         `json:"temperature,omitempty"`
	TopP                *float64         `json:"top_p,omitempty"`
	Stop                json.RawMessage  `json:"stop,omitempty"`
	ResponseFormat      json.RawMessage  `json:"response_format,omitempty"`
	Stream              bool             `json:"stream,omitempty"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	N        *int              `json:"n,omitempty"`
	User     string            `json:"user,omitempty"`
	Store    *bool             `json:"store,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func parseRequest(r *http.Request) (parsedRequest, error) {
	switch r.URL.Path {
	case "/v1/messages":
		return parseAnthropic(r)
	case "/v1/responses":
		return parseResponses(r)
	}
	var c chatRequest
	if err := decode(r, &c); err != nil {
		return parsedRequest{}, err
	}
	if c.N != nil && *c.N != 1 {
		return parsedRequest{}, engine.Invalid("Only n=1 is supported")
	}
	if c.Store != nil && *c.Store {
		return parsedRequest{}, engine.Invalid("Response storage is not supported; use store=false")
	}
	if c.MaxTokens != nil && c.MaxCompletionTokens != nil {
		return parsedRequest{}, engine.Invalid("Set only one of max_tokens or max_completion_tokens")
	}
	if c.MaxCompletionTokens != nil {
		c.MaxTokens = c.MaxCompletionTokens
	}
	stop, err := parseStop(c.Stop)
	if err != nil {
		return parsedRequest{}, err
	}
	p := parsedRequest{Protocol: "chat", Stream: c.Stream, Request: engine.Request{Model: c.Model, Messages: c.Messages, Tools: c.Tools, ToolChoice: c.ToolChoice, ParallelToolCalls: c.ParallelToolCalls, MaxTokens: c.MaxTokens, Temperature: c.Temperature, TopP: c.TopP, Stop: stop, ResponseFormat: c.ResponseFormat}}
	if c.StreamOptions != nil {
		p.IncludeUsage = c.StreamOptions.IncludeUsage
	}
	return p, nil
}

func parseStop(raw json.RawMessage) ([]string, error) {
	if absent(raw) {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}, nil
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return nil, engine.Invalid("stop must be a string or a list of strings")
	}
	return list, nil
}

func absent(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// textContent rejects unsupported modalities instead of silently dropping them.
func textContent(raw json.RawMessage, allowed ...string) (string, error) {
	if absent(raw) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []struct {
		Type        string          `json:"type"`
		Text        string          `json:"text"`
		Annotations json.RawMessage `json:"annotations,omitempty"`
		Logprobs    json.RawMessage `json:"logprobs,omitempty"`
		Citations   json.RawMessage `json:"citations,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&blocks); err != nil {
		return "", engine.Invalid("Content must be text or an array of supported text blocks")
	}
	var b strings.Builder
	for _, block := range blocks {
		ok := false
		for _, t := range allowed {
			if block.Type == t {
				ok = true
			}
		}
		if !ok {
			return "", engine.Invalid("Unsupported content type: " + block.Type + "; only text content is supported")
		}
		b.WriteString(block.Text)
	}
	return b.String(), nil
}

func validateRequest(r engine.Request) error {
	if strings.TrimSpace(r.Model) == "" {
		return engine.Invalid("model is required")
	}
	if len(r.Messages) == 0 {
		return engine.Invalid("At least one message is required")
	}
	if len(r.Messages) > 4096 {
		return engine.Invalid("Too many messages")
	}
	for _, m := range r.Messages {
		switch m.Role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return engine.Invalid("Unsupported message role: " + m.Role)
		}
		if absent(m.Content) && len(m.ToolCalls) == 0 {
			return engine.Invalid("Message content is required")
		}
		if m.Role == "tool" && m.ToolCallID == "" {
			return engine.Invalid("tool messages require tool_call_id")
		}
		if len(m.ToolCalls) > 0 && m.Role != "assistant" {
			return engine.Invalid("tool_calls are only valid on assistant messages")
		}
		for _, tc := range m.ToolCalls {
			if tc.Type != "function" || tc.ID == "" || tc.Function.Name == "" || !json.Valid([]byte(tc.Function.Arguments)) {
				return engine.Invalid("Invalid function tool call in message history")
			}
		}
	}
	if err := engine.ValidateMessageContent(r.Messages, true); err != nil {
		return err
	}
	if r.MaxTokens != nil && *r.MaxTokens < 1 {
		return engine.Invalid("max_tokens must be positive")
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 2) {
		return engine.Invalid("temperature must be between 0 and 2")
	}
	if r.TopP != nil && (*r.TopP < 0 || *r.TopP > 1) {
		return engine.Invalid("top_p must be between 0 and 1")
	}
	if len(r.Stop) > 4 {
		return engine.Invalid("At most four stop sequences are supported")
	}
	for _, stop := range r.Stop {
		if stop == "" {
			return engine.Invalid("Stop sequences must not be empty")
		}
	}
	if len(r.Tools) > 128 {
		return engine.Invalid("At most 128 tools are supported")
	}
	names := map[string]bool{}
	for _, t := range r.Tools {
		if t.Type != "function" || t.Function.Name == "" {
			return engine.Invalid("Only named function tools are supported")
		}
		if names[t.Function.Name] {
			return engine.Invalid("Duplicate tool name")
		}
		names[t.Function.Name] = true
		if !absent(t.Function.Parameters) {
			var schema map[string]any
			if json.Unmarshal(t.Function.Parameters, &schema) != nil || schema == nil {
				return engine.Invalid("Function parameters must be a JSON object")
			}
		}
	}
	if !absent(r.ToolChoice) {
		var choice string
		if json.Unmarshal(r.ToolChoice, &choice) == nil {
			if choice != "auto" && choice != "none" && choice != "required" {
				return engine.Invalid("Unsupported tool_choice")
			}
			if choice != "none" && len(r.Tools) == 0 {
				return engine.Invalid("tool_choice requires tools")
			}
		} else {
			var tc struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			d := json.NewDecoder(bytes.NewReader(r.ToolChoice))
			d.DisallowUnknownFields()
			if d.Decode(&tc) != nil || tc.Type != "function" || !names[tc.Function.Name] {
				return engine.Invalid("tool_choice must name a configured function")
			}
		}
	}
	if !absent(r.ResponseFormat) {
		var format struct {
			Type       string `json:"type"`
			JSONSchema *struct {
				Name        string          `json:"name"`
				Description string          `json:"description,omitempty"`
				Schema      json.RawMessage `json:"schema"`
				Strict      *bool           `json:"strict,omitempty"`
			} `json:"json_schema,omitempty"`
		}
		if err := strictRaw(r.ResponseFormat, &format); err != nil {
			return err
		}
		switch format.Type {
		case "text", "json_object":
			if format.JSONSchema != nil {
				return engine.Invalid("json_schema requires response format type json_schema")
			}
		case "json_schema":
			if format.JSONSchema == nil || format.JSONSchema.Name == "" {
				return engine.Invalid("json_schema requires a name and schema")
			}
			var schema map[string]any
			if json.Unmarshal(format.JSONSchema.Schema, &schema) != nil || schema == nil {
				return engine.Invalid("json_schema.schema must be an object")
			}
		default:
			return engine.Invalid("Unsupported response_format type")
		}
	}
	return nil
}

func normalizeRequest(r *engine.Request) {
	if absent(r.ToolChoice) {
		r.ToolChoice = nil
	}
	if absent(r.ResponseFormat) {
		r.ResponseFormat = nil
	}
	for i := range r.Tools {
		if absent(r.Tools[i].Function.Parameters) {
			r.Tools[i].Function.Parameters = rawObject(map[string]any{"type": "object", "properties": map[string]any{}})
		}
	}
}

func strictRaw(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return engine.Invalid(fmt.Sprintf("Invalid input: %s", err))
	}
	return nil
}
