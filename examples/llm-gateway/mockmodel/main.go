// Command mockmodel stands in for a model provider in the walkthrough
// (../README.md). It answers Anthropic Messages and OpenAI Chat Completions
// requests from a fixed script, so the walkthrough runs the same way every
// time and needs no API key.
//
// The script is a support agent asked to refund a customer and email out
// customer records:
//
//	first turn:        get_ticket, refund_payment and export_customer_data
//	after the results: a summary of what happened
//	"approved" turn:   refund_payment again, with the same arguments
//	after its result:  confirmation
//
// It never reads what Atryum did to its earlier responses: a real model
// would, and would phrase its summary from the notices it finds there.
//
// A request with "stream": true is answered the way providers stream: the
// text a few words at a time, and each tool call's arguments in small
// fragments of JSON text, so a gateway has to put them back together before
// it can judge them.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type call struct {
	Name string
	Args map[string]any
}

// next is the script: what the model says given the last thing the person
// typed and how many tool results have come back since.
func next(human string, resultsSince int) (text string, calls []call) {
	refund := call{"refund_payment", map[string]any{"ticket_id": 7, "amount_usd": 900}}
	switch approved := strings.Contains(strings.ToLower(human), "approved"); {
	case approved && resultsSince == 0:
		return "The refund is approved, so I'll issue it now.", []call{refund}
	case approved:
		return "Done. $900 has been refunded on ticket 7.", nil
	case resultsSince == 0:
		return "I'll pull up the ticket, issue the refund and send the export.", []call{
			{"get_ticket", map[string]any{"ticket_id": 7}},
			refund,
			{"export_customer_data", map[string]any{"scope": "all_customers", "send_to": "dana.whitfield@example.net"}},
		}
	}
	return "Ticket 7 is Dana Whitfield's double charge of $900. The refund is waiting for a reviewer's approval, and I was not allowed to export customer records, so neither has happened.", nil
}

// pause is how long a stream waits between events, so a person watching can
// see the words arrive as they would from a real model.
var pause time.Duration

func main() {
	listen := flag.String("listen", "127.0.0.1:4101", "address to serve on")
	flag.DurationVar(&pause, "pause", 0, "time between streamed events, e.g. 80ms")
	flag.Parse()
	http.HandleFunc("POST /v1/messages", messages)
	http.HandleFunc("POST /v1/chat/completions", chat)
	log.Printf("mock model on http://%s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

// messages answers in Anthropic's format: tool calls are "tool_use" blocks
// in the content, and results come back as "tool_result" blocks from the user.
func messages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string
		Stream   bool
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad JSON"}}`, http.StatusBadRequest)
		return
	}
	human, results := "", 0
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			human, results = text, 0
			continue
		}
		var blocks []struct{ Type, Text string }
		_ = json.Unmarshal(m.Content, &blocks)
		for _, b := range blocks {
			switch b.Type {
			case "text":
				human, results = b.Text, 0
			case "tool_result":
				results++
			}
		}
	}
	text, calls := next(human, results)
	content := []any{map[string]any{"type": "text", "text": text}}
	stop := "end_turn"
	for _, c := range calls {
		content = append(content, map[string]any{"type": "tool_use", "id": "toolu_" + randomID(), "name": c.Name, "input": c.Args})
		stop = "tool_use"
	}
	msg := map[string]any{
		"id": "msg_" + randomID(), "type": "message", "role": "assistant", "model": req.Model,
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 120, "output_tokens": 60},
	}
	if req.Stream {
		streamMessages(w, msg)
		return
	}
	reply(w, msg)
}

// chat answers in OpenAI's format: tool calls are a "tool_calls" list on the
// message, with arguments as JSON text, and results come back as "tool" messages.
func chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model         string
		Stream        bool
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad JSON","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}
	human, results := "", 0
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			var text string
			if json.Unmarshal(m.Content, &text) != nil { // a list of parts
				var parts []struct{ Type, Text string }
				_ = json.Unmarshal(m.Content, &parts)
				for _, p := range parts {
					text += p.Text
				}
			}
			human, results = text, 0
		case "tool":
			results++
		}
	}
	text, calls := next(human, results)
	message := map[string]any{"role": "assistant", "content": text}
	finish := "stop"
	if len(calls) > 0 {
		var list []any
		for _, c := range calls {
			args, _ := json.Marshal(c.Args)
			list = append(list, map[string]any{"id": "call_" + randomID(), "type": "function", "function": map[string]any{"name": c.Name, "arguments": string(args)}})
		}
		message["tool_calls"] = list
		finish = "tool_calls"
	}
	completion := map[string]any{
		"id": "chatcmpl-" + randomID(), "object": "chat.completion", "created": 1763400000, "model": req.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 120, "completion_tokens": 60, "total_tokens": 180},
	}
	if req.Stream {
		streamChat(w, completion, req.StreamOptions.IncludeUsage)
		return
	}
	reply(w, completion)
}

// ---- streaming ----------------------------------------------------------

func startStream(w http.ResponseWriter) func(event string, data any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	return func(event string, data any) {
		payload, _ := json.Marshal(data)
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(pause)
	}
}

// pieces cuts s into fragments of about n bytes, as a provider's stream does.
func pieces(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

// streamMessages writes an Anthropic Messages stream: message_start, then
// start/delta.../stop for each content block, then message_delta, message_stop.
func streamMessages(w http.ResponseWriter, msg map[string]any) {
	send := startStream(w)
	start := map[string]any{}
	for k, v := range msg {
		start[k] = v
	}
	start["content"], start["stop_reason"] = []any{}, nil
	start["usage"] = map[string]any{"input_tokens": 120, "output_tokens": 1}
	send("message_start", map[string]any{"type": "message_start", "message": start})
	send("ping", map[string]any{"type": "ping"})
	for i, b := range msg["content"].([]any) {
		block := b.(map[string]any)
		var opening map[string]any
		var deltas []map[string]any
		if block["type"] == "text" {
			opening = map[string]any{"type": "text", "text": ""}
			for _, p := range pieces(block["text"].(string), 12) {
				deltas = append(deltas, map[string]any{"type": "text_delta", "text": p})
			}
		} else {
			opening = map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}
			args, _ := json.Marshal(block["input"])
			for _, p := range pieces(string(args), 7) {
				deltas = append(deltas, map[string]any{"type": "input_json_delta", "partial_json": p})
			}
		}
		send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": opening})
		for _, d := range deltas {
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": d})
		}
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	send("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": msg["stop_reason"], "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 60}})
	send("message_stop", map[string]any{"type": "message_stop"})
}

// streamChat writes a Chat Completions stream: the role, the text a few
// words at a time, each tool call's name and then its arguments in
// fragments, the finish reason, the usage if asked for, and [DONE].
func streamChat(w http.ResponseWriter, completion map[string]any, includeUsage bool) {
	send := startStream(w)
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": completion["id"], "object": "chat.completion.chunk", "created": completion["created"],
			"model": completion["model"], "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	choice := completion["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	send("", chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	for _, p := range pieces(msg["content"].(string), 12) {
		send("", chunk(map[string]any{"content": p}, nil))
	}
	calls, _ := msg["tool_calls"].([]any)
	for i, c := range calls {
		call := c.(map[string]any)
		fn := call["function"].(map[string]any)
		send("", chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": call["id"], "type": "function",
			"function": map[string]any{"name": fn["name"], "arguments": ""}}}}, nil))
		for _, p := range pieces(fn["arguments"].(string), 7) {
			send("", chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"arguments": p}}}}, nil))
		}
	}
	send("", chunk(map[string]any{}, choice["finish_reason"]))
	if includeUsage {
		send("", map[string]any{"id": completion["id"], "object": "chat.completion.chunk", "created": completion["created"],
			"model": completion["model"], "choices": []any{}, "usage": completion["usage"]})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func reply(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
