package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

const samplePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII="

type imageTestProvider struct {
	chatCalls  int
	imageCalls int
	chat       engine.Request
	image      engine.ImageRequest
	wait       bool
}

func (p *imageTestProvider) Validate(engine.Request) error { return nil }
func (p *imageTestProvider) Generate(_ context.Context, r engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
	p.chatCalls++
	p.chat = r
	return engine.Result{Text: "I see an image", FinishReason: "stop"}, nil
}
func (p *imageTestProvider) ValidateImage(r engine.ImageRequest) error {
	p.image = r
	if r.Stream {
		return engine.Invalid("image streaming is not supported")
	}
	return nil
}
func (p *imageTestProvider) GenerateImage(ctx context.Context, r engine.ImageRequest) (json.RawMessage, error) {
	p.imageCalls++
	p.image = r
	if p.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return rawObject(map[string]any{"created": 123, "data": []any{map[string]string{"b64_json": samplePNG}}, "usage": map[string]int{"total_tokens": 4}}), nil
}

func imageServer(t *testing.T, p *imageTestProvider) *Server {
	t.Helper()
	s, err := New(Options{APIKey: "test", RequestTimeout: 20 * time.Millisecond, MaxConcurrent: 1, MaxQueue: 1, MaxBodyBytes: 32 << 20, Models: map[string]Model{
		"text":   {Provider: p, UpstreamModel: "text-upstream"},
		"vision": {Provider: p, UpstreamModel: "vision-upstream", Capabilities: []string{"chat", "vision"}},
		"draw":   {Provider: p, UpstreamModel: "image-upstream", Capabilities: []string{"image_generation"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func imageInvoke(s *Server, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestImageInputAcrossProtocols(t *testing.T) {
	dataURL := "data:image/png;base64," + samplePNG
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"vision","messages":[{"role":"user","content":[{"type":"text","text":"first"},{"type":"image_url","image_url":{"url":"` + dataURL + `"}},{"type":"text","text":"last"}]}]}`},
		{"/v1/responses", `{"model":"vision","input":[{"role":"user","content":[{"type":"input_text","text":"first"},{"type":"input_image","image_url":"` + dataURL + `"},{"type":"input_text","text":"last"}]}]}`},
		{"/v1/messages", `{"model":"vision","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"first"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + samplePNG + `"}},{"type":"text","text":"last"}]}]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			p := &imageTestProvider{}
			w := imageInvoke(imageServer(t, p), tc.path, tc.body)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			parts, err := engine.ParseContent(p.chat.Messages[0].Content)
			if err != nil || len(parts) != 3 {
				t.Fatalf("parts=%+v err=%v", parts, err)
			}
			if parts[0].Text != "first" || parts[1].ImageURL.URL != dataURL || parts[2].Text != "last" || p.chat.Model != "vision-upstream" {
				t.Fatal("image content, order, or model alias was lost")
			}
		})
	}
}

func TestImageCapabilitiesPreventWrongCalls(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`},
		{"/v1/chat/completions", `{"model":"draw","messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/images/generations", `{"model":"vision","prompt":"a cat"}`},
	} {
		t.Run(tc.body, func(t *testing.T) {
			p := &imageTestProvider{}
			w := imageInvoke(imageServer(t, p), tc.path, tc.body)
			if w.Code != 400 || p.chatCalls != 0 || p.imageCalls != 0 {
				t.Fatalf("incorrect model was called: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestImageGenerationResponseAndTimeout(t *testing.T) {
	p := &imageTestProvider{}
	s := imageServer(t, p)
	w := imageInvoke(s, "/v1/images/generations", `{"model":"draw","prompt":"a cat","n":1,"output_format":"png"}`)
	if w.Code != 200 || p.image.Model != "image-upstream" || p.image.Prompt != "a cat" {
		t.Fatalf("unexpected generation: %d %s %+v", w.Code, w.Body, p.image)
	}
	var response struct {
		Data []struct {
			B64 string `json:"b64_json"`
		}
		Usage map[string]int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].B64 != samplePNG || response.Usage["total_tokens"] != 4 {
		t.Fatal("image bytes or usage changed")
	}
	p.wait = true
	w = imageInvoke(s, "/v1/images/generations", `{"model":"draw","prompt":"a cat"}`)
	if w.Code != 504 || len(s.slots) != 0 {
		t.Fatalf("image request timeout/cleanup failed: %d %s", w.Code, w.Body)
	}
}

func TestImagesRequireAuthentication(t *testing.T) {
	p := &imageTestProvider{}
	s := imageServer(t, p)
	r := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"draw","prompt":"a cat"}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 || p.imageCalls != 0 {
		t.Fatal("image endpoint bypassed authentication")
	}
}

type blockingImageProvider struct {
	calls atomic.Int32
}

func (p *blockingImageProvider) Validate(engine.Request) error           { return nil }
func (p *blockingImageProvider) ValidateImage(engine.ImageRequest) error { return nil }
func (p *blockingImageProvider) Generate(ctx context.Context, _ engine.Request, _ func(engine.Delta) error) (engine.Result, error) {
	p.calls.Add(1)
	<-ctx.Done()
	return engine.Result{}, ctx.Err()
}
func (p *blockingImageProvider) GenerateImage(ctx context.Context, _ engine.ImageRequest) (json.RawMessage, error) {
	p.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestImagesShareChatQueueAndCancelBeforeGeneration(t *testing.T) {
	chatPath, imagePath := "/v1/chat/completions", "/v1/images/generations"
	for _, activePath := range []string{chatPath, imagePath} {
		t.Run(activePath, func(t *testing.T) {
			p := &blockingImageProvider{}
			s, err := New(Options{APIKey: "test-gateway-key", RequestTimeout: 2 * time.Second,
				MaxConcurrent: 1, MaxQueue: 1, MaxBodyBytes: 1 << 20,
				Models: map[string]Model{"test-alias": {Provider: p, UpstreamModel: "upstream", Capabilities: []string{"chat", "image_generation"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := func(path string) *http.Request {
				body := `{"model":"test-alias","prompt":"a blue bird"}`
				if path == chatPath {
					body = bodyFor(chatPath, false)
				}
				return apiRequest(path, body)
			}
			queuedPath := imagePath
			if activePath == imagePath {
				queuedPath = chatPath
			}
			activeCtx, cancelActive := context.WithCancel(context.Background())
			defer cancelActive()
			active := asyncInvoke(s, request(activePath).WithContext(activeCtx))
			waitFor(t, func() bool { return p.calls.Load() == 1 })
			queuedCtx, cancelQueued := context.WithCancel(context.Background())
			defer cancelQueued()
			queued := asyncInvoke(s, request(queuedPath).WithContext(queuedCtx))
			waitFor(t, func() bool { return len(s.queue) == 1 })
			if w := invoke(s, request(imagePath)); w.Code != 429 {
				t.Fatalf("image request did not share the full queue: %d %s", w.Code, w.Body)
			}
			cancelQueued()
			if w := awaitResponse(t, queued); w.Code != 408 || p.calls.Load() != 1 || len(s.queue) != 0 {
				t.Fatalf("cancelled queued request invoked a provider or retained capacity: %d calls=%d", w.Code, p.calls.Load())
			}
			cancelActive()
			if w := awaitResponse(t, active); w.Code != 408 || len(s.slots) != 0 {
				t.Fatalf("active request cancellation did not release its slot: %d", w.Code)
			}
		})
	}
}
