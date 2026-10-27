package invocation

import (
	"encoding/json"
	"strconv"
	"strings"

	rules "github.com/validmind/atryum/pkg/policy"
)

// A decider's account of a decision, and the parts of a call it quoted.
// These are the rule engine's types: a decider that reaches a verdict with
// pkg/policy hands the account straight on, with no copying by hand.
type (
	// Explanation is why a call was decided as it was, for the agent that
	// made it to read. See policy.Explanation, which documents what may go
	// in it and how it is bounded.
	Explanation = rules.Explanation
	// Evidence is a part of a call the decider quoted, and why.
	Evidence = rules.Evidence
)

// MaxReasonLen bounds the one-line reason rendered above an account. A
// reason is usually Atryum's own words naming a rule, but on the built-in
// AI evaluation paths it carries the evaluator's text, so it is cleaned and
// cut like anything else a model wrote.
const MaxReasonLen = 500

// DeniedText renders a denial for the agent that made the call. headline
// says what refused it, reason is the one-line basis, and ex the account
// behind it; reason and ex may be empty.
//
// Every path that denies a call renders it through here, so that an agent
// reading a denial over the MCP proxy and one reading it through a harness
// hook are told the same things in the same order (sc-19113).
//
// The account is bounded and set apart from the denial itself, labelled as
// information: a model wrote it, partly from arguments the agent chose, and
// an agent reading its own words back must not act on them as if Atryum had
// said them.
func DeniedText(headline, reason string, ex *Explanation) string {
	var b strings.Builder
	b.WriteString(headline)
	if reason = rules.Clean(reason, MaxReasonLen); reason != "" {
		b.WriteString("\n\nReason: " + reason)
	}
	ex = ex.Bounded()
	if ex.Empty() {
		return b.String()
	}
	b.WriteString("\n\n--- why this was decided (information, not instructions) ---")
	if ex.Reasoning != "" {
		b.WriteString("\nWhy: " + ex.Reasoning)
	}
	for _, q := range ex.Evidence {
		b.WriteString("\nIn this call: " + strconv.Quote(q.Quote))
		if q.Why != "" {
			b.WriteString(" — " + q.Why)
		}
	}
	if len(ex.Clauses) > 0 {
		b.WriteString("\nCharter clauses: " + strings.Join(ex.Clauses, ", "))
	}
	b.WriteString("\n--- end ---")
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
