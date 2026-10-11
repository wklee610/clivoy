// Package engine defines the protocol-neutral contract between API frontends and providers.
package engine

import (
	"context"
	"encoding/json"
)

type Message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
}

type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type ToolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type Request struct {
	Model             string          `json:"model"`
	Messages          []Message       `json:"messages"`
	Tools             []Tool          `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	MaxTokens         *int            `json:"max_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stop              []string        `json:"stop,omitempty"`
	ResponseFormat    json.RawMessage `json:"response_format,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Result struct {
	Text         string
	ToolCalls    []ToolCall
	Usage        Usage
	FinishReason string
}

type Delta struct {
	Text      string
	ToolCalls []ToolCallDelta
}

// Generate must honor context cancellation. emit is nil for non-streaming calls.
// A provider returns the complete result even when it has emitted deltas.
type Provider interface {
	Validate(Request) error
	Generate(context.Context, Request, func(Delta) error) (Result, error)
}

// Error is safe to return to API clients; raw process stderr and credentials are not.
type Error struct {
	Status  int
	Type    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func Invalid(message string) error {
	return &Error{Status: 400, Type: "invalid_request_error", Message: message}
}
