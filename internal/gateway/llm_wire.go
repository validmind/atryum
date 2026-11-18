package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The three LLM APIs an AI gateway relays. Each names a wire format: where the
// model's tool calls sit in a response, where the tools' results sit in the
// next request, and how a response reads once a call has been taken out.
const (
	FormatAnthropic       = "anthropic"        // Anthropic Messages (/v1/messages)
	FormatOpenAIChat      = "openai_chat"      // OpenAI Chat Completions (/v1/chat/completions)
	FormatOpenAIResponses = "openai_responses" // OpenAI Responses (/v1/responses)
)

const (
	// ProviderServer is the server recorded for a tool the model provider
	// runs itself (web search, code execution): no agent or MCP server does.
	ProviderServer = "model-provider"
	// ProviderMCPTool is the tool recorded for an MCP server the provider
	// connects to; the server's own name is the call's server.
	ProviderMCPTool = "mcp_connector"
)

// modelCall is one tool call the model proposed and the agent would run.
type modelCall struct {
	ID     string // the model's ID for the call (toolu_…, call_…)
	Name   string // as the model named it
	Server string // the MCP server, when the name carries one
	Tool   string // the name without the server
	// Arguments is a JSON object. Unreadable says why there is none.
	Arguments  json.RawMessage
	Unreadable string
}

// toolOutcome is a tool result the agent sent back to the model.
type toolOutcome struct {
	CallID  string
	Failed  bool
	Content json.RawMessage
}

// providerTool is a tool a request asks the model provider to run. The agent
// never sees its calls, so it can only be allowed or taken out of the request.
type providerTool struct {
	Tool      string
	Server    string
	Arguments json.RawMessage
}

func (p providerTool) key() string { return p.Server + "\x00" + p.Tool }

// normalizeFormat maps the names gateways use onto the three formats; ""
// means "work it out from the body".
func normalizeFormat(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", true
	case FormatAnthropic, "anthropic_messages", "messages":
		return FormatAnthropic, true
	case FormatOpenAIChat, "openai", "chat", "chat_completions", "chat.completions":
		return FormatOpenAIChat, true
	case FormatOpenAIResponses, "responses":
		return FormatOpenAIResponses, true
	}
	return "", false
}

// detectResponseFormat recognises a whole model response by its shape, or
// returns "" for anything else: a streamed chunk or event, another
// provider's format, an error. Reading a body in the wrong format finds no
// calls and would pass every one, so "" is refused wherever tools are in
// play.
func detectResponseFormat(resp map[string]any) string {
	for _, f := range []string{FormatOpenAIChat, FormatOpenAIResponses, FormatAnthropic} {
		if isResponseFormat(f, resp) {
			return f
		}
	}
	return ""
}

// isResponseFormat reports whether resp is a whole response in format f.
func isResponseFormat(f string, resp map[string]any) bool {
	switch f {
	case FormatOpenAIChat:
		choices, ok := resp["choices"].([]any)
		if !ok || stringAt(resp, "object") == "chat.completion.chunk" {
			return false
		}
		for _, ch := range choices {
			if asMap(ch)["delta"] != nil { // a streamed chunk
				return false
			}
		}
		return true
	case FormatOpenAIResponses:
		_, isList := resp["output"].([]any)
		if resp["output"] != nil && !isList { // Bedrock Converse's output is an object
			return false
		}
		return isList || stringAt(resp, "object") == "response"
	case FormatAnthropic:
		_, isList := resp["content"].([]any)
		return isList && (stringAt(resp, "type") == "message" || (resp["type"] == nil && stringAt(resp, "role") == "assistant"))
	}
	return false
}

// detectRequestFormat recognises a request by its shape. Chat Completions and
// Messages requests look alike until they carry tools, a system prompt or
// tool results; a request with none of those has nothing for Agent Authority
// to read in either format, so Chat Completions is the answer for it.
func detectRequestFormat(req map[string]any) string {
	if f := requestFormatSignal(req); f != "" {
		return f
	}
	return FormatOpenAIChat
}

// requestFormatSignal is the format a request's shape shows for certain, or
// "" when nothing in it tells Chat Completions and Messages apart.
func requestFormatSignal(req map[string]any) string {
	if req["input"] != nil && req["messages"] == nil {
		return FormatOpenAIResponses
	}
	if req["system"] != nil || req["mcp_servers"] != nil {
		return FormatAnthropic
	}
	for _, t := range listAt(req, "tools") {
		tool, _ := t.(map[string]any)
		if tool["input_schema"] != nil {
			return FormatAnthropic
		}
		if tool["function"] != nil {
			return FormatOpenAIChat
		}
		if _, ok := anthropicProviderTool(tool); ok {
			return FormatAnthropic
		}
	}
	for _, m := range listAt(req, "messages") {
		msg, _ := m.(map[string]any)
		switch stringAt(msg, "role") {
		case "tool", "system", "developer":
			return FormatOpenAIChat
		}
		if msg["tool_calls"] != nil {
			return FormatOpenAIChat
		}
		for _, b := range listAt(msg, "content") {
			switch stringAt(asMap(b), "type") {
			case "tool_use", "tool_result":
				return FormatAnthropic
			}
		}
	}
	return ""
}

// ---- The model's tool calls, in a response ---------------------------------

// modelCalls lists the calls in resp that the agent would run.
func modelCalls(format string, resp map[string]any) []modelCall {
	var calls []modelCall
	switch format {
	case FormatAnthropic:
		for _, b := range listAt(resp, "content") {
			blk := asMap(b)
			if stringAt(blk, "type") == "tool_use" {
				calls = append(calls, newModelCall(stringAt(blk, "id"), stringAt(blk, "name"), blk["input"]))
			}
		}
	case FormatOpenAIChat:
		for _, ch := range listAt(resp, "choices") {
			msg := mapAt(asMap(ch), "message")
			for _, tc := range listAt(msg, "tool_calls") {
				calls = append(calls, chatCall(asMap(tc)))
			}
			// The pre-2023 single call, still answered when a request uses
			// "functions". It has no ID.
			if fc := mapAt(msg, "function_call"); fc != nil {
				calls = append(calls, newModelCall("", stringAt(fc, "name"), fc["arguments"]))
			}
		}
	case FormatOpenAIResponses:
		for _, it := range listAt(resp, "output") {
			if c, ok := responsesCall(asMap(it)); ok {
				calls = append(calls, c)
			}
		}
	}
	return calls
}

func chatCall(tc map[string]any) modelCall {
	if custom := mapAt(tc, "custom"); custom != nil {
		return newModelCall(stringAt(tc, "id"), stringAt(custom, "name"), map[string]any{"input": custom["input"]})
	}
	fn := mapAt(tc, "function")
	return newModelCall(stringAt(tc, "id"), stringAt(fn, "name"), fn["arguments"])
}

// agentCallKinds are the Responses output items the agent runs. Every other
// *_call item (web_search_call, mcp_call, code_interpreter_call, …) the
// provider already ran.
var agentCallKinds = map[string]bool{
	"function_call": true, "custom_tool_call": true, "computer_call": true,
	"local_shell_call": true, "shell_call": true, "apply_patch_call": true,
}

// responsesCall reads one Responses output item. An item the agent runs
// carries a call_id; one without it can't be told apart from another and is
// unreadable, so it is withheld rather than passed as a provider's.
func responsesCall(item map[string]any) (modelCall, bool) {
	kind := stringAt(item, "type")
	id := stringAt(item, "call_id")
	if !agentCallKinds[kind] && (id == "" || !strings.HasSuffix(kind, "_call")) {
		return modelCall{}, false
	}
	name := firstNonEmpty(stringAt(item, "name"), strings.TrimSuffix(kind, "_call"))
	if id == "" {
		c := newModelCall("", name, nil)
		c.Unreadable = "it has no call_id"
		return c, true
	}
	var args any
	switch {
	case item["arguments"] != nil:
		args = item["arguments"]
	case item["input"] != nil:
		args = item["input"]
		if _, isText := args.(string); isText { // a custom tool's free text
			args = map[string]any{"input": args}
		}
	case item["action"] != nil:
		args = item["action"]
	case item["operation"] != nil:
		args = item["operation"]
	}
	return newModelCall(id, name, args), true
}

func newModelCall(id, name string, args any) modelCall {
	c := modelCall{ID: id, Name: name}
	c.Server, c.Tool = splitToolName(name)
	switch v := args.(type) {
	case nil:
		c.Arguments = json.RawMessage(`{}`)
	case map[string]any:
		c.Arguments, _ = encodeJSON(v)
	case string: // OpenAI sends the arguments as JSON text
		text := strings.TrimSpace(v)
		if text == "" {
			c.Arguments = json.RawMessage(`{}`)
		} else if _, err := decodeObject(json.RawMessage(text)); err != nil {
			c.Unreadable = "its arguments are not a JSON object"
		} else {
			c.Arguments = json.RawMessage(text)
		}
	default:
		c.Unreadable = "its arguments are not a JSON object"
	}
	return c
}

// splitToolName reads the "mcp__{server}__{tool}" names agent harnesses give
// MCP tools, so a rule on the server matches here as it does at the MCP
// gateway. Any other name is a tool with no server.
func splitToolName(name string) (server, tool string) {
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		if s, t, ok := strings.Cut(rest, "__"); ok && s != "" && t != "" {
			return s, t
		}
	}
	return "", name
}

// providerCalls names the tools in resp that the provider already ran. They
// are recorded, never decided: by the time a response exists they are done.
func providerCalls(format string, resp map[string]any) []string {
	var names []string
	switch format {
	case FormatAnthropic:
		for _, b := range listAt(resp, "content") {
			blk := asMap(b)
			switch stringAt(blk, "type") {
			case "server_tool_use":
				names = append(names, stringAt(blk, "name"))
			case "mcp_tool_use":
				names = append(names, firstNonEmpty(stringAt(blk, "server_name"), "mcp")+"/"+stringAt(blk, "name"))
			}
		}
	case FormatOpenAIResponses:
		for _, it := range listAt(resp, "output") {
			item := asMap(it)
			kind := stringAt(item, "type")
			if !strings.HasSuffix(kind, "_call") || stringAt(item, "call_id") != "" {
				continue
			}
			if kind == "mcp_call" {
				names = append(names, firstNonEmpty(stringAt(item, "server_label"), "mcp")+"/"+stringAt(item, "name"))
				continue
			}
			names = append(names, strings.TrimSuffix(kind, "_call"))
		}
	}
	return names
}

// ---- Taking calls out of a response ----------------------------------------

// withhold removes the calls named in notices (call ID -> what to tell the
// agent) from resp and puts each notice where the model's own words go, so
// the agent reads it as the reply and keeps it in its history. A response
// left with no call to run ends the turn.
func withhold(format string, resp map[string]any, notices map[string]string) {
	switch format {
	case FormatAnthropic:
		content := listAt(resp, "content")
		left := 0
		for i, b := range content {
			blk := asMap(b)
			if stringAt(blk, "type") != "tool_use" {
				continue
			}
			if note, ok := notices[stringAt(blk, "id")]; ok {
				content[i] = map[string]any{"type": "text", "text": note}
			} else {
				left++
			}
		}
		if left == 0 && stringAt(resp, "stop_reason") == "tool_use" {
			resp["stop_reason"] = "end_turn"
		}
	case FormatOpenAIChat:
		for _, ch := range listAt(resp, "choices") {
			choice := asMap(ch)
			msg := mapAt(choice, "message")
			var kept []any
			var notes []string
			for _, tc := range listAt(msg, "tool_calls") {
				if note, ok := notices[stringAt(asMap(tc), "id")]; ok {
					notes = append(notes, note)
				} else {
					kept = append(kept, tc)
				}
			}
			if note, ok := notices[""]; ok && msg["function_call"] != nil {
				delete(msg, "function_call")
				notes = append(notes, note)
			}
			if len(notes) == 0 {
				continue
			}
			appendChatText(msg, strings.Join(notes, "\n\n"))
			if len(kept) > 0 {
				msg["tool_calls"] = kept
				continue
			}
			delete(msg, "tool_calls")
			if msg["function_call"] == nil {
				choice["finish_reason"] = "stop"
			}
		}
	case FormatOpenAIResponses:
		output := listAt(resp, "output")
		for i, it := range output {
			item := asMap(it)
			if _, isCall := responsesCall(item); !isCall {
				continue
			}
			id := stringAt(item, "call_id")
			if note, ok := notices[id]; ok {
				output[i] = map[string]any{
					"id": "msg_aa_" + id, "type": "message", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": note, "annotations": []any{}}},
				}
			}
		}
	}
}

// appendChatText adds text to a Chat Completions message, whose content is a
// string, null, or (rarely) a list of parts.
func appendChatText(msg map[string]any, text string) {
	switch content := msg["content"].(type) {
	case string:
		if strings.TrimSpace(content) != "" {
			text = content + "\n\n" + text
		}
		msg["content"] = text
	case []any:
		msg["content"] = append(content, map[string]any{"type": "text", "text": text})
	default:
		msg["content"] = text
	}
}

// ---- Tool results, in the next request -------------------------------------

// toolOutcomes lists the tool results a request adds: those after the model's
// last turn. Everything before it was in an earlier request too.
func toolOutcomes(format string, req map[string]any) []toolOutcome {
	var out []toolOutcome
	switch format {
	case FormatAnthropic:
		for _, m := range afterLastModelTurn(listAt(req, "messages"), isAssistantMessage) {
			for _, b := range listAt(asMap(m), "content") {
				blk := asMap(b)
				if stringAt(blk, "type") != "tool_result" {
					continue
				}
				failed, _ := blk["is_error"].(bool)
				out = append(out, newOutcome(stringAt(blk, "tool_use_id"), failed, blk["content"]))
			}
		}
	case FormatOpenAIChat:
		for _, m := range afterLastModelTurn(listAt(req, "messages"), isAssistantMessage) {
			msg := asMap(m)
			if stringAt(msg, "role") == "tool" {
				out = append(out, newOutcome(stringAt(msg, "tool_call_id"), false, msg["content"]))
			}
		}
	case FormatOpenAIResponses:
		for _, it := range afterLastModelTurn(listAt(req, "input"), isResponsesModelItem) {
			item := asMap(it)
			if !strings.HasSuffix(stringAt(item, "type"), "_output") || stringAt(item, "call_id") == "" {
				continue
			}
			out = append(out, newOutcome(stringAt(item, "call_id"), stringAt(item, "status") == "failed", item["output"]))
		}
	}
	return out
}

// requestCalls lists the calls the model proposed in a request's history,
// by ID (a later turn's call wins for a reused ID). A tool result is matched
// to the call it sits beside.
func requestCalls(format string, req map[string]any) map[string]modelCall {
	out := map[string]modelCall{}
	switch format {
	case FormatAnthropic:
		for _, m := range listAt(req, "messages") {
			if !isAssistantMessage(asMap(m)) {
				continue
			}
			for _, b := range listAt(asMap(m), "content") {
				if blk := asMap(b); stringAt(blk, "type") == "tool_use" {
					out[stringAt(blk, "id")] = newModelCall(stringAt(blk, "id"), stringAt(blk, "name"), blk["input"])
				}
			}
		}
	case FormatOpenAIChat:
		for _, m := range listAt(req, "messages") {
			msg := asMap(m)
			if !isAssistantMessage(msg) {
				continue
			}
			for _, tc := range listAt(msg, "tool_calls") {
				c := chatCall(asMap(tc))
				out[c.ID] = c
			}
		}
	case FormatOpenAIResponses:
		for _, it := range listAt(req, "input") {
			if c, ok := responsesCall(asMap(it)); ok && c.ID != "" {
				out[c.ID] = c
			}
		}
	}
	delete(out, "")
	return out
}

// sameArguments reports whether two argument objects are equal as values
// (key order and whitespace aside).
func sameArguments(a, b json.RawMessage) bool {
	x, errA := parseArguments(a)
	y, errB := parseArguments(b)
	if errA != nil || errB != nil {
		return false
	}
	cx, errA := canonicalJSON(x)
	cy, errB := canonicalJSON(y)
	return errA == nil && errB == nil && bytes.Equal(cx, cy)
}

func newOutcome(callID string, failed bool, content any) toolOutcome {
	raw, _ := encodeJSON(map[string]any{"content": content})
	return toolOutcome{CallID: callID, Failed: failed, Content: raw}
}

func afterLastModelTurn(items []any, fromModel func(map[string]any) bool) []any {
	for i := len(items) - 1; i >= 0; i-- {
		if fromModel(asMap(items[i])) {
			return items[i+1:]
		}
	}
	return items
}

func isAssistantMessage(m map[string]any) bool { return stringAt(m, "role") == "assistant" }

func isResponsesModelItem(item map[string]any) bool {
	kind := stringAt(item, "type")
	return stringAt(item, "role") == "assistant" || kind == "reasoning" || strings.HasSuffix(kind, "_call")
}

// ---- Tools the provider runs, in a request ---------------------------------

// providerTools lists the tools a request asks the provider to run.
func providerTools(format string, req map[string]any) []providerTool {
	var out []providerTool
	switch format {
	case FormatAnthropic:
		for _, t := range listAt(req, "tools") {
			if p, ok := anthropicProviderTool(asMap(t)); ok {
				out = append(out, p)
			}
		}
		for _, s := range listAt(req, "mcp_servers") {
			out = append(out, mcpConnector(stringAt(asMap(s), "name"), stringAt(asMap(s), "url")))
		}
	case FormatOpenAIChat:
		if req["web_search_options"] != nil {
			out = append(out, providerTool{Tool: "web_search", Server: ProviderServer, Arguments: json.RawMessage(`{}`)})
		}
	case FormatOpenAIResponses:
		for _, t := range listAt(req, "tools") {
			if p, ok := responsesProviderTool(asMap(t)); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// anthropicServerTools are the tool types Anthropic runs on its side. Every
// other typed tool (bash_…, text_editor_…, computer_…) is run by the agent
// and comes back as an ordinary tool_use.
var anthropicServerTools = []string{"web_search_", "web_fetch_", "code_execution_"}

func anthropicProviderTool(tool map[string]any) (providerTool, bool) {
	kind := stringAt(tool, "type")
	for _, prefix := range anthropicServerTools {
		if strings.HasPrefix(kind, prefix) {
			name := firstNonEmpty(stringAt(tool, "name"), strings.TrimSuffix(prefix, "_"))
			return providerTool{Tool: name, Server: ProviderServer, Arguments: json.RawMessage(`{}`)}, true
		}
	}
	return providerTool{}, false
}

// responsesAgentTools are the Responses tool types the agent runs itself.
var responsesAgentTools = map[string]bool{"function": true, "custom": true, "computer_use_preview": true, "computer": true, "local_shell": true, "shell": true, "apply_patch": true}

func responsesProviderTool(tool map[string]any) (providerTool, bool) {
	kind := stringAt(tool, "type")
	if kind == "" || responsesAgentTools[kind] {
		return providerTool{}, false
	}
	if kind == "mcp" {
		return mcpConnector(stringAt(tool, "server_label"), stringAt(tool, "server_url")), true
	}
	return providerTool{Tool: kind, Server: ProviderServer, Arguments: json.RawMessage(`{}`)}, true
}

func mcpConnector(name, url string) providerTool {
	args, _ := encodeJSON(map[string]any{"url": url})
	return providerTool{Tool: ProviderMCPTool, Server: firstNonEmpty(name, "mcp"), Arguments: args}
}

// dropProviderTools takes the denied tools (by providerTool.key) out of req.
func dropProviderTools(format string, req map[string]any, denied map[string]bool) {
	switch format {
	case FormatAnthropic:
		gone := map[string]bool{} // MCP servers taken out, by name
		filterList(req, "mcp_servers", func(s map[string]any) bool {
			p := mcpConnector(stringAt(s, "name"), stringAt(s, "url"))
			if denied[p.key()] {
				gone[stringAt(s, "name")] = true
			}
			return !denied[p.key()]
		})
		filterList(req, "tools", func(t map[string]any) bool {
			if stringAt(t, "type") == "mcp_toolset" {
				return !gone[stringAt(t, "mcp_server_name")]
			}
			p, ok := anthropicProviderTool(t)
			return !ok || !denied[p.key()]
		})
	case FormatOpenAIChat:
		if denied[providerTool{Tool: "web_search", Server: ProviderServer}.key()] {
			delete(req, "web_search_options")
		}
	case FormatOpenAIResponses:
		filterList(req, "tools", func(t map[string]any) bool {
			p, ok := responsesProviderTool(t)
			return !ok || !denied[p.key()]
		})
	}
	// Providers refuse a tool choice with no tools to choose from.
	if _, has := req["tools"]; !has {
		delete(req, "tool_choice")
	}
}

// filterList keeps the entries of m[key] that keep accepts, and removes the
// key when none are left (providers refuse an empty list).
func filterList(m map[string]any, key string, keep func(map[string]any) bool) {
	list := listAt(m, key)
	if list == nil {
		return
	}
	var kept []any
	for _, e := range list {
		if keep(asMap(e)) {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		delete(m, key)
		return
	}
	m[key] = kept
}

// declaresTools reports whether a request gives the model any tool to call,
// or may: Bedrock Converse lists them under toolConfig, and a stored
// Responses prompt (prompt.id) can carry tools the request doesn't show.
func declaresTools(req map[string]any) bool {
	return len(listAt(req, "tools")) > 0 || len(listAt(req, "functions")) > 0 || len(listAt(req, "mcp_servers")) > 0 ||
		req["toolConfig"] != nil || req["tool_config"] != nil || mapAt(req, "prompt") != nil
}

// ---- What the person asked, for the judge ----------------------------------

const (
	excerptMessages = 5
	excerptChars    = 2000
)

// humanExcerpt renders the last few things a person typed in the request's
// conversation, in the form the judge already reads from other paths. Tool
// results and the model's own turns are left out.
func humanExcerpt(format string, req map[string]any) (string, int) {
	var texts []string
	add := func(content any, textType string) {
		var parts []string
		switch c := content.(type) {
		case string:
			parts = append(parts, c)
		case []any:
			for _, p := range c {
				part := asMap(p)
				if t := stringAt(part, "type"); t == "text" || t == textType {
					parts = append(parts, stringAt(part, "text"))
				}
			}
		}
		if text := strings.TrimSpace(strings.Join(parts, "\n")); text != "" {
			texts = append(texts, text)
		}
	}
	switch format {
	case FormatAnthropic, FormatOpenAIChat:
		for _, m := range listAt(req, "messages") {
			if msg := asMap(m); stringAt(msg, "role") == "user" {
				add(msg["content"], "text")
			}
		}
	case FormatOpenAIResponses:
		if text, ok := req["input"].(string); ok {
			add(text, "")
		}
		for _, it := range listAt(req, "input") {
			if item := asMap(it); stringAt(item, "role") == "user" {
				add(item["content"], "input_text")
			}
		}
	}
	if len(texts) == 0 {
		return "", 0
	}
	if len(texts) > excerptMessages {
		texts = texts[len(texts)-excerptMessages:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Recent human messages (oldest to newest, up to %d):", excerptMessages)
	for _, text := range texts {
		if r := []rune(text); len(r) > excerptChars {
			text = string(r[:excerptChars]) + " […]"
		}
		b.WriteString("\n- ")
		b.WriteString(strings.ReplaceAll(text, "\n", " "))
	}
	return b.String(), len(texts)
}

// ---- JSON helpers -----------------------------------------------------------

// decodeObject reads a JSON object, keeping numbers as written.
func decodeObject(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF { // one object, nothing after it
		return nil, fmt.Errorf("not a single JSON object")
	}
	return m, nil
}

// encodeJSON writes v without HTML escaping, so URLs in a notice stay readable.
func encodeJSON(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func listAt(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	v, _ := m[key].([]any)
	return v
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func mapAt(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func stringAt(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
