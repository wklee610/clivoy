package server

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/engine"
)

type observedEvent struct {
	name string
	data map[string]json.RawMessage
	done bool
}

func readEvents(t *testing.T, body string) []observedEvent {
	t.Helper()
	var events []observedEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	var name string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			name = strings.TrimPrefix(line, "event: ")
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		event := observedEvent{name: name, done: data == "[DONE]"}
		if !event.done {
			if err := json.Unmarshal([]byte(data), &event.data); err != nil {
				t.Fatalf("invalid streaming JSON: %v", err)
			}
		}
		events = append(events, event)
		name = ""
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func decodeEventField[T any](t *testing.T, event observedEvent, key string) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(event.data[key], &result); err != nil {
		t.Fatalf("event %q has invalid %q: %v", event.name, key, err)
	}
	return result
}

func TestNamedStreamsRejectLateToolMetadata(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		for _, field := range []string{"name", "id"} {
			t.Run(path+"/"+field, func(t *testing.T) {
				var emittedError error
				p := &fakeProvider{generate: func(_ context.Context, _ engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
					first := engine.ToolCallDelta{Index: 0, ID: "call_1", Type: "function", Function: engine.FunctionCall{Name: "weather", Arguments: "{"}}
					if err := emit(engine.Delta{ToolCalls: []engine.ToolCallDelta{first}}); err != nil {
						return engine.Result{}, err
					}
					late := engine.ToolCallDelta{Index: 0, Function: engine.FunctionCall{Arguments: "}"}}
					if field == "name" {
						late.Function.Name = "_changed"
					} else {
						late.ID = "_changed"
					}
					emittedError = emit(engine.Delta{ToolCalls: []engine.ToolCallDelta{late}})
					return engine.Result{}, emittedError
				}}
				w := invoke(testServer(t, p, nil), apiRequest(path, bodyFor(path, true)))
				if emittedError == nil {
					t.Fatal("late tool metadata was accepted")
				}
				events := readEvents(t, w.Body.String())
				failure, started := false, false
				for _, event := range events {
					if event.name == "content_block_start" || event.name == "response.output_item.added" {
						started = true
					}
					if event.name == "error" || event.name == "response.failed" {
						failure = true
					}
					if event.name == "message_stop" || event.name == "response.completed" || event.done {
						t.Fatal("late metadata error advertised successful completion")
					}
				}
				if !started || !failure {
					t.Fatalf("expected a started tool followed by a stream error: %s", w.Body.String())
				}
			})
		}
	}
}

func interleavedToolProvider() *fakeProvider {
	return &fakeProvider{generate: func(_ context.Context, _ engine.Request, emit func(engine.Delta) error) (engine.Result, error) {
		// Call index 1 starts before index 0, and argument fragments interleave.
		deltas := []engine.Delta{
			{Text: "Checking."},
			{ToolCalls: []engine.ToolCallDelta{{Index: 1, ID: "call_time", Type: "function", Function: engine.FunctionCall{Name: "time", Arguments: `{"zone":`}}}},
			{ToolCalls: []engine.ToolCallDelta{{Index: 0, ID: "call_weather", Type: "function", Function: engine.FunctionCall{Name: "weather", Arguments: `{"city":`}}}},
			{ToolCalls: []engine.ToolCallDelta{{Index: 1, Function: engine.FunctionCall{Arguments: `"UTC"}`}}}},
			{ToolCalls: []engine.ToolCallDelta{{Index: 0, Function: engine.FunctionCall{Arguments: `"Seoul"}`}}}},
		}
		for _, delta := range deltas {
			if err := emit(delta); err != nil {
				return engine.Result{}, err
			}
		}
		return engine.Result{Text: "Checking.", ToolCalls: []engine.ToolCall{
			{ID: "call_weather", Type: "function", Function: engine.FunctionCall{Name: "weather", Arguments: `{"city":"Seoul"}`}},
			{ID: "call_time", Type: "function", Function: engine.FunctionCall{Name: "time", Arguments: `{"zone":"UTC"}`}},
		}, Usage: engine.Usage{InputTokens: 15, OutputTokens: 20}, FinishReason: "tool_calls"}, nil
	}}
}

func TestParallelToolStreamsPreserveCalls(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			w := invoke(testServer(t, interleavedToolProvider(), nil), apiRequest(path, bodyFor(path, true)))
			if w.Code != 200 {
				t.Fatalf("stream failed: %d %s", w.Code, w.Body.String())
			}
			events := readEvents(t, w.Body.String())
			calls := map[int]*engine.ToolCall{}
			text := ""
			finished := false
			for _, event := range events {
				if len(event.data["error"]) > 0 || event.name == "error" || event.name == "response.failed" {
					t.Fatalf("interleaved stream failed: %s", w.Body.String())
				}
				switch path {
				case "/v1/chat/completions":
					if event.done {
						finished = true
						continue
					}
					var chunk struct {
						Choices []struct {
							Delta struct {
								Content string                 `json:"content"`
								Calls   []engine.ToolCallDelta `json:"tool_calls"`
							} `json:"delta"`
							Finish string `json:"finish_reason"`
						} `json:"choices"`
					}
					data, _ := json.Marshal(event.data)
					if err := json.Unmarshal(data, &chunk); err != nil {
						t.Fatal(err)
					}
					for _, choice := range chunk.Choices {
						text += choice.Delta.Content
						for _, delta := range choice.Delta.Calls {
							call := calls[delta.Index]
							if call == nil {
								call = &engine.ToolCall{}
								calls[delta.Index] = call
							}
							call.ID += delta.ID
							call.Function.Name += delta.Function.Name
							call.Function.Arguments += delta.Function.Arguments
						}
					}
				case "/v1/messages":
					switch event.name {
					case "content_block_start":
						index := decodeEventField[int](t, event, "index")
						block := decodeEventField[struct{ Type, ID, Name string }](t, event, "content_block")
						if block.Type == "tool_use" {
							calls[index] = &engine.ToolCall{ID: block.ID, Function: engine.FunctionCall{Name: block.Name}}
						}
					case "content_block_delta":
						index := decodeEventField[int](t, event, "index")
						delta := decodeEventField[struct {
							Type, Text string
							Partial    string `json:"partial_json"`
						}](t, event, "delta")
						if delta.Type == "text_delta" {
							text += delta.Text
						} else if delta.Type == "input_json_delta" {
							call := calls[index]
							if call == nil {
								t.Fatal("tool delta arrived before its start")
							}
							call.Function.Arguments += delta.Partial
						}
					case "message_stop":
						finished = true
					}
				case "/v1/responses":
					switch event.name {
					case "response.output_item.added":
						index := decodeEventField[int](t, event, "output_index")
						item := decodeEventField[struct {
							Type, Name string
							CallID     string `json:"call_id"`
						}](t, event, "item")
						if item.Type == "function_call" {
							calls[index] = &engine.ToolCall{ID: item.CallID, Function: engine.FunctionCall{Name: item.Name}}
						}
					case "response.output_text.delta":
						text += decodeEventField[string](t, event, "delta")
					case "response.function_call_arguments.delta":
						index := decodeEventField[int](t, event, "output_index")
						call := calls[index]
						if call == nil {
							t.Fatal("tool delta arrived before its start")
						}
						call.Function.Arguments += decodeEventField[string](t, event, "delta")
					case "response.completed":
						finished = true
						response := decodeEventField[struct {
							Output []struct {
								Type, Name, Arguments string
								CallID                string `json:"call_id"`
							}
						}](t, event, "response")
						for index, item := range response.Output {
							if item.Type != "function_call" {
								continue
							}
							call := calls[index]
							if call == nil || call.ID != item.CallID || call.Function.Name != item.Name || call.Function.Arguments != item.Arguments {
								t.Fatal("final response output order/content disagreed with streamed output indexes")
							}
						}
					}
				}
			}
			if !finished || text != "Checking." || len(calls) != 2 {
				t.Fatalf("incomplete stream: finish=%v text=%q calls=%+v", finished, text, calls)
			}
			byID := map[string]engine.FunctionCall{}
			for _, call := range calls {
				byID[call.ID] = call.Function
			}
			for id, want := range map[string]engine.FunctionCall{"call_weather": {Name: "weather", Arguments: `{"city":"Seoul"}`}, "call_time": {Name: "time", Arguments: `{"zone":"UTC"}`}} {
				if got := byID[id]; got != want {
					t.Fatalf("call %s: got %+v, want %+v", id, got, want)
				}
			}
		})
	}
}
