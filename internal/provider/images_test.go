package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

func basicImageRequest() engine.ImageRequest {
	return engine.ImageRequest{Model: "image-model", Prompt: "A lighthouse at dusk"}
}

func TestOpenAIImageRequestAndResponsePassthrough(t *testing.T) {
	var fetched atomic.Bool
	imageServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fetched.Store(true) }))
	defer imageServer.Close()
	n, compression := 2, 0
	req := basicImageRequest()
	req.N, req.OutputCompression = &n, &compression
	req.Size, req.Quality = "1376x768", "max"
	req.ResponseFormat, req.OutputFormat = "b64_json", "png"
	req.Background, req.Moderation, req.Style, req.User = "transparent", "low", "natural", "local-user"
	body := fmt.Sprintf(" {\n\"created\":123,\"data\":[{\"b64_json\":\"aW1hZ2U=\",\"url\":null,\"revised_prompt\":\"A glowing lighthouse\",\"extra\":true},{\"url\":%q,\"b64_json\":null,\"revised_prompt\":null}],\"usage\":{\"total_tokens\":12,\"input_tokens_details\":{\"text_tokens\":3}},\"extra_metadata\":[1,2],\"error\":null}\n", imageServer.URL+"/image.png?signature=retained")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fake-image-key" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Error("image request headers not forwarded")
		}
		var got engine.ImageRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		} else if !reflect.DeepEqual(got, req) {
			t.Errorf("image settings not forwarded: got %+v, want %+v", got, req)
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	p, err := NewOpenAI(server.URL+"/v1/", "fake-image-key")
	if err != nil {
		t.Fatal(err)
	}
	images, ok := p.(engine.ImageProvider)
	if !ok {
		t.Fatal("OpenAI backend does not implement ImageProvider")
	}
	got, err := images.GenerateImage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("response changed: %s", got)
	}
	if fetched.Load() {
		t.Fatal("provider fetched a returned image URL")
	}
}

func TestOpenAIImageValidation(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	p, _ := NewOpenAI(server.URL, "")
	images := p.(engine.ImageProvider)
	for _, tc := range []struct {
		name   string
		change func(*engine.ImageRequest)
	}{
		{"missing_model", func(r *engine.ImageRequest) { r.Model = " \t" }},
		{"missing_prompt", func(r *engine.ImageRequest) { r.Prompt = " \n" }},
		{"stream", func(r *engine.ImageRequest) { r.Stream = true }},
		{"n_zero", func(r *engine.ImageRequest) { n := 0; r.N = &n }},
		{"n_negative", func(r *engine.ImageRequest) { n := -1; r.N = &n }},
		{"n_large", func(r *engine.ImageRequest) { n := 11; r.N = &n }},
		{"compression_negative", func(r *engine.ImageRequest) { n := -1; r.OutputCompression = &n }},
		{"compression_large", func(r *engine.ImageRequest) { n := 101; r.OutputCompression = &n }},
		{"size", func(r *engine.ImageRequest) { r.Size = "1024" }},
		{"size_zero", func(r *engine.ImageRequest) { r.Size = "0x1024" }},
		{"size_negative", func(r *engine.ImageRequest) { r.Size = "1024x-1" }},
		{"size_signed", func(r *engine.ImageRequest) { r.Size = "+1024x1024" }},
		{"size_fractional", func(r *engine.ImageRequest) { r.Size = "1024.5x1024" }},
		{"size_missing_height", func(r *engine.ImageRequest) { r.Size = "1024x" }},
		{"size_missing_width", func(r *engine.ImageRequest) { r.Size = "x1024" }},
		{"size_extra_dimension", func(r *engine.ImageRequest) { r.Size = "1024x1024x3" }},
		{"size_uppercase", func(r *engine.ImageRequest) { r.Size = "1024X1024" }},
		{"size_whitespace", func(r *engine.ImageRequest) { r.Size = "1024x 1024" }},
		{"size_too_large", func(r *engine.ImageRequest) { r.Size = "65537x1024" }},
		{"size_overflow", func(r *engine.ImageRequest) { r.Size = "1024x999999999999999999999999" }},
		{"quality", func(r *engine.ImageRequest) { r.Quality = "ultra" }},
		{"response_format", func(r *engine.ImageRequest) { r.ResponseFormat = "binary" }},
		{"output_format", func(r *engine.ImageRequest) { r.OutputFormat = "gif" }},
		{"background", func(r *engine.ImageRequest) { r.Background = "white" }},
		{"moderation", func(r *engine.ImageRequest) { r.Moderation = "none" }},
		{"style", func(r *engine.ImageRequest) { r.Style = "photo" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := basicImageRequest()
			tc.change(&req)
			_, err := images.GenerateImage(context.Background(), req)
			var invalid *engine.Error
			if !errors.As(err, &invalid) || invalid.Status != http.StatusBadRequest {
				t.Fatalf("expected invalid request, got %v", err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("invalid image request reached upstream")
	}
	for _, n := range []int{1, 10} {
		for _, compression := range []int{0, 100} {
			req := basicImageRequest()
			req.N, req.OutputCompression = &n, &compression
			if err := images.ValidateImage(req); err != nil {
				t.Fatalf("valid boundary rejected: %v", err)
			}
		}
	}
	for _, tc := range []struct {
		values []string
		set    func(*engine.ImageRequest, string)
	}{
		{[]string{"", "auto", "1x1", "256x256", "512x512", "1024x1024", "1536x1024", "1024x1536", "1792x1024", "1024x1792", "1376x768", "4096x2048", "65536x65536"}, func(r *engine.ImageRequest, v string) { r.Size = v }},
		{[]string{"", "auto", "high", "medium", "low", "standard", "hd", "xhigh", "max"}, func(r *engine.ImageRequest, v string) { r.Quality = v }},
		{[]string{"", "b64_json", "url"}, func(r *engine.ImageRequest, v string) { r.ResponseFormat = v }},
		{[]string{"", "png", "jpeg", "webp"}, func(r *engine.ImageRequest, v string) { r.OutputFormat = v }},
		{[]string{"", "transparent", "opaque", "auto"}, func(r *engine.ImageRequest, v string) { r.Background = v }},
		{[]string{"", "auto", "low"}, func(r *engine.ImageRequest, v string) { r.Moderation = v }},
		{[]string{"", "vivid", "natural"}, func(r *engine.ImageRequest, v string) { r.Style = v }},
	} {
		for _, value := range tc.values {
			req := basicImageRequest()
			tc.set(&req, value)
			if err := images.ValidateImage(req); err != nil {
				t.Errorf("valid option %q rejected: %v", value, err)
			}
		}
	}
}

func TestOpenAIImageRejectsInvalidResponses(t *testing.T) {
	for name, body := range map[string]string{
		"invalid_json":        `{`,
		"top_level_array":     `[]`,
		"top_level_null":      `null`,
		"created_missing":     `{"data":[{"b64_json":"YQ=="}]}`,
		"created_null":        `{"created":null,"data":[{"b64_json":"YQ=="}]}`,
		"created_string":      `{"created":"1","data":[{"b64_json":"YQ=="}]}`,
		"created_fraction":    `{"created":1.5,"data":[{"b64_json":"YQ=="}]}`,
		"created_negative":    `{"created":-1,"data":[{"b64_json":"YQ=="}]}`,
		"created_overflow":    `{"created":999999999999999999999999,"data":[{"b64_json":"YQ=="}]}`,
		"data_missing":        `{"created":1}`,
		"data_empty":          `{"created":1,"data":[]}`,
		"data_null":           `{"created":1,"data":null}`,
		"data_object":         `{"created":1,"data":{}}`,
		"data_many":           `{"created":1,"data":[` + strings.Repeat(`{"b64_json":"YQ=="},`, 10) + `{"b64_json":"YQ=="}]}`,
		"item_null":           `{"created":1,"data":[null]}`,
		"item_string":         `{"created":1,"data":["secret-value"]}`,
		"item_empty":          `{"created":1,"data":[{}]}`,
		"both_null":           `{"created":1,"data":[{"b64_json":null,"url":null}]}`,
		"base64_number":       `{"created":1,"data":[{"b64_json":42}]}`,
		"base64_null":         `{"created":1,"data":[{"b64_json":null}]}`,
		"base64_empty":        `{"created":1,"data":[{"b64_json":""}]}`,
		"base64_invalid":      `{"created":1,"data":[{"b64_json":"secret-value"}]}`,
		"base64_noncanonical": `{"created":1,"data":[{"b64_json":"YR=="}]}`,
		"base64_no_bytes":     `{"created":1,"data":[{"b64_json":"\r\n"}]}`,
		"url_number":          `{"created":1,"data":[{"url":42}]}`,
		"url_null":            `{"created":1,"data":[{"url":null}]}`,
		"url_empty":           `{"created":1,"data":[{"url":""}]}`,
		"url_relative":        `{"created":1,"data":[{"url":"/image.png"}]}`,
		"url_missing_host":    `{"created":1,"data":[{"url":"https:///image.png"}]}`,
		"url_only_port":       `{"created":1,"data":[{"url":"https://:443/image.png"}]}`,
		"url_credentials":     `{"created":1,"data":[{"url":"https://user:secret-value@example.com/image.png"}]}`,
		"url_invalid_escape":  `{"created":1,"data":[{"url":"https://example.com/%xx"}]}`,
		"url_file":            `{"created":1,"data":[{"url":"file:///secret-value"}]}`,
		"url_data":            `{"created":1,"data":[{"url":"data:image/png;base64,YQ=="}]}`,
		"mixed_type_error":    `{"created":1,"data":[{"b64_json":"YQ==","url":false}]}`,
		"revised_type_error":  `{"created":1,"data":[{"b64_json":"YQ==","revised_prompt":{}}]}`,
		"error_payload":       `{"created":1,"data":[{"b64_json":"YQ=="}],"error":{"message":"secret-value"}}`,
		"trailing_json":       `{"created":1,"data":[{"b64_json":"YQ=="}]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			p, _ := NewOpenAI(server.URL, "")
			got, err := p.(engine.ImageProvider).GenerateImage(context.Background(), basicImageRequest())
			var safe *engine.Error
			if !errors.As(err, &safe) || safe.Status != 502 || strings.Contains(err.Error(), "secret-value") || len(got) != 0 {
				t.Fatalf("expected safe response error, got %s, %v", got, err)
			}
		})
	}
}

func TestOpenAIImageErrorSanitizationAndRedirect(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var redirected atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"secret-value"}}`)
			}))
			defer server.Close()
			p, _ := NewOpenAI(server.URL, "fake-key")
			_, err := p.(engine.ImageProvider).GenerateImage(context.Background(), basicImageRequest())
			var safe *engine.Error
			want := http.StatusBadGateway
			if status == http.StatusTooManyRequests {
				want = status
			}
			if !errors.As(err, &safe) || safe.Status != want || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("expected safe status %d, got %v", want, err)
			}
			if redirected.Load() {
				t.Fatal("image request followed upstream redirect")
			}
		})
	}
}

func TestOpenAIImageCancellation(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(fmt.Sprint(headers), func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if headers {
					fmt.Fprint(w, `{"created":1,"data":[`)
					w.(http.Flusher).Flush()
				}
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			p, _ := NewOpenAI(server.URL, "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				_, err := p.(engine.ImageProvider).GenerateImage(ctx, basicImageRequest())
				finished <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("image request did not start")
			}
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation not preserved: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("image request did not stop on cancellation")
			}
		})
	}
}

func TestOpenAIImageResponseSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // Exercise chunked responses without Content-Length.
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for n := 0; n < 65<<20; n += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	p, _ := NewOpenAI(server.URL, "")
	got, err := p.(engine.ImageProvider).GenerateImage(context.Background(), basicImageRequest())
	var safe *engine.Error
	if !errors.As(err, &safe) || safe.Status != 502 || !strings.Contains(err.Error(), "size limit") || len(got) != 0 {
		t.Fatalf("expected image size-limit error, got %v", err)
	}
}
