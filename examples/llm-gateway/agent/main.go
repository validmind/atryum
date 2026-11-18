// Command agent is the smallest useful agent, for the walkthrough in
// ../README.md: it sends what you type to a model, runs the tools the model
// asks for, sends the results back, and repeats until the model stops asking.
//
// It knows nothing about Atryum or Agent Authority. It only talks to the AI
// gateway, as an agent would to any model endpoint; that is the point.
//
// Its three tools are pretend. Each prints a line when it runs, so the
// transcript shows which calls reached the agent and which never did.
//
// With -stream it asks for a streamed answer, as coding agents do, and prints
// the model's words as they arrive.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// tools are what this agent can do, and what it tells the model it can do.
var tools = map[string]func(args map[string]any) string{
	"get_ticket": func(map[string]any) string {
		return `{"ticket_id": 7, "customer": "Dana Whitfield", "subject": "Charged twice for the annual plan", "amount_usd": 900}`
	},
	"refund_payment": func(args map[string]any) string {
		return fmt.Sprintf(`{"refunded_usd": %v, "status": "sent to the card"}`, args["amount_usd"])
	},
	"export_customer_data": func(args map[string]any) string {
		return fmt.Sprintf(`{"sent_to": %q, "records": 48211}`, args["send_to"])
	},
}

type block struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

func main() {
	gateway := flag.String("gateway", "http://127.0.0.1:4100", "the AI gateway (where a real agent's ANTHROPIC_BASE_URL would point)")
	user := flag.String("user", "emma@example.test", "who is using the agent; a real gateway knows this from its own sign-in")
	session := flag.String("session", "support-chat-1", "the conversation's ID at the gateway")
	history := flag.String("history", "", "file to keep the conversation in between runs")
	model := flag.String("model", "mock-model", "the model name to ask the gateway for")
	key := flag.String("key", os.Getenv("AGENT_GATEWAY_KEY"), "the agent's key at the gateway, if it needs one (sent as a Bearer token)")
	stream := flag.Bool("stream", false, "ask for a streamed answer and print the words as they arrive")
	flag.Parse()
	prompt := strings.Join(flag.Args(), " ")
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: agent [flags] <what to ask>")
		os.Exit(2)
	}

	var conversation []message
	if *history != "" {
		if saved, err := os.ReadFile(*history); err == nil {
			_ = json.Unmarshal(saved, &conversation)
		}
	}
	fmt.Printf("you    ▸ %s\n", prompt)
	conversation = append(conversation, message{Role: "user", Content: []block{{Type: "text", Text: prompt}}})

	for {
		reply, err := ask(target{*gateway, *model, *key, *user, *session, *stream}, conversation)
		if err != nil {
			fmt.Printf("error  ▸ %v\n", err)
			os.Exit(1)
		}
		conversation = append(conversation, message{Role: "assistant", Content: reply})

		// Run every tool the model asked for, and collect the results.
		var results []block
		for _, b := range reply {
			switch b.Type {
			case "text":
				if !*stream { // a stream has printed its words already
					fmt.Printf("model  ▸ %s\n", b.Text)
				}
			case "tool_use":
				args, _ := json.Marshal(b.Input)
				output := tools[b.Name](b.Input)
				fmt.Printf("agent  ▸ RAN %s %s\n         %s\n", b.Name, args, output)
				results = append(results, block{Type: "tool_result", ToolUseID: b.ID, Content: output})
			}
		}
		if len(results) == 0 {
			break // the model asked for nothing more: the turn is over
		}
		conversation = append(conversation, message{Role: "user", Content: results})
	}

	if *history != "" {
		saved, _ := json.MarshalIndent(conversation, "", "  ")
		_ = os.WriteFile(*history, saved, 0o600)
	}
}

type target struct {
	gateway, model, key, user, session string
	stream                             bool
}

// ask sends the conversation to the model, through the gateway.
func ask(t target, conversation []message) ([]block, error) {
	var declared []map[string]any
	for _, name := range []string{"get_ticket", "refund_payment", "export_customer_data"} {
		declared = append(declared, map[string]any{"name": name, "input_schema": map[string]any{"type": "object"}})
	}
	body, _ := json.Marshal(map[string]any{"model": t.model, "max_tokens": 1024, "tools": declared, "messages": conversation, "stream": t.stream})
	req, err := http.NewRequest(http.MethodPost, t.gateway+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-User", t.user)
	req.Header.Set("X-Gateway-Session", t.session)
	if t.key != "" {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("the gateway answered %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if t.stream {
		return readStream(resp.Body)
	}
	raw, _ := io.ReadAll(resp.Body)
	var reply struct {
		Content []block `json:"content"`
	}
	return reply.Content, json.Unmarshal(raw, &reply)
}

// readStream puts an Anthropic Messages stream back together, printing each
// text block's words as they arrive, with the time since the request began.
func readStream(body io.Reader) ([]block, error) {
	start := time.Now()
	var blocks []block
	partial := map[int]string{}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			ContentBlock block           `json:"content_block"`
			Delta        json.RawMessage `json:"delta"`
			Error        json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &ev) != nil {
			continue
		}
		stamp := fmt.Sprintf("%5.2fs", time.Since(start).Seconds())
		switch ev.Type {
		case "content_block_start":
			for len(blocks) <= ev.Index {
				blocks = append(blocks, block{})
			}
			blocks[ev.Index] = ev.ContentBlock
			if ev.ContentBlock.Type == "text" {
				fmt.Printf("model  ▸ [%s] ", stamp)
			} else {
				fmt.Printf("stream ▸ [%s] %s %s arrives\n", stamp, ev.ContentBlock.Type, ev.ContentBlock.Name)
			}
		case "content_block_delta":
			var d struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			}
			_ = json.Unmarshal(ev.Delta, &d)
			switch d.Type {
			case "text_delta":
				blocks[ev.Index].Text += d.Text
				fmt.Print(d.Text)
			case "input_json_delta":
				partial[ev.Index] += d.PartialJSON
			}
		case "content_block_stop":
			if blocks[ev.Index].Type == "text" {
				fmt.Println()
			}
			if text, ok := partial[ev.Index]; ok {
				_ = json.Unmarshal([]byte(text), &blocks[ev.Index].Input)
				if blocks[ev.Index].Input == nil {
					blocks[ev.Index].Input = map[string]any{}
				}
			}
		case "error":
			return nil, fmt.Errorf("the stream ended in an error: %s", ev.Error)
		}
	}
	return blocks, scanner.Err()
}
