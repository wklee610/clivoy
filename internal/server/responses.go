package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

type responsesRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       string          `json:"instructions,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"top_p,omitempty"`
	Stream             bool            `json:"stream,omitempty"`
	Store              *bool           `json:"store,omitempty"`
	Background         bool            `json:"background,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Tools              []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
		Strict      *bool           `json:"strict,omitempty"`
	} `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Text              *struct {
		Format json.RawMessage `json:"format,omitempty"`
	} `json:"text,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	User     string            `json:"user,omitempty"`
}

type responseInput struct {
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	Status    string          `json:"status,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

func parseResponses(r *http.Request) (parsedRequest, error) {
	var a responsesRequest
	if err := decode(r, &a); err != nil {
		return parsedRequest{}, err
	}
	if a.Background || a.PreviousResponseID != "" || (a.Store != nil && *a.Store) {
		return parsedRequest{}, engine.Invalid("Responses are stateless: background, previous_response_id and store=true are not supported; send conversation history in input")
	}
	e := engine.Request{Model: a.Model, MaxTokens: a.MaxOutputTokens, Temperature: a.Temperature, TopP: a.TopP, ParallelToolCalls: a.ParallelToolCalls}
	if a.Instructions != "" {
		e.Messages = append(e.Messages, engine.Message{Role: "system", Content: rawString(a.Instructions)})
	}
	if absent(a.Input) {
		return parsedRequest{}, engine.Invalid("input is required")
	}
	var text string
	if json.Unmarshal(a.Input, &text) == nil {
		e.Messages = append(e.Messages, engine.Message{Role: "user", Content: rawString(text)})
	} else {
		var items []responseInput
		if err := strictRaw(a.Input, &items); err != nil {
			return parsedRequest{}, err
		}
		if len(items) == 0 {
			return parsedRequest{}, engine.Invalid("input must not be empty")
		}
		for _, item := range items {
			switch item.Type {
			case "", "message":
				if absent(item.Content) {
					return parsedRequest{}, engine.Invalid("Input message content is required")
				}
				if item.Role != "user" && item.Role != "assistant" && item.Role != "system" && item.Role != "developer" {
					return parsedRequest{}, engine.Invalid("Invalid Responses message role")
				}
				content, err := responsesContent(item.Content)
				if err != nil {
					return parsedRequest{}, err
				}
				e.Messages = append(e.Messages, engine.Message{Role: item.Role, Content: content})
			case "function_call":
				tc := engine.ToolCall{ID: item.CallID, Type: "function", Function: engine.FunctionCall{Name: item.Name, Arguments: item.Arguments}}
				// Consecutive calls belong to one assistant turn for parallel tool results.
				if n := len(e.Messages); n > 0 && e.Messages[n-1].Role == "assistant" {
					e.Messages[n-1].ToolCalls = append(e.Messages[n-1].ToolCalls, tc)
				} else {
					e.Messages = append(e.Messages, engine.Message{Role: "assistant", Content: rawString(""), ToolCalls: []engine.ToolCall{tc}})
				}
			case "function_call_output":
				if absent(item.Output) {
					return parsedRequest{}, engine.Invalid("function_call_output requires output")
				}
				text, err := textContent(item.Output, "input_text")
				if err != nil {
					return parsedRequest{}, err
				}
				e.Messages = append(e.Messages, engine.Message{Role: "tool", ToolCallID: item.CallID, Content: rawString(text)})
			default:
				return parsedRequest{}, engine.Invalid("Unsupported Responses input type: " + item.Type)
			}
		}
	}
	for _, t := range a.Tools {
		e.Tools = append(e.Tools, engine.Tool{Type: t.Type, Function: engine.Function{Name: t.Name, Description: t.Description, Parameters: t.Parameters, Strict: t.Strict}})
	}
	if !absent(a.ToolChoice) {
		var choice string
		if json.Unmarshal(a.ToolChoice, &choice) == nil {
			e.ToolChoice = a.ToolChoice
		} else {
			var named struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if err := strictRaw(a.ToolChoice, &named); err != nil {
				return parsedRequest{}, err
			}
			if named.Type != "function" {
				return parsedRequest{}, engine.Invalid("Only function tool choice is supported")
			}
			e.ToolChoice, _ = json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": named.Name}})
		}
	}
	if a.Text != nil && !absent(a.Text.Format) {
		var f struct {
			Type        string          `json:"type"`
			Name        string          `json:"name,omitempty"`
			Description string          `json:"description,omitempty"`
			Schema      json.RawMessage `json:"schema,omitempty"`
			Strict      *bool           `json:"strict,omitempty"`
		}
		if err := strictRaw(a.Text.Format, &f); err != nil {
			return parsedRequest{}, err
		}
		switch f.Type {
		case "text":
		case "json_object":
			e.ResponseFormat = rawObject(map[string]any{"type": "json_object"})
		case "json_schema":
			e.ResponseFormat = rawObject(map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": f.Name, "description": f.Description, "schema": f.Schema, "strict": f.Strict}})
		default:
			return parsedRequest{}, engine.Invalid("Unsupported text.format")
		}
	}
	p := parsedRequest{Protocol: "responses", Request: e, Stream: a.Stream, Metadata: a.Metadata, Instructions: a.Instructions}
	if a.Text != nil {
		p.TextFormat = a.Text.Format
	}
	return p, nil
}

func rawObject(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (p parsedRequest) idPrefix() string {
	switch p.Protocol {
	case "anthropic":
		return "msg"
	case "responses":
		return "resp"
	default:
		return "chatcmpl"
	}
}

func (p parsedRequest) response(r engine.Result, id, status string) map[string]any {
	switch p.Protocol {
	case "anthropic":
		return map[string]any{"id": id, "type": "message", "role": "assistant", "model": p.Request.Model, "content": anthropicContent(r), "stop_reason": anthropicStop(r), "stop_sequence": nil, "usage": r.Usage}
	case "responses":
		output := make([]any, 0, 1+len(r.ToolCalls))
		var incomplete any
		if r.FinishReason == "length" {
			status = "incomplete"
			incomplete = map[string]string{"reason": "max_output_tokens"}
		}
		if status != "in_progress" {
			if r.Text != "" || len(r.ToolCalls) == 0 {
				output = append(output, responseMessage(id, r.Text, status))
			}
			for i, t := range r.ToolCalls {
				output = append(output, responseTool(id, i, t))
			}
		}
		metadata := p.Metadata
		if metadata == nil {
			metadata = map[string]string{}
		}
		tools := make([]any, 0, len(p.Request.Tools))
		for _, t := range p.Request.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": t.Function.Name, "description": t.Function.Description, "parameters": t.Function.Parameters, "strict": t.Function.Strict})
		}
		choice := any("auto")
		if !absent(p.Request.ToolChoice) {
			var name string
			if json.Unmarshal(p.Request.ToolChoice, &name) == nil {
				choice = name
			} else {
				var t struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				}
				_ = json.Unmarshal(p.Request.ToolChoice, &t)
				choice = map[string]any{"type": "function", "name": t.Function.Name}
			}
		}
		parallel := true
		if p.Request.ParallelToolCalls != nil {
			parallel = *p.Request.ParallelToolCalls
		}
		format := p.TextFormat
		if absent(format) {
			format = rawObject(map[string]string{"type": "text"})
		}
		var instructions any
		if p.Instructions != "" {
			instructions = p.Instructions
		}
		return map[string]any{
			"id": id, "object": "response", "created_at": time.Now().Unix(),
			"status": status, "error": nil, "incomplete_details": incomplete,
			"model": p.Request.Model, "output": output,
			"usage": map[string]any{
				"input_tokens": r.Usage.InputTokens, "output_tokens": r.Usage.OutputTokens,
				"total_tokens":          r.Usage.InputTokens + r.Usage.OutputTokens,
				"input_tokens_details":  map[string]int{"cached_tokens": 0, "cache_write_tokens": 0},
				"output_tokens_details": map[string]int{"reasoning_tokens": 0},
			},
			"metadata": metadata, "tools": tools, "tool_choice": choice, "parallel_tool_calls": parallel,
			"store": false, "background": false, "previous_response_id": nil, "instructions": instructions,
			"max_output_tokens": p.Request.MaxTokens, "temperature": p.Request.Temperature, "top_p": p.Request.TopP,
			"text": map[string]any{"format": format}, "truncation": "disabled", "reasoning": nil,
		}
	default:
		message := map[string]any{"role": "assistant", "content": r.Text, "refusal": nil}
		if len(r.ToolCalls) > 0 {
			message["tool_calls"] = r.ToolCalls
			if r.Text == "" {
				message["content"] = nil
			}
		}
		finish := r.FinishReason
		if finish == "" {
			finish = "stop"
		}
		if len(r.ToolCalls) > 0 {
			finish = "tool_calls"
		}
		return map[string]any{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": p.Request.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish, "logprobs": nil}}, "usage": chatUsage(r.Usage)}
	}
}

func responseMessage(id, text, status string) map[string]any {
	return map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "status": status, "content": []any{responseText(text)}}
}
func responseText(text string) map[string]any {
	return map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}
}
func responseTool(id string, index int, t engine.ToolCall) map[string]any {
	return map[string]any{"id": fmt.Sprintf("fc_%s_%d", id, index), "type": "function_call", "call_id": t.ID, "name": t.Function.Name, "arguments": t.Function.Arguments, "status": "completed"}
}
func chatUsage(u engine.Usage) map[string]int {
	return map[string]int{"prompt_tokens": u.InputTokens, "completion_tokens": u.OutputTokens, "total_tokens": u.InputTokens + u.OutputTokens}
}
