package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

const inputTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jD1sAAAAASUVORK5CYII="

func imageRequest() engine.Request {
	r := basicRequest()
	r.Messages[0].Content, _ = json.Marshal([]engine.ContentPart{{Type: "text", Text: "What is in this image?"}, {Type: "image_url", ImageURL: &engine.ImageURL{URL: "data:image/png;base64," + inputTestPNG}}})
	return r
}

func TestCodexImageFilesAndCleanup(t *testing.T) {
	p := fakeCLI(t, "codex", "capture").(*cliProvider)
	args, input, cleanup, err := p.prepareInput(imageRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var attachment string
	for i, arg := range args {
		if arg == "--image" {
			attachment = args[i+1]
		}
	}
	if attachment == "" || !strings.Contains(input, "[Attached image 1]") || strings.Contains(input, inputTestPNG) {
		t.Fatal("image was not represented by an attachment and transcript marker")
	}
	data, err := os.ReadFile(attachment)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := base64.StdEncoding.DecodeString(inputTestPNG)
	if !bytes.Equal(data, want) {
		t.Fatal("temporary image bytes changed")
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != "-" {
		t.Fatal("stdin marker could be consumed as an image path")
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(attachment)); !os.IsNotExist(err) {
		t.Fatal("attachment directory was not removed")
	}
	result, err := p.Generate(context.Background(), imageRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Args   []string `json:"args"`
		Images []struct {
			Path string `json:"path"`
			Data string `json:"data"`
		} `json:"images"`
	}
	if err := json.Unmarshal([]byte(result.Text), &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Images) != 1 || capture.Images[0].Data != inputTestPNG {
		t.Fatal("fake CLI did not receive the attached image")
	}
	if _, err := os.Stat(capture.Images[0].Path); !os.IsNotExist(err) {
		t.Fatal("successful Generate left an image file")
	}
}

func TestCLIRuntimeFilesUseTemporaryDirectoriesOnDemand(t *testing.T) {
	for _, mode := range []string{"os-default", "custom"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CLIVOY_TEST_PROVIDER_CHILD", "capture")
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			tempRoot, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var p engine.Provider
			if mode == "os-default" {
				for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
					t.Setenv(name, tempRoot)
				}
				p, err = NewCLI("codex", []string{binary}, "")
			} else {
				p, err = NewCLIWithTempDir("codex", []string{binary}, "", tempRoot)
			}
			if err != nil {
				t.Fatal(err)
			}
			if entries, err := os.ReadDir(tempRoot); err != nil || len(entries) != 0 {
				t.Fatalf("provider created runtime files before a request: %v, %v", entries, err)
			}
			result, err := p.Generate(context.Background(), imageRequest(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var capture struct {
				CWD    string `json:"cwd"`
				Images []struct {
					Path string `json:"path"`
				} `json:"images"`
			}
			if err := json.Unmarshal([]byte(result.Text), &capture); err != nil {
				t.Fatal(err)
			}
			if len(capture.Images) != 1 {
				t.Fatal("missing image attachment")
			}
			for _, path := range []string{capture.CWD, capture.Images[0].Path} {
				rel, err := filepath.Rel(tempRoot, path)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
					t.Fatalf("CLI runtime file escaped temporary directory: %s", path)
				}
			}
			if entries, err := os.ReadDir(tempRoot); err != nil || len(entries) != 0 {
				t.Fatalf("temporary files remained after execution: %v, %v", entries, err)
			}
		})
	}
}

func TestClaudeImageInputUsesUserStreamJSON(t *testing.T) {
	p := fakeCLI(t, "claude", "capture_claude")
	result, err := p.Generate(context.Background(), imageRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Args  []string `json:"args"`
		Stdin string   `json:"stdin"`
	}
	if err = json.Unmarshal([]byte(result.Text), &capture); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(capture.Args, " "), "--input-format stream-json") {
		t.Fatal("Claude image input did not use stream-json")
	}
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type  string `json:"type"`
					Media string `json:"media_type"`
					Data  string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err = json.Unmarshal([]byte(capture.Stdin), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "user" || event.Message.Role != "user" {
		t.Fatal("invalid Claude user-message envelope")
	}
	images := 0
	for _, block := range event.Message.Content {
		if block.Type == "image" {
			images++
			if block.Source.Type != "base64" || block.Source.Media != "image/png" || block.Source.Data != inputTestPNG {
				t.Fatal("Claude image block changed")
			}
		} else if strings.Contains(block.Text, inputTestPNG) {
			t.Fatal("base64 data leaked into text prompt")
		}
	}
	if images != 1 {
		t.Fatalf("expected one image block, got %d", images)
	}
}

func TestCodexImagesRemovedOnFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"image_failure", "image_block"} {
		t.Run(mode, func(t *testing.T) {
			p := fakeCLI(t, "codex", mode)
			pathFile := filepath.Join(t.TempDir(), "attachment-path")
			t.Setenv("CLIVOY_TEST_IMAGE_PATH", pathFile)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := p.Generate(ctx, imageRequest(), nil); done <- err }()
			var path string
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				data, _ := os.ReadFile(pathFile)
				if len(data) > 0 {
					path = string(data)
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if path == "" {
				t.Fatal("fake CLI did not report its image path")
			}
			if mode == "image_block" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected failure")
				}
				if mode == "image_block" && !errors.Is(err, context.Canceled) {
					t.Fatalf("unexpected cancel error: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Generate did not finish")
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatal("failed/cancelled request left image files")
			}
		})
	}
}

func TestCLIImageOrderMatchesConversationMarkers(t *testing.T) {
	const gif = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"
	imagePart := func(media, data string) engine.ContentPart {
		return engine.ContentPart{Type: "image_url", ImageURL: &engine.ImageURL{URL: "data:" + media + ";base64," + data}}
	}
	first, _ := json.Marshal([]engine.ContentPart{{Type: "text", Text: "First:"}, imagePart("image/png", inputTestPNG), {Type: "text", Text: "then"}, imagePart("image/gif", gif)})
	last, _ := json.Marshal([]engine.ContentPart{imagePart("image/png", inputTestPNG), {Type: "text", Text: "Compare these."}})
	req := basicRequest()
	req.Messages = []engine.Message{{Role: "user", Content: first}, {Role: "assistant", Content: json.RawMessage(`"Earlier answer"`)}, {Role: "user", Content: last}}
	wantImages := []string{inputTestPNG, gif, inputTestPNG}
	type turn struct{ Role, Content string }
	wantTurns := []turn{{"user", "First:\n[Attached image 1]\nthen\n[Attached image 2]\n"}, {"assistant", "Earlier answer"}, {"user", "\n[Attached image 3]\nCompare these."}}
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			p := &cliProvider{kind: kind}
			args, input, cleanup, err := p.prepareInput(req)
			defer cleanup()
			if err != nil {
				t.Fatal(err)
			}
			var gotImages []string
			if kind == "codex" {
				for i, arg := range args {
					if arg == "--image" {
						data, err := os.ReadFile(args[i+1])
						if err != nil {
							t.Fatal(err)
						}
						gotImages = append(gotImages, base64.StdEncoding.EncodeToString(data))
					}
				}
			} else {
				var event struct {
					Message struct {
						Content []struct {
							Type, Text string
							Source     struct{ Data string }
						}
					}
				}
				if err := json.Unmarshal([]byte(input), &event); err != nil {
					t.Fatal(err)
				}
				input = event.Message.Content[0].Text
				for _, block := range event.Message.Content {
					if block.Type == "image" {
						gotImages = append(gotImages, block.Source.Data)
					}
				}
			}
			if !reflect.DeepEqual(gotImages, wantImages) {
				t.Fatal("attachments changed order across messages or content blocks")
			}
			var gotTurns []turn
			if err := json.Unmarshal([]byte(input[strings.LastIndex(input, "\n")+1:]), &gotTurns); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotTurns, wantTurns) {
				t.Fatalf("transcript order or image markers changed: %+v", gotTurns)
			}
		})
	}
}

func TestCodexImagesRemovedOnEarlyAndCallbackErrors(t *testing.T) {
	for _, mode := range []string{"prepare", "start", "callback"} {
		t.Run(mode, func(t *testing.T) {
			tempRoot := t.TempDir()
			for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(name, tempRoot)
			}
			p := fakeCLI(t, "codex", "codex").(*cliProvider)
			req := imageRequest()
			var err error
			switch mode {
			case "prepare":
				parts, _ := engine.ParseContent(req.Messages[0].Content)
				parts = append(parts, engine.ContentPart{Type: "image_url", ImageURL: &engine.ImageURL{URL: "data:image/png;base64,invalid"}})
				req.Messages[0].Content, _ = json.Marshal(parts)
				_, _, cleanup, prepareErr := p.prepareInput(req)
				cleanup()
				err = prepareErr
			case "start":
				p.workdir = filepath.Join(tempRoot, "missing-working-directory")
				_, err = p.Generate(context.Background(), req, nil)
			case "callback":
				callbackErr := errors.New("downstream disconnected")
				_, err = p.Generate(context.Background(), req, func(engine.Delta) error { return callbackErr })
				if !errors.Is(err, callbackErr) {
					t.Fatalf("callback error not preserved: %v", err)
				}
			}
			if err == nil {
				t.Fatal("expected failure")
			}
			entries, readErr := os.ReadDir(tempRoot)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("error left temporary image files or working directory: %v, %v", entries, readErr)
			}
		})
	}
}
