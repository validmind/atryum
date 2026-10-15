package policy

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
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

// assignment captures key and value in key=value, key: value and "key":"value"
// inside free text such as a shell command or a config file; spacedFlag
// captures them in "--password value" and "aws_secret_access_key value",
// where the key is a flag or contains an underscore (never plain prose).
var (
	assignment = regexp.MustCompile(`([A-Za-z0-9_.-]+)["']?\s*[:=]\s*["']?([^\s"',;&]+)`)
	spacedFlag = regexp.MustCompile(`(?:^|\s)(--?[A-Za-z][A-Za-z0-9_-]*|[A-Za-z0-9]+_[A-Za-z0-9_]+)\s+["']?([^\s"',;&]+)`)
)

// Command-line passwords with no key name: mysql -pSECRET, curl -u user:pass.
var (
	mysqlPassword = regexp.MustCompile(`\bmysql(?:dump|admin)?\b[^|;&\n]*?\s-p([^\s"']+)`)
	curlUser      = regexp.MustCompile(`\bcurl\b[^|;&\n]*?\s(?:-u|--user)[ =]?["']?[^\s:"']+:([^\s"']+)`)
)

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
	for _, re := range []*regexp.Regexp{mysqlPassword, curlUser} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if !placeholder.MatchString(m[1]) {
				return true
			}
		}
	}
	for _, re := range []*regexp.Regexp{assignment, spacedFlag} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if secretKey(m[1]) && plausibleSecret(m[2]) {
				return true
			}
		}
	}
	return false
}

// secretWord is a word that names a secret value; valueWord may follow it
// in a name that still holds the value (secret_access_key, PASSWORD_PLAIN).
var (
	secretWord = map[string]bool{"password": true, "passwd": true, "passphrase": true, "secret": true, "apikey": true, "token": true}
	valueWord  = map[string]bool{"key": true, "access": true, "value": true, "secret": true, "token": true, "string": true, "plain": true, "plaintext": true, "literal": true, "raw": true, "b64": true, "base64": true, "hex": true}
)

// secretKey reports whether the name k labels a secret value. k is split into
// words at camelCase, _, -, . and /; it holds a secret when a secret word
// (or api/access/private followed by key) is followed only by value words.
// So password, DB_PASSWORD, clientSecret, OPENAI_API_KEY and
// aws_secret_access_key hold one, while names that go on to say what they
// point at (secretName, secretKeyRef, api_key_id, password_file,
// secret.reloader.stakater.com/reload, nextPageToken) and Helm's
// existingSecret do not. Words match whole, so secretary is not secret.
func secretKey(k string) bool {
	w := keyWords(k)
	if len(w) == 0 || w[0] == "existing" {
		return false
	}
	for i, word := range w {
		named := secretWord[word] && (word != "token" || i == 0 || w[i-1] != "page")
		if word == "key" && i > 0 && (w[i-1] == "api" || w[i-1] == "access" || w[i-1] == "private") {
			named = true
		}
		if !named {
			continue
		}
		rest := w[i+1:]
		for len(rest) > 0 && valueWord[rest[0]] {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return true
		}
	}
	return false
}

// keyWords splits a name into lower-case words at non-alphanumerics and
// camelCase boundaries: "tlsSecretName" -> tls secret name, "APIKey" -> api key.
func keyWords(k string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	rs := []rune(k)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if unicode.IsUpper(r) && i > 0 {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, unicode.ToLower(r))
	}
	flush()
	return words
}

// plausibleSecret rejects values too short to be a credential and values that
// are booleans, nulls, masks or placeholders.
func plausibleSecret(v string) bool {
	v = strings.TrimSpace(v)
	return len(v) >= 8 && !placeholder.MatchString(v)
}
