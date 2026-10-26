package invocation

import (
	"encoding/json"
	"strings"
	"testing"
)

// A denied agent is told why, which part of its call did it, and what
// would be allowed instead (sc-19113).
func TestDeniedTextRendersTheWholeAccount(t *testing.T) {
	got := DeniedText("Tool call denied.", "C-04 Protect the ledger → judge said deny", &Explanation{
		Reasoning: "the charter lets this agent read the ledger, never write it",
		Clauses:   []string{"3.1", "3.4"},
		Evidence:  []Evidence{{Quote: "UPDATE ledger SET", Why: "writes a system of record"}},
		AllowedIf: "read the balance with a SELECT instead",
	})
	for _, want := range []string{
		"Tool call denied.",
		"Reason: C-04 Protect the ledger → judge said deny",
		"Why: the charter lets this agent read the ledger, never write it",
		"In this call: UPDATE ledger SET",
		"writes a system of record",
		"Charter clauses: 3.1, 3.4",
		"What would make it allowed: read the balance with a SELECT instead",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Without an account the text is what it always was: a headline and the
// reason. A decider that fills nothing in loses nothing.
func TestDeniedTextWithoutAnAccount(t *testing.T) {
	if got, want := DeniedText("Tool call denied.", "no rule matched", nil),
		"Tool call denied.\n\nReason: no rule matched"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := DeniedText("Tool call denied.", "", nil), "Tool call denied."; got != want {
		t.Errorf("bare: got %q, want %q", got, want)
	}
	if got := DeniedText("Tool call denied.", "r", &Explanation{}); !strings.HasSuffix(got, "Reason: r") {
		t.Errorf("empty explanation added text: %q", got)
	}
}

func TestExplanationEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		ex   *Explanation
		want bool
	}{
		{"nil", nil, true},
		{"zero", &Explanation{}, true},
		{"reasoning", &Explanation{Reasoning: "x"}, false},
		{"clauses", &Explanation{Clauses: []string{"1"}}, false},
		{"evidence", &Explanation{Evidence: []Evidence{{Quote: "q"}}}, false},
		{"allowed_if", &Explanation{AllowedIf: "x"}, false},
	} {
		if got := tc.ex.Empty(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The tool result a denied call returns is the same text, in the MCP error
// envelope every transport reads.
func TestDeniedErrorEnvelope(t *testing.T) {
	var out struct {
		Content []struct{ Type, Text string } `json:"content"`
		IsError bool                          `json:"isError"`
	}
	if err := json.Unmarshal(deniedError("Tool call denied.", "r", &Explanation{AllowedIf: "ask a person"}), &out); err != nil {
		t.Fatal(err)
	}
	if !out.IsError || len(out.Content) != 1 || out.Content[0].Type != "text" {
		t.Fatalf("envelope: %+v", out)
	}
	if !strings.Contains(out.Content[0].Text, "What would make it allowed: ask a person") {
		t.Errorf("account missing: %q", out.Content[0].Text)
	}
}
