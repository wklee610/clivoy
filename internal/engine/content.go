package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	MaxInputImages      = 16
	MaxInputImageBytes  = 5 << 20
	MaxInputImagesBytes = 20 << 20
)

// ContentPart is the canonical Chat Completions text/image input shape.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// ParseContent validates structure without fetching remote URLs or decoding
// image data. ValidateMessageContent applies image limits across all messages.
func ParseContent(raw json.RawMessage) ([]ContentPart, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []ContentPart{{Type: "text", Text: text}}, nil
	}
	var parts []struct {
		Type        string          `json:"type"`
		Text        *string         `json:"text"`
		ImageURL    *ImageURL       `json:"image_url"`
		Annotations json.RawMessage `json:"annotations,omitempty"`
		Logprobs    json.RawMessage `json:"logprobs,omitempty"`
		Citations   json.RawMessage `json:"citations,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&parts) != nil || d.Decode(new(any)) != io.EOF {
		return nil, Invalid("message content must be text or text/image_url content blocks")
	}
	result := make([]ContentPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			if part.Text == nil || part.ImageURL != nil {
				return nil, Invalid("text content blocks require text and cannot contain an image_url")
			}
			result = append(result, ContentPart{Type: "text", Text: *part.Text})
		case "image_url":
			if part.ImageURL == nil || part.ImageURL.URL == "" || part.Text != nil {
				return nil, Invalid("image_url content blocks require an image_url object with a URL")
			}
			switch part.ImageURL.Detail {
			case "", "auto", "low", "high", "original":
			default:
				return nil, Invalid("image detail must be auto, low, high, or original")
			}
			result = append(result, ContentPart{Type: "image_url", ImageURL: part.ImageURL})
		default:
			return nil, Invalid("unsupported content type; use text or image_url")
		}
	}
	return result, nil
}

// DecodeImageDataURI validates a base64 PNG/JPEG/WebP/GIF without decoding its
// pixels. The byte signature must agree with the declared media type.
func DecodeImageDataURI(value string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return "", nil, Invalid("images must use a base64 data URI")
	}
	media := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	switch media {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
	default:
		return "", nil, Invalid("supported image media types are PNG, JPEG, WebP, and GIF")
	}
	if len(encoded) == 0 || len(encoded) > base64.StdEncoding.EncodedLen(MaxInputImageBytes) {
		return "", nil, Invalid("each decoded input image must be nonempty and at most 5 MiB")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", nil, Invalid("image data contains invalid base64")
	}
	if len(data) == 0 || len(data) > MaxInputImageBytes {
		return "", nil, Invalid("each decoded input image must be nonempty and at most 5 MiB")
	}
	if http.DetectContentType(data) != media {
		return "", nil, Invalid("image bytes do not match the declared PNG, JPEG, WebP, or GIF media type")
	}
	return media, data, nil
}

// ValidateImageURL never dereferences a URL. Remote image retrieval belongs to
// the configured HTTP backend, while local CLIs accept only data URIs.
func ValidateImageURL(value string) error {
	if strings.HasPrefix(value, "data:") {
		_, _, err := DecodeImageDataURI(value)
		return err
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Hostname() == "" || u.User != nil {
		return Invalid("image URLs must be absolute HTTP(S) URLs without credentials or supported base64 data URIs")
	}
	return nil
}

// ValidateMessageContent enforces count and decoded-byte limits over an entire
// request. Remote image sizes cannot be checked without fetching their URLs.
func ValidateMessageContent(messages []Message, allowRemote bool) error {
	count, total := 0, 0
	for _, message := range messages {
		parts, err := ParseContent(message.Content)
		if err != nil {
			return err
		}
		for _, part := range parts {
			if part.Type != "image_url" {
				continue
			}
			if message.Role != "user" {
				return Invalid("image input is supported only in user messages")
			}
			count++
			if count > MaxInputImages {
				return Invalid("at most 16 input images are supported per request")
			}
			if strings.HasPrefix(part.ImageURL.URL, "data:") {
				_, data, err := DecodeImageDataURI(part.ImageURL.URL)
				if err != nil {
					return err
				}
				total += len(data)
				if total > MaxInputImagesBytes {
					return Invalid("decoded input images must total at most 20 MiB per request")
				}
			} else {
				if !allowRemote {
					return Invalid("CLI image input requires base64 data URIs; remote image URLs are supported only by HTTP backends")
				}
				if err := ValidateImageURL(part.ImageURL.URL); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
