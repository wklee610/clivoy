// Package provider implements local CLI and OpenAI-compatible HTTP backends.
package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

const (
	maxOutputBytes = 8 << 20
	maxWireBytes   = 32 << 20
	maxStderrBytes = 64 << 10
	maxToolCalls   = 128
)

func upstreamError(message string) error {
	return &engine.Error{Status: 502, Type: "provider_error", Message: message}
}

func textContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", engine.Invalid("message content must be text")
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", engine.Invalid("only text message content is supported")
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

func validateMessages(req engine.Request, allowRemoteImages bool) error {
	if len(req.Messages) == 0 {
		return engine.Invalid("messages must not be empty")
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return engine.Invalid("unsupported message role")
		}
	}
	if err := engine.ValidateMessageContent(req.Messages, allowRemoteImages); err != nil {
		return err
	}
	if req.MaxTokens != nil && *req.MaxTokens <= 0 {
		return engine.Invalid("max_tokens must be positive")
	}
	return nil
}

func cliPrompt(req engine.Request) string {
	// Role boundaries are represented as a transcript; the CLIs do not expose
	// an API with the same system/developer role hierarchy as Chat Completions.
	type turn struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	turns := make([]turn, 0, len(req.Messages))
	imageIndex := 0
	for _, m := range req.Messages {
		parts, _ := engine.ParseContent(m.Content)
		var content strings.Builder
		for _, part := range parts {
			if part.Type == "text" {
				content.WriteString(part.Text)
			} else {
				imageIndex++
				fmt.Fprintf(&content, "\n[Attached image %d]\n", imageIndex)
			}
		}
		turns = append(turns, turn{m.Role, content.String()})
	}
	data, _ := json.Marshal(turns)
	prompt := "Continue the following conversation. Respond to its last user message with only your answer. The JSON entries preserve the conversation's roles and order. Do not use tools or inspect the local environment.\n"
	if imageIndex > 0 {
		prompt += "Attached images are numbered in conversation order and correspond to the numbered markers in the transcript. Analyze the attached image content directly.\n"
	}
	if req.MaxTokens != nil {
		prompt += fmt.Sprintf("Keep the response within approximately %d tokens. This is a requested response length, not a hard token limit.\n", *req.MaxTokens)
	}
	return prompt + string(data)
}
