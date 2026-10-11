package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

type streamTool struct {
	Call    engine.ToolCall
	Index   int
	Started bool
	Sent    int
	name    strings.Builder
	id      strings.Builder
	args    strings.Builder
}
type streamWriter struct {
	w         http.ResponseWriter
	ctx       context.Context
	p         parsedRequest
	id        string
	created   int64
	sequence  int
	textIndex int
	nextIndex int
	text      strings.Builder
	tools     map[int]*streamTool
}

func (s *Server) stream(ctx context.Context, w http.ResponseWriter, p parsedRequest, provider engine.Provider, req engine.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	sw := &streamWriter{w: w, ctx: ctx, p: p, id: newID(p.idPrefix()), created: time.Now().Unix(), textIndex: -1, tools: map[int]*streamTool{}}
	if err := sw.begin(); err != nil {
		return
	}
	r, err := provider.Generate(ctx, req, sw.emit)
	if err == nil {
		err = validateResult(r)
	}
	if err == nil {
		err = sw.reconcile(r)
	}
	if err != nil {
		sw.fail(err)
		return
	}
	if err = sw.finish(r); err != nil {
		return
	}
}

func (s *streamWriter) send(event string, payload any) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	controller := http.NewResponseController(s.w)
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := s.ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = controller.SetWriteDeadline(deadline)
	var b []byte
	if str, ok := payload.(string); ok {
		b = []byte(str)
	} else {
		var err error
		b, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	if event != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	return controller.Flush()
}

func (s *streamWriter) named(name string, fields map[string]any) error {
	fields["type"] = name
	if s.p.Protocol == "responses" {
		fields["sequence_number"] = s.sequence
		s.sequence++
	}
	return s.send(name, fields)
}

func (s *streamWriter) chat(delta any, finish any, usage any) error {
	choices := []any{}
	if delta != nil {
		choices = append(choices, map[string]any{"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil})
	}
	v := map[string]any{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.p.Request.Model, "choices": choices}
	if s.p.IncludeUsage {
		v["usage"] = usage
	}
	return s.send("", v)
}

func (s *streamWriter) begin() error {
	switch s.p.Protocol {
	case "anthropic":
		m := s.p.response(engine.Result{}, s.id, "in_progress")
		m["content"] = []any{}
		m["stop_reason"] = nil
		return s.named("message_start", map[string]any{"message": m})
	case "responses":
		r := s.p.response(engine.Result{}, s.id, "in_progress")
		r["usage"] = nil
		r["created_at"] = s.created
		if err := s.named("response.created", map[string]any{"response": r}); err != nil {
			return err
		}
		return s.named("response.in_progress", map[string]any{"response": r})
	default:
		return s.chat(map[string]any{"role": "assistant", "content": ""}, nil, nil)
	}
}

func (s *streamWriter) startText() error {
	if s.textIndex >= 0 {
		return nil
	}
	s.textIndex = s.nextIndex
	s.nextIndex++
	switch s.p.Protocol {
	case "anthropic":
		return s.named("content_block_start", map[string]any{"index": s.textIndex, "content_block": map[string]any{"type": "text", "text": ""}})
	case "responses":
		item := responseMessage(s.id, "", "in_progress")
		item["content"] = []any{}
		if err := s.named("response.output_item.added", map[string]any{"output_index": s.textIndex, "item": item}); err != nil {
			return err
		}
		return s.named("response.content_part.added", map[string]any{"item_id": "msg_" + s.id, "output_index": s.textIndex, "content_index": 0, "part": responseText("")})
	}
	return nil
}

func (s *streamWriter) emit(d engine.Delta) error {
	if d.Text != "" {
		if err := s.startText(); err != nil {
			return err
		}
		s.text.WriteString(d.Text)
		var err error
		switch s.p.Protocol {
		case "anthropic":
			err = s.named("content_block_delta", map[string]any{"index": s.textIndex, "delta": map[string]any{"type": "text_delta", "text": d.Text}})
		case "responses":
			err = s.named("response.output_text.delta", map[string]any{"item_id": "msg_" + s.id, "output_index": s.textIndex, "content_index": 0, "delta": d.Text, "logprobs": []any{}})
		default:
			err = s.chat(map[string]any{"content": d.Text}, nil, nil)
		}
		if err != nil {
			return err
		}
	}
	for _, delta := range d.ToolCalls {
		if delta.Index < 0 || delta.Index >= 128 {
			return &engine.Error{Status: 502, Type: "api_error", Message: "Provider returned an invalid tool index"}
		}
		t, ok := s.tools[delta.Index]
		if !ok {
			t = &streamTool{Index: -1, Call: engine.ToolCall{Type: "function"}}
			s.tools[delta.Index] = t
		}
		if t.Started && (delta.ID != "" || delta.Function.Name != "") {
			return &engine.Error{Status: 502, Type: "api_error", Message: "Provider changed a tool identifier after streaming its arguments"}
		}
		t.id.WriteString(delta.ID)
		t.name.WriteString(delta.Function.Name)
		t.args.WriteString(delta.Function.Arguments)
		t.Call.ID = t.id.String()
		t.Call.Function.Name = t.name.String()
		t.Call.Function.Arguments = t.args.String()
		if s.p.Protocol == "chat" {
			if err := s.chat(map[string]any{"tool_calls": []engine.ToolCallDelta{delta}}, nil, nil); err != nil {
				return err
			}
			continue
		}
		if !t.Started && t.Call.ID != "" && t.Call.Function.Name != "" && t.Call.Function.Arguments != "" {
			t.Index = s.nextIndex
			s.nextIndex++
			t.Started = true
			var err error
			if s.p.Protocol == "anthropic" {
				err = s.named("content_block_start", map[string]any{"index": t.Index, "content_block": map[string]any{"type": "tool_use", "id": t.Call.ID, "name": t.Call.Function.Name, "input": map[string]any{}}})
			} else {
				item := responseTool(s.id, delta.Index, t.Call)
				item["arguments"] = ""
				item["status"] = "in_progress"
				err = s.named("response.output_item.added", map[string]any{"output_index": t.Index, "item": item})
			}
			if err != nil {
				return err
			}
		}
		if t.Started && len(t.Call.Function.Arguments) > t.Sent {
			fragment := t.Call.Function.Arguments[t.Sent:]
			t.Sent = len(t.Call.Function.Arguments)
			var err error
			if s.p.Protocol == "anthropic" {
				err = s.named("content_block_delta", map[string]any{"index": t.Index, "delta": map[string]any{"type": "input_json_delta", "partial_json": fragment}})
			} else {
				err = s.named("response.function_call_arguments.delta", map[string]any{"item_id": fmt.Sprintf("fc_%s_%d", s.id, delta.Index), "output_index": t.Index, "delta": fragment})
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Complete providers that return final-only output, and detect inconsistent deltas.
func (s *streamWriter) reconcile(r engine.Result) error {
	if !strings.HasPrefix(r.Text, s.text.String()) {
		return &engine.Error{Status: 502, Type: "api_error", Message: "Provider returned inconsistent text deltas"}
	}
	if err := s.emit(engine.Delta{Text: strings.TrimPrefix(r.Text, s.text.String())}); err != nil {
		return err
	}
	for i, call := range r.ToolCalls {
		t, ok := s.tools[i]
		if !ok {
			if err := s.emit(engine.Delta{ToolCalls: []engine.ToolCallDelta{{Index: i, ID: call.ID, Type: "function", Function: call.Function}}}); err != nil {
				return err
			}
			continue
		}
		if t.Call.ID != call.ID || t.Call.Function.Name != call.Function.Name || !strings.HasPrefix(call.Function.Arguments, t.Call.Function.Arguments) {
			return &engine.Error{Status: 502, Type: "api_error", Message: "Provider returned inconsistent tool deltas"}
		}
		if err := s.emit(engine.Delta{ToolCalls: []engine.ToolCallDelta{{Index: i, Function: engine.FunctionCall{Arguments: strings.TrimPrefix(call.Function.Arguments, t.Call.Function.Arguments)}}}}); err != nil {
			return err
		}
	}
	if len(s.tools) != len(r.ToolCalls) {
		return &engine.Error{Status: 502, Type: "api_error", Message: "Provider returned incomplete tool calls"}
	}
	if s.textIndex < 0 && len(r.ToolCalls) == 0 {
		return s.startText()
	}
	return nil
}

func (s *streamWriter) finish(r engine.Result) error {
	if s.p.Protocol == "chat" {
		finish := r.FinishReason
		if finish == "" {
			finish = "stop"
		}
		if len(r.ToolCalls) > 0 {
			finish = "tool_calls"
		}
		if err := s.chat(map[string]any{}, finish, nil); err != nil {
			return err
		}
		if s.p.IncludeUsage {
			if err := s.chat(nil, nil, chatUsage(r.Usage)); err != nil {
				return err
			}
		}
		return s.send("", "[DONE]")
	}
	if s.p.Protocol == "anthropic" {
		for i := 0; i < s.nextIndex; i++ {
			if err := s.named("content_block_stop", map[string]any{"index": i}); err != nil {
				return err
			}
		}
		if err := s.named("message_delta", map[string]any{"delta": map[string]any{"stop_reason": anthropicStop(r), "stop_sequence": nil}, "usage": r.Usage}); err != nil {
			return err
		}
		return s.named("message_stop", map[string]any{})
	}
	status := "completed"
	if r.FinishReason == "length" {
		status = "incomplete"
	}
	output := make([]any, s.nextIndex)
	if s.textIndex >= 0 {
		fields := map[string]any{"item_id": "msg_" + s.id, "output_index": s.textIndex, "content_index": 0, "text": r.Text, "logprobs": []any{}}
		if err := s.named("response.output_text.done", fields); err != nil {
			return err
		}
		if err := s.named("response.content_part.done", map[string]any{"item_id": "msg_" + s.id, "output_index": s.textIndex, "content_index": 0, "part": responseText(r.Text)}); err != nil {
			return err
		}
		item := responseMessage(s.id, r.Text, status)
		output[s.textIndex] = item
		if err := s.named("response.output_item.done", map[string]any{"output_index": s.textIndex, "item": item}); err != nil {
			return err
		}
	}
	keys := make([]int, 0, len(s.tools))
	for k := range s.tools {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		t := s.tools[k]
		item := responseTool(s.id, k, t.Call)
		output[t.Index] = item
		if err := s.named("response.function_call_arguments.done", map[string]any{"item_id": item["id"], "output_index": t.Index, "arguments": t.Call.Function.Arguments, "name": t.Call.Function.Name}); err != nil {
			return err
		}
		if err := s.named("response.output_item.done", map[string]any{"output_index": t.Index, "item": item}); err != nil {
			return err
		}
	}
	response := s.p.response(r, s.id, status)
	response["output"] = output
	response["created_at"] = s.created
	return s.named("response."+status, map[string]any{"response": response})
}

func (s *streamWriter) fail(err error) {
	e := clientError(err)
	// The request deadline may have elapsed. Permit only a bounded final error write.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), time.Second)
	defer cancel()
	s.ctx = ctx
	switch s.p.Protocol {
	case "anthropic":
		_ = s.named("error", map[string]any{"error": map[string]any{"type": anthropicErrorType(e), "message": e.Message}})
	case "responses":
		r := s.p.response(engine.Result{}, s.id, "failed")
		code := "server_error"
		if e.Status == 429 {
			code = "rate_limit_exceeded"
		}
		r["error"] = map[string]any{"code": code, "message": e.Message}
		r["output"] = []any{}
		_ = s.named("response.failed", map[string]any{"response": r})
	default:
		_ = s.send("", map[string]any{"error": map[string]any{"type": e.Type, "code": e.Type, "message": e.Message}})
	}
}
