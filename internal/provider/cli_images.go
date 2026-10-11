package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

// prepareInput keeps image bytes off argv, preserves image order using numbered
// transcript markers, and returns cleanup even when preparation fails midway.
func (p *cliProvider) prepareInput(req engine.Request) (args []string, input string, cleanup func(), err error) {
	if p.kind == "grok" {
		return p.prepareGrokInput(req)
	}
	args = p.args(req)
	input = cliPrompt(req)
	if p.kind == "gemini" {
		// Gemini expands @file references before sending the prompt. Preserve
		// literal @ characters inside the JSON transcript without invoking it.
		input = strings.ReplaceAll(input, "@", `\u0040`)
	}
	cleanup = func() {}
	var urls []string
	for _, message := range req.Messages {
		parts, parseErr := engine.ParseContent(message.Content)
		if parseErr != nil {
			return args, "", cleanup, parseErr
		}
		for _, part := range parts {
			if part.Type == "image_url" {
				urls = append(urls, part.ImageURL.URL)
			}
		}
	}
	if len(urls) == 0 {
		return args, input, cleanup, nil
	}
	if p.kind == "claude" {
		args = append(args, "--input-format", "stream-json")
		blocks := []any{map[string]string{"type": "text", "text": input}}
		for i, value := range urls {
			media, data, decodeErr := engine.DecodeImageDataURI(value)
			if decodeErr != nil {
				return args, "", cleanup, decodeErr
			}
			blocks = append(blocks, map[string]string{"type": "text", "text": fmt.Sprintf("Attached image %d:", i+1)}, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": media, "data": base64.StdEncoding.EncodeToString(data)}})
		}
		payload := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": blocks}, "parent_tool_use_id": nil}
		encoded, encodeErr := json.Marshal(payload)
		if encodeErr != nil {
			return args, "", cleanup, engine.Invalid("could not encode CLI image input")
		}
		return args, string(encoded) + "\n", cleanup, nil
	}
	dir, createErr := os.MkdirTemp(p.tempDir, "clivoy-images-*")
	if createErr != nil {
		return args, "", cleanup, upstreamError("could not create temporary image directory")
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	args = args[:len(args)-1] // Replace the stdin marker after adding image flags.
	for i, value := range urls {
		media, data, decodeErr := engine.DecodeImageDataURI(value)
		if decodeErr != nil {
			cleanup()
			return args, "", func() {}, decodeErr
		}
		ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}[media]
		path := filepath.Join(dir, fmt.Sprintf("image-%02d%s", i+1, ext))
		if writeErr := os.WriteFile(path, data, 0600); writeErr != nil {
			cleanup()
			return args, "", func() {}, upstreamError("could not write temporary image input")
		}
		args = append(args, "--image", path)
	}
	args = append(args, "--", "-")
	return args, input, cleanup, nil
}
