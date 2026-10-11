package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
)

func TestHTTPMultimodalPassThroughWithoutLocalFetch(t *testing.T) {
	var imageFetched atomic.Bool
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { imageFetched.Store(true); w.WriteHeader(500) }))
	defer imageServer.Close()
	remoteURL := imageServer.URL + "/image.png"
	request := basicRequest()
	request.Messages[0].Content, _ = json.Marshal([]engine.ContentPart{{Type: "text", Text: "Compare these images."}, {Type: "image_url", ImageURL: &engine.ImageURL{URL: remoteURL, Detail: "high"}}, {Type: "image_url", ImageURL: &engine.ImageURL{URL: "data:image/png;base64," + inputTestPNG, Detail: "original"}}})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got engine.Request
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if len(got.Messages) != 1 || string(got.Messages[0].Content) != string(request.Messages[0].Content) {
			t.Error("multimodal content was altered instead of forwarded")
		}
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"content":"Two images."},"finish_reason":"stop"}]}`)
	}))
	defer backend.Close()
	p, _ := NewOpenAI(backend.URL+"/v1", "")
	result, err := p.Generate(context.Background(), request, nil)
	if err != nil || result.Text != "Two images." {
		t.Fatalf("image request failed: %+v %v", result, err)
	}
	if imageFetched.Load() {
		t.Fatal("gateway fetched a remote image URL")
	}
}

func TestMultimodalValidationRejectsBeforeHTTP(t *testing.T) {
	var called atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer backend.Close()
	p, _ := NewOpenAI(backend.URL, "")
	for _, url := range []string{"file:///etc/passwd", "data:image/png;base64,secret-invalid-payload"} {
		req := basicRequest()
		req.Messages[0].Content, _ = json.Marshal([]engine.ContentPart{{Type: "image_url", ImageURL: &engine.ImageURL{URL: url}}})
		if _, err := p.Generate(context.Background(), req, nil); err == nil {
			t.Fatal("invalid image accepted")
		}
	}
	if called.Load() {
		t.Fatal("invalid input reached upstream")
	}
}
