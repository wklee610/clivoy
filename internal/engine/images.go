package engine

import (
	"context"
	"encoding/json"
)

// ImageRequest is the JSON Images API subset accepted by Clivoy.
// Model is an administrator-selected upstream model, never an API endpoint.
type ImageRequest struct {
	Model             string `json:"model"`
	Prompt            string `json:"prompt"`
	N                 *int   `json:"n,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	ResponseFormat    string `json:"response_format,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	OutputCompression *int   `json:"output_compression,omitempty"`
	Background        string `json:"background,omitempty"`
	Moderation        string `json:"moderation,omitempty"`
	Style             string `json:"style,omitempty"`
	User              string `json:"user,omitempty"`
	Stream            bool   `json:"stream,omitempty"`
}

// ImageProvider is optional: only backends with an Images API implement it.
// The validated JSON response preserves image bytes/URLs and upstream usage.
type ImageProvider interface {
	ValidateImage(ImageRequest) error
	GenerateImage(context.Context, ImageRequest) (json.RawMessage, error)
}
