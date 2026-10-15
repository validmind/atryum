package policy

import (
	"encoding/json"
	"regexp"
)

// secretPattern matches well-known secret shapes: AWS access key IDs, PEM
// private-key blocks, GitHub and Slack tokens, bearer tokens, and generic
// password=, secret: or api_key= assignments (quoted, as in JSON, or not).
// ponytail: a short list of common shapes, not a scanner; it misses
// high-entropy strings with no telltale prefix. Swap in a gitleaks-style rule
// set if the pilot needs broader coverage.
var secretPattern = regexp.MustCompile(`(?i)` +
	`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b` +
	`|-----BEGIN (?:[A-Z]+ )*PRIVATE KEY-----` +
	`|\bgh[pousr]_[A-Za-z0-9]{36,}` +
	`|\bxox[abprs]-[A-Za-z0-9-]{10,}` +
	`|\bbearer\s+[A-Za-z0-9\-._~+/]{20,}=*` +
	`|\b(?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret)\\?["']?\s*[:=]\s*\\?["']?[^\s"'\\,;&}]{4,}`)

// ContainsSecret reports whether v (typically a call's arguments) contains a
// secret literal. Embedders set it as facts.contains_secret.
func ContainsSecret(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return secretPattern.Match(b)
}
