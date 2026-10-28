package policy

import (
	"strings"
	"unicode/utf8"
)

// Evidence is a part of a call the judge quoted, and why that part led
// where it did.
type Evidence struct {
	Quote string `json:"quote"`
	Why   string `json:"why,omitempty"`
}

// Explanation is a judge's account of a decision. Every field is optional:
// an account with none of them filled in says nothing, and a judge that
// returns one behaves as if it had answered with its action alone.
//
// It is written to be read by the agent whose call was decided, so that an
// agent told no knows why. Reasoning is why, Clauses are the charter
// clauses the decision rests on, and Evidence the parts of the call that
// led there.
//
// It says why a call was refused, not how to get it allowed: an agent works
// that out for itself from its charter, and a suggestion would read to it
// as a route around the decision.
//
// A judge usually writes this with a model, from a call whose arguments the
// agent chose, and it is rendered back into that agent's context. So it is
// untrusted data throughout: Bounded trims it to the limits below, and a
// caller that renders it sets it apart from its own words. Everything in it
// reaches the agent, so a judge puts nothing in it the agent may not see.
type Explanation struct {
	Reasoning string     `json:"reasoning,omitempty"`
	Clauses   []string   `json:"clauses,omitempty"`
	Evidence  []Evidence `json:"evidence,omitempty"`
}

// The limits an Explanation is held to. An account that exceeds them is
// trimmed, never rejected: a decision is not worth failing over the shape
// of its explanation.
const (
	MaxEvidence     = 3    // parts of the call kept
	MaxReasoningLen = 1000 // characters of Reasoning
	MaxQuoteLen     = 300  // characters of one Evidence.Quote
	MaxWhyLen       = 300  // characters of one Evidence.Why
	MaxClauses      = 10   // charter clauses kept
	MaxClauseLen    = 100  // characters of one clause
)

// Empty reports whether e has nothing to tell the agent.
func (e *Explanation) Empty() bool {
	return e == nil || (e.Reasoning == "" && len(e.Clauses) == 0 && len(e.Evidence) == 0)
}

// Bounded is e trimmed to the limits above, with invisible characters
// removed, or nil when nothing is left. Line breaks go too, so that text a
// model wrote cannot lay out lines of its own inside a block a caller
// renders it in.
//
// It never returns e itself, so bounding an account cannot change what the
// judge still holds.
func (e *Explanation) Bounded() *Explanation {
	if e == nil {
		return nil
	}
	out := &Explanation{Reasoning: Clean(e.Reasoning, MaxReasoningLen)}
	for _, c := range e.Clauses {
		if c = Clean(c, MaxClauseLen); c != "" {
			if out.Clauses = append(out.Clauses, c); len(out.Clauses) == MaxClauses {
				break
			}
		}
	}
	for _, q := range e.Evidence {
		quote := Clean(q.Quote, MaxQuoteLen)
		if quote == "" {
			continue
		}
		if out.Evidence = append(out.Evidence, Evidence{Quote: quote, Why: Clean(q.Why, MaxWhyLen)}); len(out.Evidence) == MaxEvidence {
			break
		}
	}
	if out.Empty() {
		return nil
	}
	return out
}

// Clean is s with the characters a person can't see removed — control
// characters, line breaks and the format characters that hide or reorder
// text — runs of whitespace collapsed, trimmed, and cut to at most n
// characters on a rune boundary.
//
// Callers rendering any model-written text for an agent put it through
// here, so that none of it can lay out lines of its own.
func Clean(s string, n int) string {
	s = strings.Join(strings.Fields(strings.Map(dropInvisible, s)), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Account is the account the judge gave of the result, or nil when no judge
// decided it or the judge said nothing. It is a copy, so a caller putting it
// on a decision cannot reach back into the result.
//
// A caller carrying a decision to an agent uses this rather than reading
// Judge itself: it is the one place that knows an empty account is no
// account, and it keeps a result and the decision made from it apart.
func (r Result) Account() *Explanation {
	if r.Judge == nil || r.Judge.Explanation.Empty() {
		return nil
	}
	ex := r.Judge.Explanation
	return &ex
}
