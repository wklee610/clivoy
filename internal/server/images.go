package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

func (m Model) supports(capability string) bool {
	for _, c := range m.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

func hasImageInput(req engine.Request) bool {
	for _, m := range req.Messages {
		parts, _ := engine.ParseContent(m.Content)
		for _, part := range parts {
			if part.Type == "image_url" {
				return true
			}
		}
	}
	return false
}

func (s *Server) generateImage(w http.ResponseWriter, r *http.Request) {
	var request engine.ImageRequest
	if err := decode(r, &request); err != nil {
		s.writeError(w, "openai", err)
		return
	}
	if strings.TrimSpace(request.Model) == "" {
		s.writeError(w, "openai", engine.Invalid("model is required"))
		return
	}
	m, ok := s.options.Models[request.Model]
	if !ok {
		s.writeError(w, "openai", &engine.Error{Status: 404, Type: "not_found_error", Message: "Unknown model; use GET /v1/models to list configured aliases"})
		return
	}
	if !m.supports("image_generation") {
		s.writeError(w, "openai", engine.Invalid("Selected model does not support image generation; choose an image_generation model from /v1/models"))
		return
	}
	p, ok := m.Provider.(engine.ImageProvider)
	if !ok {
		s.writeError(w, "openai", engine.Invalid("Selected provider does not support image generation"))
		return
	}
	request.Model = m.UpstreamModel
	if err := p.ValidateImage(request); err != nil {
		s.writeError(w, "openai", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.options.RequestTimeout)
	defer cancel()
	if err := s.acquire(ctx); err != nil {
		s.writeError(w, "openai", err)
		return
	}
	defer func() { <-s.slots }()
	result, err := p.GenerateImage(ctx, request)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.writeError(w, "openai", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// Preserve the order of interleaved text and image blocks. Plain text stays a
// string for compatibility with text-only upstreams and conversation history.
func encodeContentParts(parts []engine.ContentPart) json.RawMessage {
	images := false
	for _, part := range parts {
		if part.Type == "image_url" {
			images = true
		}
	}
	if !images {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return rawString(b.String())
	}
	wire := make([]any, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" {
			wire = append(wire, map[string]any{"type": "text", "text": part.Text})
		} else {
			wire = append(wire, map[string]any{"type": "image_url", "image_url": part.ImageURL})
		}
	}
	return rawObject(wire)
}

func responsesContent(raw json.RawMessage) (json.RawMessage, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return rawString(text), nil
	}
	var blocks []json.RawMessage
	if err := strictRaw(raw, &blocks); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, engine.Invalid("Input content must not be empty")
	}
	parts := make([]engine.ContentPart, 0, len(blocks))
	for _, rawBlock := range blocks {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawBlock, &header); err != nil {
			return nil, engine.Invalid("Invalid Responses content block")
		}
		switch header.Type {
		case "input_text":
			var block struct {
				Type string  `json:"type"`
				Text *string `json:"text"`
			}
			if err := strictRaw(rawBlock, &block); err != nil {
				return nil, err
			}
			if block.Text == nil {
				return nil, engine.Invalid("Text blocks require text")
			}
			parts = append(parts, engine.ContentPart{Type: "text", Text: *block.Text})
		case "output_text":
			var block struct {
				Type        string          `json:"type"`
				Text        *string         `json:"text"`
				Annotations json.RawMessage `json:"annotations,omitempty"`
				Logprobs    json.RawMessage `json:"logprobs,omitempty"`
			}
			if err := strictRaw(rawBlock, &block); err != nil {
				return nil, err
			}
			if block.Text == nil {
				return nil, engine.Invalid("Text blocks require text")
			}
			parts = append(parts, engine.ContentPart{Type: "text", Text: *block.Text})
		case "input_image":
			var block struct {
				Type     string `json:"type"`
				ImageURL string `json:"image_url,omitempty"`
				FileID   string `json:"file_id,omitempty"`
				Detail   string `json:"detail,omitempty"`
			}
			if err := strictRaw(rawBlock, &block); err != nil {
				return nil, err
			}
			if block.FileID != "" {
				return nil, engine.Invalid("Image file_id is not supported; use image_url with an HTTP(S) URL or base64 data URI")
			}
			if block.ImageURL == "" {
				return nil, engine.Invalid("input_image requires image_url")
			}
			parts = append(parts, engine.ContentPart{Type: "image_url", ImageURL: &engine.ImageURL{URL: block.ImageURL, Detail: block.Detail}})
		default:
			return nil, engine.Invalid("Unsupported Responses content type: " + header.Type)
		}
	}
	return encodeContentParts(parts), nil
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

func (source *anthropicImageSource) UnmarshalJSON(raw []byte) error {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return engine.Invalid("Invalid image source")
	}
	*source = anthropicImageSource{Type: header.Type}
	switch header.Type {
	case "base64":
		var value struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		}
		if err := strictRaw(raw, &value); err != nil {
			return err
		}
		source.MediaType, source.Data = value.MediaType, value.Data
	case "url":
		var value struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}
		if err := strictRaw(raw, &value); err != nil {
			return err
		}
		source.URL = value.URL
	default:
		return engine.Invalid("Image source type must be base64 or url")
	}
	return nil
}

func (source *anthropicImageSource) imagePart() (engine.ContentPart, error) {
	if source == nil {
		return engine.ContentPart{}, engine.Invalid("image blocks require a source")
	}
	var value string
	switch source.Type {
	case "base64":
		if source.MediaType == "" || source.Data == "" || source.URL != "" {
			return engine.ContentPart{}, engine.Invalid("Base64 image source requires media_type and data")
		}
		value = "data:" + source.MediaType + ";base64," + source.Data
	case "url":
		if source.URL == "" || source.MediaType != "" || source.Data != "" {
			return engine.ContentPart{}, engine.Invalid("URL image source requires only url")
		}
		if !strings.HasPrefix(source.URL, "https://") && !strings.HasPrefix(source.URL, "http://") {
			return engine.ContentPart{}, engine.Invalid("URL image source must use HTTP(S)")
		}
		value = source.URL
	default:
		return engine.ContentPart{}, engine.Invalid("Image source type must be base64 or url")
	}
	return engine.ContentPart{Type: "image_url", ImageURL: &engine.ImageURL{URL: value}}, nil
}
