package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wklee610/clivoy/internal/engine"
)

const maxImageResponseBytes = 64 << 20

var _ engine.ImageProvider = (*openAIProvider)(nil)

func (p *openAIProvider) ValidateImage(req engine.ImageRequest) error {
	if strings.TrimSpace(req.Model) == "" {
		return engine.Invalid("an upstream model is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return engine.Invalid("prompt must not be empty")
	}
	if req.Stream {
		return engine.Invalid("image streaming is not supported")
	}
	if req.N != nil && (*req.N < 1 || *req.N > 10) {
		return engine.Invalid("n must be between 1 and 10")
	}
	if req.OutputCompression != nil && (*req.OutputCompression < 0 || *req.OutputCompression > 100) {
		return engine.Invalid("output_compression must be between 0 and 100")
	}
	if !validImageSize(req.Size) {
		return engine.Invalid("size must be auto or WIDTHxHEIGHT with dimensions between 1 and 65536")
	}
	for _, field := range []struct {
		name, value string
		allowed     []string
	}{
		{"quality", req.Quality, []string{"auto", "high", "medium", "low", "standard", "hd", "xhigh", "max"}},
		{"response_format", req.ResponseFormat, []string{"b64_json", "url"}},
		{"output_format", req.OutputFormat, []string{"png", "jpeg", "webp"}},
		{"background", req.Background, []string{"transparent", "opaque", "auto"}},
		{"moderation", req.Moderation, []string{"auto", "low"}},
		{"style", req.Style, []string{"vivid", "natural"}},
	} {
		if field.value == "" {
			continue
		}
		valid := false
		for _, allowed := range field.allowed {
			if field.value == allowed {
				valid = true
				break
			}
		}
		if !valid {
			return engine.Invalid("unsupported " + field.name)
		}
	}
	return nil
}

// Backends choose supported dimensions. Bound numeric parsing without imposing
// a model-specific size list or allocating an image locally.
func validImageSize(size string) bool {
	if size == "" || size == "auto" {
		return true
	}
	width, height, ok := strings.Cut(size, "x")
	if !ok {
		return false
	}
	for _, dimension := range []string{width, height} {
		if len(dimension) == 0 || len(dimension) > 6 {
			return false
		}
		for _, digit := range dimension {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		n, err := strconv.ParseUint(dimension, 10, 32)
		if err != nil || n == 0 || n > 65536 {
			return false
		}
	}
	return true
}

func (p *openAIProvider) GenerateImage(ctx context.Context, req engine.ImageRequest) (json.RawMessage, error) {
	if err := p.ValidateImage(req); err != nil {
		return nil, err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, engine.Invalid("request could not be encoded")
	}
	endpoint := strings.TrimSuffix(p.endpoint, "/chat/completions") + "/images/generations"
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, upstreamError("could not prepare upstream image request")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		r.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, upstreamError("could not connect to upstream provider")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := http.StatusBadGateway
		if resp.StatusCode == http.StatusTooManyRequests {
			status = http.StatusTooManyRequests
		}
		return nil, &engine.Error{Status: status, Type: "provider_error", Message: fmt.Sprintf("upstream provider returned HTTP %d", resp.StatusCode)}
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxImageResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, upstreamError("could not read upstream image response")
	}
	if len(data) > maxImageResponseBytes {
		return nil, upstreamError("upstream image response exceeded the size limit")
	}
	if !validImageResponse(data) {
		return nil, upstreamError("upstream returned an invalid image response")
	}
	// Keep the original response, including usage, revised prompts, and metadata.
	return json.RawMessage(data), nil
}

func validImageResponse(data []byte) bool {
	var result struct {
		Created *int64            `json:"created"`
		Data    []json.RawMessage `json:"data"`
		Error   json.RawMessage   `json:"error"`
	}
	if json.Unmarshal(data, &result) != nil || hasError(result.Error) || result.Created == nil || *result.Created < 0 || len(result.Data) == 0 || len(result.Data) > 10 {
		return false
	}
	for _, raw := range result.Data {
		var item struct {
			B64JSON       *string `json:"b64_json"`
			URL           *string `json:"url"`
			RevisedPrompt *string `json:"revised_prompt"`
		}
		if json.Unmarshal(raw, &item) != nil || (item.B64JSON == nil && item.URL == nil) {
			return false
		}
		if item.B64JSON != nil {
			// Validate without retaining another decoded copy of the image.
			n, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(*item.B64JSON)))
			if err != nil || n == 0 {
				return false
			}
		}
		if item.URL != nil {
			u, err := url.Parse(*item.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
				return false
			}
		}
	}
	return true
}
