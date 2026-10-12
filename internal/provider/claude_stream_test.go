package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
)

func TestClaudeStreamMessageBoundaries(t *testing.T) {
	const start = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"message_start"}}`
	const stop = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"message_stop"}}`
	const checking = `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"확인 중. "}}}`
	const partial = `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Answer: "}}}`
	const complete = `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Answer: 42."}}}`
	const result = `{"type":"result","subtype":"success","result":"Answer: 42.","usage":{"input_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":3,"output_tokens":4}}`

	for _, tc := range []struct {
		name   string
		events []string
		want   string
	}{
		{
			name:   "legacy deltas without message boundaries",
			events: []string{partial, result},
			want:   "Answer: 42.",
		},
		{
			name:   "single message missing suffix",
			events: []string{start, partial, stop, result},
			want:   "Answer: 42.",
		},
		{
			name: "tool loop final result duplicates only last message",
			events: []string{
				start, checking,
				`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash"}}}`,
				`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo 42\"}"}}}`,
				`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"}}}`,
				stop,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"확인 중. "}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","content":"42"}]}}`,
				start, complete, stop,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"Answer: 42."}]}}`,
				result,
			},
			want: "확인 중. Answer: 42.",
		},
		{
			name:   "tool loop final result supplies missing suffix",
			events: []string{start, checking, stop, start, partial, stop, result},
			want:   "확인 중. Answer: 42.",
		},
		{
			name:   "final message has no text deltas",
			events: []string{start, checking, stop, start, stop, result},
			want:   "확인 중. Answer: 42.",
		},
		{
			name: "subagent boundaries and text do not affect parent message",
			events: []string{
				start, checking, stop, start, partial,
				`{"type":"stream_event","parent_tool_use_id":"tool-1","event":{"type":"message_start"}}`,
				`{"type":"stream_event","parent_tool_use_id":"tool-1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Private subagent output"}}}`,
				`{"type":"stream_event","parent_tool_use_id":"tool-1","event":{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}}`,
				`{"type":"stream_event","parent_tool_use_id":"tool-1","event":{"type":"message_stop"}}`,
				`{"type":"result","parent_tool_use_id":"tool-1","subtype":"success","result":"Private subagent output"}`,
				stop, result,
			},
			want: "확인 중. Answer: 42.",
		},
		{
			name:   "result without streamed messages",
			events: []string{result},
			want:   "Answer: 42.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				var emitted strings.Builder
				parser := cliParser{kind: "claude"}
				if streaming {
					parser.emit = func(d engine.Delta) error {
						emitted.WriteString(d.Text)
						return nil
					}
				}
				for i, event := range tc.events {
					if err := parser.line([]byte(event)); err != nil {
						t.Fatalf("streaming=%v event %d: %v", streaming, i, err)
					}
				}
				if !parser.done || parser.result.Text != tc.want || parser.result.FinishReason != "stop" {
					t.Fatalf("streaming=%v got result=%+v done=%v; want text=%q", streaming, parser.result, parser.done, tc.want)
				}
				if parser.result.Usage.InputTokens != 15 || parser.result.Usage.OutputTokens != 4 {
					t.Fatalf("unexpected result usage: %+v", parser.result.Usage)
				}
				if streaming && emitted.String() != tc.want {
					t.Fatalf("emitted %q, want %q", emitted.String(), tc.want)
				}
			}
		})
	}
}

func TestClaudeStreamRejectsMismatchedFinalMessage(t *testing.T) {
	const start = `{"type":"stream_event","event":{"type":"message_start"}}`
	const text = `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Answer: 42."}}}`
	for _, tc := range []struct {
		name   string
		events []string
		final  string
		want   string
	}{
		{
			name:   "legacy single message",
			events: []string{text},
			final:  `{"type":"result","subtype":"success","result":"Different answer."}`,
			want:   "Answer: 42.",
		},
		{
			name:   "last message mismatch after tool loop",
			events: []string{start, text, start, text},
			final:  `{"type":"result","subtype":"success","result":"Answer: 43."}`,
			want:   "Answer: 42.Answer: 42.",
		},
		{
			name: "combined transcript is not last message",
			events: []string{
				start,
				`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Checking. "}}}`,
				start, text,
			},
			final: `{"type":"result","subtype":"success","result":"Checking. Answer: 42."}`,
			want:  "Checking. Answer: 42.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var emitted strings.Builder
			parser := cliParser{kind: "claude", emit: func(d engine.Delta) error {
				emitted.WriteString(d.Text)
				return nil
			}}
			for _, event := range tc.events {
				if err := parser.line([]byte(event)); err != nil {
					t.Fatal(err)
				}
			}
			var safe *engine.Error
			err := parser.line([]byte(tc.final))
			if !errors.As(err, &safe) || safe.Status != 502 || safe.Message != "Claude CLI final output did not match the streamed response" {
				t.Fatalf("expected output mismatch, got %v", err)
			}
			if parser.done || parser.result.Text != tc.want || emitted.String() != tc.want {
				t.Fatalf("mismatched final changed response: result=%+v done=%v emitted=%q", parser.result, parser.done, emitted.String())
			}
		})
	}
}
