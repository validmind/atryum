package policy

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Shapes that are a secret wherever they appear.
// ponytail: a short list of common shapes, not a scanner; it misses
// high-entropy strings with no telltale prefix. Swap in a gitleaks-style rule
// set if the pilot needs broader coverage.
var secretShape = regexp.MustCompile(`` +
	`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b` + // AWS access key ID
	`|-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----` + // PEM, OpenSSH, PGP
	`|\bgh[pousr]_[A-Za-z0-9]{36,}|\bgithub_pat_[A-Za-z0-9_]{22,}` +
	`|\bxox[abprs]-[A-Za-z0-9-]{10,}` + // Slack
	`|\bsk-[A-Za-z0-9_-]{20,}` + // Anthropic (sk-ant-), OpenAI
	`|\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}` + // JWT
	`|(?i:\bbearer\s+[A-Za-z0-9\-._~+/]{20,}=*)`)

// urlCredential captures the password in scheme://user:password@host.
var urlCredential = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s/:@]+:([^\s/@]+)@`)

// secretKeyName matches names like password, DB_PASSWORD, OPENAI_API_KEY,
// aws_secret_access_key or refresh-token; notSecretKey excludes names that
// only refer to a secret (secret_name, api_key_id, password_file).
var (
	secretKeyName = regexp.MustCompile(`(?i)password|passwd|secret|api[_-]?key|access[_-]?key|private[_-]?key|(?:access|auth|refresh|api|session|bearer)[_-]?token`)
	notSecretKey  = regexp.MustCompile(`(?i)[_.-](?:name|id|ref|arn|path|file|url|length|len|policy|type|count|reset|expires?)$`)
)

// assignment captures key and value in key=value, key: value and "key":"value"
// inside free text such as a shell command or a config file.
var assignment = regexp.MustCompile(`([A-Za-z0-9_.-]+)["']?\s*[:=]\s*["']?([^\s"',;&]+)`)

// placeholder is a value that stands in for a secret rather than being one.
var placeholder = regexp.MustCompile(`(?i)^(?:true|false|null|nil|none|undefined|redacted|\[redacted\]|changeme)$|^\$\{.*\}$|^\$[A-Za-z_][A-Za-z0-9_]*$|^<.*>$|^\{\{.*\}\}$|^%\(.*\)s$|\*\*\*|^[*xX•.#-]+$`)

// ContainsSecret reports whether v (typically a call's arguments) contains a
// secret literal. Embedders set it as facts.contains_secret. A value that
// cannot be inspected counts as containing one.
func ContainsSecret(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return true
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		return true
	}
	return containsSecret(tree)
}

func containsSecret(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && secretKey(k) && plausibleSecret(s) {
				return true
			}
			if containsSecret(val) {
				return true
			}
		}
	case []any:
		for _, it := range x {
			if containsSecret(it) {
				return true
			}
		}
	case string:
		return secretInText(x)
	}
	return false
}

func secretInText(s string) bool {
	if secretShape.MatchString(s) {
		return true
	}
	for _, m := range urlCredential.FindAllStringSubmatch(s, -1) {
		if !placeholder.MatchString(m[1]) {
			return true
		}
	}
	for _, m := range assignment.FindAllStringSubmatch(s, -1) {
		if secretKey(m[1]) && plausibleSecret(m[2]) {
			return true
		}
	}
	return false
}

func secretKey(k string) bool { return secretKeyName.MatchString(k) && !notSecretKey.MatchString(k) }

// plausibleSecret rejects values too short to be a credential and values that
// are booleans, nulls, masks or placeholders.
func plausibleSecret(v string) bool {
	v = strings.TrimSpace(v)
	return len(v) >= 8 && !placeholder.MatchString(v)
}
