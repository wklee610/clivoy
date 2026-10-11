package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAnthropicRejectsMissingTextAndMixedBlockFields(t *testing.T) {
	for _, tc := range []struct {
		name, role, block string
	}{
		{"text_missing", "user", `{"type":"text"}`},
		{"text_null", "user", `{"type":"text","text":null}`},
		{"text_source_null", "user", `{"type":"text","text":"hello","source":null}`},
		{"text_input_null", "user", `{"type":"text","text":"hello","input":null}`},
		{"text_is_error_false", "user", `{"type":"text","text":"hello","is_error":false}`},
		{"image_text", "user", `{"type":"image","text":"do not drop this","source":{"type":"url","url":"https://example.invalid/a.png"}}`},
		{"image_text_null", "user", `{"type":"image","text":null,"source":{"type":"url","url":"https://example.invalid/a.png"}}`},
		{"image_tool_id_empty", "user", `{"type":"image","tool_use_id":"","source":{"type":"url","url":"https://example.invalid/a.png"}}`},
		{"image_citations_null", "user", `{"type":"image","citations":null,"source":{"type":"url","url":"https://example.invalid/a.png"}}`},
		{"image_source_missing", "user", `{"type":"image"}`},
		{"image_source_null", "user", `{"type":"image","source":null}`},
		{"url_source_data_null", "user", `{"type":"image","source":{"type":"url","url":"https://example.invalid/a.png","data":null}}`},
		{"url_source_media_empty", "user", `{"type":"image","source":{"type":"url","url":"https://example.invalid/a.png","media_type":""}}`},
		{"base64_source_url_null", "user", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + samplePNG + `","url":null}}`},
		{"tool_use_text_null", "assistant", `{"type":"tool_use","id":"call_1","name":"lookup","input":{},"text":null}`},
		{"tool_use_source_null", "assistant", `{"type":"tool_use","id":"call_1","name":"lookup","input":{},"source":null}`},
		{"tool_use_content_null", "assistant", `{"type":"tool_use","id":"call_1","name":"lookup","input":{},"content":null}`},
		{"tool_result_input_null", "user", `{"type":"tool_result","tool_use_id":"call_1","content":"ok","input":null}`},
		{"tool_result_source_null", "user", `{"type":"tool_result","tool_use_id":"call_1","content":"ok","source":null}`},
		{"tool_result_nested_text_missing", "user", `{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text"}]}`},
		{"tool_result_nested_text_null", "user", `{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":null}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &imageTestProvider{}
			body := `{"model":"vision","max_tokens":10,"messages":[{"role":"` + tc.role + `","content":[` + tc.block + `]}]}`
			w := imageInvoke(imageServer(t, p), "/v1/messages", body)
			if w.Code != http.StatusBadRequest || p.chatCalls != 0 {
				t.Fatalf("malformed block reached provider: %d %s", w.Code, w.Body)
			}
		})
	}
	for _, system := range []string{`[{"type":"text"}]`, `[{"type":"text","text":null}]`, `[{"type":"text","text":"hello","source":null}]`} {
		p := &imageTestProvider{}
		body := `{"model":"vision","max_tokens":10,"system":` + system + `,"messages":[{"role":"user","content":"hello"}]}`
		w := imageInvoke(imageServer(t, p), "/v1/messages", body)
		if w.Code != http.StatusBadRequest || p.chatCalls != 0 {
			t.Fatalf("malformed system block reached provider: %d %s", w.Code, w.Body)
		}
	}
}

func TestAnthropicValidBlockVariantsRemainAccepted(t *testing.T) {
	for _, tc := range []struct{ role, block string }{
		{"user", `{"type":"text","text":""}`},
		{"assistant", `{"type":"text","text":"Earlier answer","citations":[]}`},
		{"user", `{"type":"image","source":{"type":"url","url":"https://example.invalid/a.png"}}`},
		{"user", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + samplePNG + `"}}`},
		{"assistant", `{"type":"tool_use","id":"call_1","name":"lookup","input":{"query":"hello"}}`},
		{"user", `{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"found","citations":null}],"is_error":false}`},
		{"user", `{"type":"tool_result","tool_use_id":"call_1"}`},
	} {
		p := &imageTestProvider{}
		body := `{"model":"vision","max_tokens":10,"messages":[{"role":"` + tc.role + `","content":[` + tc.block + `]}]}`
		w := imageInvoke(imageServer(t, p), "/v1/messages", body)
		if w.Code != http.StatusOK || p.chatCalls != 1 {
			t.Fatalf("valid block rejected (%s): %d %s", tc.block, w.Code, w.Body)
		}
	}
}

func TestResponsesRejectsMixedContentFields(t *testing.T) {
	for _, block := range []string{
		`{"type":"input_text","text":"hello","detail":"high"}`,
		`{"type":"input_text","text":"hello","detail":null}`,
		`{"type":"input_text","text":"hello","annotations":[]}`,
		`{"type":"input_text","text":"hello","logprobs":null}`,
		`{"type":"input_text","text":"hello","image_url":null}`,
		`{"type":"output_text","text":"hello","detail":"low"}`,
		`{"type":"output_text","text":"hello","file_id":null}`,
		`{"type":"input_image","image_url":"https://example.invalid/a.png","text":null}`,
		`{"type":"input_image","image_url":"https://example.invalid/a.png","annotations":[]}`,
		`{"type":"input_image","image_url":"https://example.invalid/a.png","logprobs":null}`,
		`{"type":"input_text"}`,
		`{"type":"output_text","text":null}`,
	} {
		p := &imageTestProvider{}
		body := `{"model":"vision","input":[{"role":"user","content":[` + block + `]}]}`
		w := imageInvoke(imageServer(t, p), "/v1/responses", body)
		if w.Code != http.StatusBadRequest || p.chatCalls != 0 {
			t.Fatalf("malformed Responses block reached provider (%s): %d %s", block, w.Code, w.Body)
		}
	}
}

func TestResponsesOutputTextHistoryPreservesAnswer(t *testing.T) {
	p := &imageTestProvider{}
	body := `{"model":"vision","input":[{"role":"assistant","content":[{"type":"output_text","text":"Earlier answer","annotations":[],"logprobs":[]}]},{"role":"user","content":[{"type":"input_text","text":"Explain"}]}]}`
	w := imageInvoke(imageServer(t, p), "/v1/responses", body)
	if w.Code != http.StatusOK || len(p.chat.Messages) != 2 {
		t.Fatalf("output history rejected: %d %s", w.Code, w.Body)
	}
	var answer string
	if err := json.Unmarshal(p.chat.Messages[0].Content, &answer); err != nil || answer != "Earlier answer" {
		t.Fatalf("output history changed: %q, %v", answer, err)
	}
}
