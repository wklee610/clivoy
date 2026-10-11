package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const testPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jD1sAAAAASUVORK5CYII="

func imageMessage(urls ...string) Message {
	parts := make([]ContentPart, 0, len(urls))
	for _, value := range urls {
		parts = append(parts, ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: value}})
	}
	content, _ := json.Marshal(parts)
	return Message{Role: "user", Content: content}
}

func TestImageContentValidation(t *testing.T) {
	dataURI := "data:image/png;base64," + testPNG
	for _, url := range []string{dataURI, "https://example.com/image.png?signature=abc", "http://localhost:9000/image.jpg"} {
		if err := ValidateMessageContent([]Message{imageMessage(url)}, true); err != nil {
			t.Errorf("valid URL rejected: %v", err)
		}
	}
	if err := ValidateMessageContent([]Message{imageMessage(dataURI)}, false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMessageContent([]Message{imageMessage("https://example.com/image.png")}, false); err == nil {
		t.Fatal("CLI remote image URL accepted")
	}
	for _, url := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://example.com/image.png", "https://user:secret@example.com/image.png", "/local/image.png", "data:image/svg+xml;base64,PHN2Zy8+", "data:image/png;base64,not-valid-base64", "data:image/jpeg;base64," + testPNG} {
		if err := ValidateMessageContent([]Message{imageMessage(url)}, true); err == nil {
			t.Errorf("invalid image accepted: %.50s", url)
		}
	}
	message := imageMessage(dataURI)
	message.Role = "assistant"
	if err := ValidateMessageContent([]Message{message}, true); err == nil {
		t.Fatal("assistant image accepted")
	}
}

func TestContentShapeValidation(t *testing.T) {
	for _, raw := range []string{`[{"type":"image_url","image_url":"https://example.com/i.png"}]`, `[{"type":"image_url","image_url":{"url":"https://example.com/i.png","detail":"unknown"}}]`, `[{"type":"text"}]`, `[{"type":"text","text":"hi","image_url":{"url":"https://example.com/i.png"}}]`, `[{"type":"input_audio"}]`, `[{"type":"image_url","image_url":{"url":"https://example.com/i.png","unexpected":true}}]`} {
		if _, err := ParseContent(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid content accepted: %s", raw)
		}
	}
	parts, err := ParseContent(json.RawMessage(`[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/i.png","detail":"original"}}]`))
	if err != nil || len(parts) != 2 || parts[1].ImageURL.Detail != "original" {
		t.Fatalf("valid content lost: %+v %v", parts, err)
	}
}

func TestImageCountAndDecodedByteLimits(t *testing.T) {
	dataURI := "data:image/png;base64," + testPNG
	urls := make([]string, MaxInputImages+1)
	for i := range urls {
		urls[i] = dataURI
	}
	if err := ValidateMessageContent([]Message{imageMessage(urls...)}, true); err == nil {
		t.Fatal("too many images accepted")
	}
	if err := ValidateMessageContent([]Message{imageMessage(urls[:MaxInputImages]...)}, true); err != nil {
		t.Fatal(err)
	}
	oversize := "data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxInputImageBytes)+4)
	if _, _, err := DecodeImageDataURI(oversize); err == nil {
		t.Fatal("oversize image accepted")
	}
	// Valid PNG signature followed by opaque bytes tests byte accounting without
	// allocating decoded pixels or relying on image-library decompression.
	data := bytes.Repeat([]byte{0}, MaxInputImageBytes)
	copy(data, []byte("\x89PNG\r\n\x1a\n"))
	large := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if _, _, err := DecodeImageDataURI(large); err != nil {
		t.Fatal(err)
	}
	message := imageMessage(large)
	messages := []Message{message, message, message, message, message}
	if err := ValidateMessageContent(messages[:4], true); err != nil {
		t.Fatalf("20 MiB boundary rejected: %v", err)
	}
	if err := ValidateMessageContent(messages, true); err == nil {
		t.Fatal("aggregate image limit not enforced across messages")
	}
}
