package managedagents

import (
	"encoding/json"
	"strings"
)

// Event type names emitted by Anthropic Managed Agents that the bridge keys on.
const (
	evtAgentToolUse       = "agent.tool_use"
	evtAgentMCPToolUse    = "agent.mcp_tool_use"
	evtAgentCustomToolUse = "agent.custom_tool_use"
	evtSessionIdle        = "session.status_idle"

	stopReasonRequiresAction = "requires_action"
)

// firstString returns the first non-empty string value among the given keys.
func firstString(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// asObject decodes a RawEvent body into a generic key->raw map.
func asObject(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	return m
}

type chatMessage struct {
	Role string
	Text string
}

// parseChatMessage extracts human (user) messages only. Agent-authored messages
// are intentionally excluded from judge context: the agent under evaluation is
// not trusted, and its messages add a prompt-injection surface with little
// value for judging a concrete tool call.
func parseChatMessage(evt RawEvent) (chatMessage, bool) {
	if evt.Type != "user.message" {
		return chatMessage{}, false
	}
	m := asObject(evt.Raw)
	role := firstString(m, "role", "sender", "author")
	if role == "" {
		role = "user"
	}
	text := messageText(m)
	if strings.TrimSpace(text) == "" {
		return chatMessage{}, false
	}
	return chatMessage{Role: role, Text: strings.TrimSpace(text)}, true
}

func messageText(m map[string]json.RawMessage) string {
	for _, key := range []string{"content", "text"} {
		if text := contentText(m[key]); text != "" {
			return text
		}
	}
	if raw, ok := m["message"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) == nil {
			if text := messageText(nested); text != "" {
				return text
			}
		}
		if text := contentText(raw); text != "" {
			return text
		}
	}
	return ""
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if text := contentText(block["text"]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// parseToolUse extracts the normalized tool-call fields from a tool-use event.
// Returns ok=false when the event is not a tool-use event.
func parseToolUse(evt RawEvent) (toolUse, bool) {
	var custom bool
	switch evt.Type {
	case evtAgentToolUse, evtAgentMCPToolUse:
	case evtAgentCustomToolUse:
		custom = true
	default:
		return toolUse{}, false
	}
	m := asObject(evt.Raw)
	tu := toolUse{
		EventID:  evt.ID,
		Kind:     evt.Type,
		ToolName: firstString(m, "name", "tool_name", "tool"),
		IsCustom: custom,
	}
	if tu.EventID == "" {
		tu.EventID = firstString(m, "tool_use_id", "id")
	}
	tu.ServerName = firstString(m, "server_name", "mcp_server_name")
	if raw, ok := m["input"]; ok {
		_ = json.Unmarshal(raw, &tu.Input)
	}
	if tu.Input == nil {
		if raw, ok := m["arguments"]; ok {
			_ = json.Unmarshal(raw, &tu.Input)
		}
	}
	return tu, tu.ToolName != ""
}

// requiresAction reports whether an idle event is blocking on client action and
// returns the blocking event IDs. Returns ok=false for non-blocking idles
// (e.g. stop_reason end_turn).
func requiresAction(evt RawEvent) (eventIDs []string, ok bool) {
	if evt.Type != evtSessionIdle {
		return nil, false
	}
	m := asObject(evt.Raw)
	raw, present := m["stop_reason"]
	if !present {
		return nil, false
	}
	var stop struct {
		Type     string   `json:"type"`
		EventIDs []string `json:"event_ids"`
	}
	if err := json.Unmarshal(raw, &stop); err != nil {
		return nil, false
	}
	if stop.Type != stopReasonRequiresAction {
		return nil, false
	}
	return stop.EventIDs, true
}

// toolResult holds the outcome parsed from a tool-result event.
type toolResult struct {
	Kind      string
	ToolUseID string
	IsError   bool
	Content   json.RawMessage
}

// parseToolResult extracts a tool result keyed to its originating tool-use
// event. Returns ok=false when the event is not a recognizable tool result.
func parseToolResult(evt RawEvent) (toolResult, bool) {
	if !strings.Contains(evt.Type, "tool_result") {
		return toolResult{}, false
	}
	m := asObject(evt.Raw)
	tr := toolResult{
		Kind:      evt.Type,
		ToolUseID: firstString(m, "tool_use_id", "mcp_tool_use_id", "custom_tool_use_id", "tool_use_event_id"),
	}
	if tr.ToolUseID == "" {
		return toolResult{}, false
	}
	if raw, ok := m["is_error"]; ok {
		_ = json.Unmarshal(raw, &tr.IsError)
	}
	if raw, ok := m["status"]; ok {
		var status string
		if json.Unmarshal(raw, &status) == nil && (status == "error" || status == "failed") {
			tr.IsError = true
		}
	}
	if raw, ok := m["content"]; ok {
		tr.Content = raw
	} else if raw, ok := m["result"]; ok {
		tr.Content = raw
	} else if raw, ok := m["output"]; ok {
		tr.Content = raw
	}
	return tr, true
}
