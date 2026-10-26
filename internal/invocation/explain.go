package invocation

import (
	"encoding/json"
	"strings"
)

// Evidence is a part of a call the decider quoted, and why that part led
// where it did.
type Evidence struct {
	Quote string `json:"quote"`
	Why   string `json:"why,omitempty"`
}

// Explanation is a decider's account of why it decided a call the way it
// did. Every field is optional: a decider that fills none of them leaves
// the call explained by its reason alone, as before.
//
// It is written to be read by the agent whose call was decided, so that an
// agent told no can tell what to do instead. Reasoning is why, Clauses are
// the charter clauses the decision rests on, Evidence the parts of the call
// that led there, and AllowedIf the change that would make the call allowed.
// The console shows a reviewer the same fields under the same names.
//
// Everything here reaches the agent, so a decider puts nothing in it that
// the agent may not see.
type Explanation struct {
	Reasoning string     `json:"reasoning,omitempty"`
	Clauses   []string   `json:"clauses,omitempty"`
	Evidence  []Evidence `json:"evidence,omitempty"`
	AllowedIf string     `json:"allowed_if,omitempty"`
}

// Empty reports whether e has nothing to tell the agent.
func (e *Explanation) Empty() bool {
	return e == nil || (e.Reasoning == "" && len(e.Clauses) == 0 && len(e.Evidence) == 0 && e.AllowedIf == "")
}

// DeniedText renders a denial for the agent that made the call. headline
// says what refused it, reason is the decider's one-line basis, and ex the
// account behind it; reason and ex may be empty.
//
// Every path that denies a call renders it through here, so that an agent
// reading a denial over the MCP proxy and one reading it through a harness
// hook are told the same things in the same order (sc-19113).
func DeniedText(headline, reason string, ex *Explanation) string {
	var b strings.Builder
	b.WriteString(headline)
	if reason = strings.TrimSpace(reason); reason != "" {
		b.WriteString("\n\nReason: " + reason)
	}
	if ex.Empty() {
		return b.String()
	}
	if ex.Reasoning != "" {
		b.WriteString("\n\nWhy: " + ex.Reasoning)
	}
	for _, q := range ex.Evidence {
		if q.Quote == "" {
			continue
		}
		b.WriteString("\n\nIn this call: " + q.Quote)
		if q.Why != "" {
			b.WriteString("\n  " + q.Why)
		}
	}
	if len(ex.Clauses) > 0 {
		b.WriteString("\n\nCharter clauses: " + strings.Join(ex.Clauses, ", "))
	}
	if ex.AllowedIf != "" {
		b.WriteString("\n\nWhat would make it allowed: " + ex.AllowedIf)
	}
	return b.String()
}

// deniedError is the tool result a denied call returns, built from the same
// text every denial path renders.
func deniedError(headline, reason string, ex *Explanation) json.RawMessage {
	return mustJSON(map[string]any{
		"content": []map[string]any{{"type": "text", "text": DeniedText(headline, reason, ex)}},
		"isError": true,
	})
}
