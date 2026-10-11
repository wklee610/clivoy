package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

type anthropicRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	System        json.RawMessage `json:"system,omitempty"`
	MaxTokens     *int            `json:"max_tokens"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Tools         []struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools,omitempty"`
	ToolChoice *struct {
		Type                   string `json:"type"`
		Name                   string `json:"name,omitempty"`
		DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
	} `json:"tool_choice,omitempty"`
	Metadata *struct {
		UserID string `json:"user_id,omitempty"`
	} `json:"metadata,omitempty"`
}

type anthropicBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ToolUseID string                `json:"tool_use_id,omitempty"`
	Content   json.RawMessage       `json:"content,omitempty"`
	IsError   bool                  `json:"is_error,omitempty"`
	Citations json.RawMessage       `json:"citations,omitempty"`
	Source    *anthropicImageSource `json:"source,omitempty"`
}

// Decode each variant separately so fields from another block type cannot be
// silently discarded, including fields explicitly set to null or false.
func (b *anthropicBlock) UnmarshalJSON(raw []byte) error {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return engine.Invalid("Invalid Anthropic content block")
	}
	*b = anthropicBlock{Type: header.Type}
	switch header.Type {
	case "text":
		var block struct {
			Type      string          `json:"type"`
			Text      *string         `json:"text"`
			Citations json.RawMessage `json:"citations,omitempty"`
		}
		if err := strictRaw(raw, &block); err != nil {
			return err
		}
		if block.Text == nil {
			return engine.Invalid("Text blocks require text")
		}
		b.Text, b.Citations = *block.Text, block.Citations
	case "image":
		var block struct {
			Type   string                `json:"type"`
			Source *anthropicImageSource `json:"source"`
		}
		if err := strictRaw(raw, &block); err != nil {
			return err
		}
		if block.Source == nil {
			return engine.Invalid("Image blocks require a source")
		}
		b.Source = block.Source
	case "tool_use":
		var block struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := strictRaw(raw, &block); err != nil {
			return err
		}
		b.ID, b.Name, b.Input = block.ID, block.Name, block.Input
	case "tool_result":
		var block struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content,omitempty"`
			IsError   bool            `json:"is_error,omitempty"`
		}
		if err := strictRaw(raw, &block); err != nil {
			return err
		}
		b.ToolUseID, b.Content, b.IsError = block.ToolUseID, block.Content, block.IsError
	default:
		return engine.Invalid("Unsupported Anthropic content type: " + header.Type)
	}
	return nil
}

func anthropicTextContent(raw json.RawMessage) (string, error) {
	if absent(raw) {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []anthropicBlock
	if err := strictRaw(raw, &blocks); err != nil {
		return "", err
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			return "", engine.Invalid("Only text content is supported in system messages and tool results")
		}
		result.WriteString(block.Text)
	}
	return result.String(), nil
}

func parseAnthropic(r *http.Request) (parsedRequest, error) {
	var a anthropicRequest
	if err := decode(r, &a); err != nil {
		return parsedRequest{}, err
	}
	if a.MaxTokens == nil {
		return parsedRequest{}, engine.Invalid("max_tokens is required")
	}
	if a.Temperature != nil && *a.Temperature > 1 {
		return parsedRequest{}, engine.Invalid("Anthropic temperature must be between 0 and 1")
	}
	e := engine.Request{Model: a.Model, MaxTokens: a.MaxTokens, Temperature: a.Temperature, TopP: a.TopP, Stop: a.StopSequences}
	if !absent(a.System) {
		text, err := anthropicTextContent(a.System)
		if err != nil {
			return parsedRequest{}, err
		}
		e.Messages = append(e.Messages, engine.Message{Role: "system", Content: rawString(text)})
	}
	for _, m := range a.Messages {
		if absent(m.Content) {
			return parsedRequest{}, engine.Invalid("Message content is required")
		}
		if m.Role != "user" && m.Role != "assistant" {
			return parsedRequest{}, engine.Invalid("Anthropic message role must be user or assistant")
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			e.Messages = append(e.Messages, engine.Message{Role: m.Role, Content: rawString(text)})
			continue
		}
		var blocks []anthropicBlock
		if err := strictRaw(m.Content, &blocks); err != nil {
			return parsedRequest{}, err
		}
		if len(blocks) == 0 {
			return parsedRequest{}, engine.Invalid("Message content blocks must not be empty")
		}
		current := engine.Message{Role: m.Role}
		var parts []engine.ContentPart
		flush := func() {
			if len(parts) > 0 || len(current.ToolCalls) > 0 {
				current.Content = encodeContentParts(parts)
				e.Messages = append(e.Messages, current)
				current = engine.Message{Role: m.Role}
				parts = nil
			}
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, engine.ContentPart{Type: "text", Text: b.Text})
			case "image":
				if m.Role != "user" {
					return parsedRequest{}, engine.Invalid("Images are supported only in user messages")
				}
				part, err := b.Source.imagePart()
				if err != nil {
					return parsedRequest{}, err
				}
				parts = append(parts, part)
			case "tool_use":
				if m.Role != "assistant" {
					return parsedRequest{}, engine.Invalid("tool_use requires an assistant message")
				}
				var obj map[string]any
				if b.ID == "" || b.Name == "" || json.Unmarshal(b.Input, &obj) != nil || obj == nil {
					return parsedRequest{}, engine.Invalid("Invalid tool_use block")
				}
				current.ToolCalls = append(current.ToolCalls, engine.ToolCall{ID: b.ID, Type: "function", Function: engine.FunctionCall{Name: b.Name, Arguments: string(b.Input)}})
			case "tool_result":
				if m.Role != "user" || b.ToolUseID == "" {
					return parsedRequest{}, engine.Invalid("tool_result requires user role and tool_use_id")
				}
				flush()
				output, err := anthropicTextContent(b.Content)
				if err != nil {
					return parsedRequest{}, err
				}
				if b.IsError {
					output = "Tool error: " + output
				}
				e.Messages = append(e.Messages, engine.Message{Role: "tool", ToolCallID: b.ToolUseID, Content: rawString(output)})
			default:
				return parsedRequest{}, engine.Invalid("Unsupported Anthropic content type: " + b.Type)
			}
		}
		flush()
	}
	if len(a.Messages) == 0 {
		return parsedRequest{}, engine.Invalid("At least one message is required")
	}
	for _, t := range a.Tools {
		e.Tools = append(e.Tools, engine.Tool{Type: "function", Function: engine.Function{Name: t.Name, Description: t.Description, Parameters: t.InputSchema}})
	}
	if c := a.ToolChoice; c != nil {
		switch c.Type {
		case "auto", "none":
			e.ToolChoice = rawString(c.Type)
		case "any":
			e.ToolChoice = rawString("required")
		case "tool":
			e.ToolChoice, _ = json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": c.Name}})
		default:
			return parsedRequest{}, engine.Invalid("Unsupported Anthropic tool_choice")
		}
		if c.DisableParallelToolUse != nil {
			v := !*c.DisableParallelToolUse
			e.ParallelToolCalls = &v
		}
	}
	return parsedRequest{Protocol: "anthropic", Request: e, Stream: a.Stream}, nil
}

func anthropicStop(r engine.Result) string {
	if len(r.ToolCalls) > 0 || r.FinishReason == "tool_calls" {
		return "tool_use"
	}
	if r.FinishReason == "length" {
		return "max_tokens"
	}
	return "end_turn"
}

func anthropicContent(r engine.Result) []any {
	content := make([]any, 0, 1+len(r.ToolCalls))
	if r.Text != "" || len(r.ToolCalls) == 0 {
		content = append(content, map[string]any{"type": "text", "text": r.Text})
	}
	for _, t := range r.ToolCalls {
		content = append(content, map[string]any{"type": "tool_use", "id": t.ID, "name": t.Function.Name, "input": json.RawMessage(t.Function.Arguments)})
	}
	return content
}
