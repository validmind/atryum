package policy

// Holds reports whether every condition in when holds for c in each of the
// passes Evaluate takes: on the arguments as sent, cleaned up (case,
// spaces, %XX escapes, invisible characters; see plainText), and with
// paths cleaned too (detours, doubled slashes, Windows spellings; see
// canonicalText). Facts are compared as they are.
//
// Evaluate needs one pass to match: it takes the strictest answer, so a
// spelling that matches a deny once cleaned denies. An embedder that lets
// a person allow calls by what they say, such as a session approval on
// "args.path prefix /tmp/", needs the opposite, and asks here: the call is
// allowed only when the condition holds in every pass, so a spelling that
// names something else once cleaned ("/tmp/../etc/passwd" is
// "/etc/passwd"; "/tmp/%2E%2E/etc/passwd" is the same, escaped) is not
// covered, and goes to a person as it would without the allowance.
//
// A spelling that fails as sent is never allowed, even when it would match
// once cleaned ("/TMP/x" for "prefix /tmp/"): an allowance is read as it
// was written.
func Holds(when []Cond, c Call) bool {
	for _, clean := range []*cleaner{nil, newCleaner(false), newCleaner(true)} {
		for _, cond := range when {
			if !condMatches(cond, c, clean) {
				return false
			}
		}
	}
	return true
}
