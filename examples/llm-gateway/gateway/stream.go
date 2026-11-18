package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// stream writes a finished response as the server-sent events the agent
// asked for. The events carry the same content a provider's stream would,
// in fewer pieces: each block arrives whole.
func stream(w http.ResponseWriter, format string, response []byte, request map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	var body map[string]any
	if err := json.Unmarshal(response, &body); err != nil {
		return
	}
	if format == "anthropic" {
		streamAnthropic(w, body)
	} else {
		streamChat(w, body, request)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func send(w http.ResponseWriter, event string, data any) {
	payload, _ := json.Marshal(data)
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", payload)
}

// streamAnthropic follows the Messages streaming order: message_start, then
// start/delta/stop for each content block, then message_delta, message_stop.
func streamAnthropic(w http.ResponseWriter, msg map[string]any) {
	blocks, _ := msg["content"].([]any)
	start := map[string]any{}
	for k, v := range msg {
		start[k] = v
	}
	start["content"] = []any{}
	start["stop_reason"], start["stop_sequence"] = nil, nil
	send(w, "message_start", map[string]any{"type": "message_start", "message": start})

	for i, b := range blocks {
		block, _ := b.(map[string]any)
		opening, deltas := block, []map[string]any(nil)
		switch block["type"] {
		case "text":
			opening = map[string]any{"type": "text", "text": ""}
			deltas = append(deltas, map[string]any{"type": "text_delta", "text": block["text"]})
		case "tool_use":
			input, _ := json.Marshal(block["input"])
			opening = map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}
			deltas = append(deltas, map[string]any{"type": "input_json_delta", "partial_json": string(input)})
		case "thinking":
			opening = map[string]any{"type": "thinking", "thinking": ""}
			deltas = append(deltas,
				map[string]any{"type": "thinking_delta", "thinking": block["thinking"]},
				map[string]any{"type": "signature_delta", "signature": block["signature"]})
		}
		send(w, "content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": opening})
		for _, d := range deltas {
			send(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": d})
		}
		send(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}

	usage, _ := msg["usage"].(map[string]any)
	send(w, "message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": msg["stop_reason"], "stop_sequence": msg["stop_sequence"]},
		"usage": map[string]any{"output_tokens": usage["output_tokens"]}})
	send(w, "message_stop", map[string]any{"type": "message_stop"})
}

// streamChat writes a Chat Completions stream: one chunk with each choice's
// whole message, one with its finish reason, the usage if asked for, [DONE].
func streamChat(w http.ResponseWriter, completion map[string]any, request map[string]any) {
	chunk := func(choices []any, usage any) map[string]any {
		c := map[string]any{"id": completion["id"], "object": "chat.completion.chunk", "created": completion["created"], "model": completion["model"], "choices": choices}
		if usage != nil {
			c["usage"] = usage
		}
		return c
	}
	var first, last []any
	choices, _ := completion["choices"].([]any)
	for _, ch := range choices {
		choice, _ := ch.(map[string]any)
		msg, _ := choice["message"].(map[string]any)
		delta := map[string]any{"role": "assistant", "content": msg["content"]}
		if calls, ok := msg["tool_calls"].([]any); ok {
			indexed := make([]any, len(calls))
			for i, c := range calls {
				call, _ := c.(map[string]any)
				with := map[string]any{"index": i}
				for k, v := range call {
					with[k] = v
				}
				indexed[i] = with
			}
			delta["tool_calls"] = indexed
		}
		first = append(first, map[string]any{"index": choice["index"], "delta": delta, "finish_reason": nil})
		last = append(last, map[string]any{"index": choice["index"], "delta": map[string]any{}, "finish_reason": choice["finish_reason"]})
	}
	send(w, "", chunk(first, nil))
	send(w, "", chunk(last, nil))
	if opts, _ := request["stream_options"].(map[string]any); opts["include_usage"] == true {
		send(w, "", chunk([]any{}, completion["usage"]))
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}
