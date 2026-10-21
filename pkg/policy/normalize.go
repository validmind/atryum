package policy

// Why this file exists
//
// A condition compares text literally. A rule such as
//
//	read_file when args.path prefix "/etc/"  ->  deny
//
// matches "/etc/passwd" but not "//etc/passwd", "/tmp/../etc/passwd",
// "/ETC/passwd" or "%2Fetc%2Fpasswd", although they all name the same file.
// When the deny rule misses, a broader allow rule can let the call run.
//
// So Evaluate decides every call twice: as sent, and again with the
// arguments (and the rules' own values) cleaned by the functions below. It
// keeps the stricter answer (see Evaluate). Cleaning can only make a
// decision stricter, never more permissive.

import (
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// canonicalText is the one cleaned-up spelling of a piece of text, so that
// different spellings of the same thing compare equal. In order:
//
//  1. Unicode compatibility form (NFKC): full-width "ｆｏｒｂｉｄｄｅｎ" becomes "forbidden".
//  2. %XX escapes are decoded (up to three times, for "%252F").
//  3. Invisible and control characters are dropped (zero-width spaces, NUL).
//  4. Lower case.
//  5. Runs of white space become one space, with none at either end.
//  6. A "file://" prefix is dropped.
//  7. If it looks like a path, "\" becomes "/" and the path is cleaned:
//     "//a", "/a/./b" and "/x/../a/b" lose their detours.
func canonicalText(s string) string {
	s = norm.NFKC.String(s)
	for i := 0; i < 3 && strings.Contains(s, "%"); i++ {
		decoded, err := url.PathUnescape(s)
		if err != nil || decoded == s {
			break
		}
		s = decoded
	}
	s = strings.Map(dropInvisible, s)
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimPrefix(s, "file://")
	if strings.ContainsAny(s, `/\`) {
		s = cleanPath(s)
	}
	return s
}

// dropInvisible is a strings.Map function: it returns -1 (drop) for
// characters a person can't see, and the character itself otherwise.
func dropInvisible(r rune) rune {
	if unicode.IsSpace(r) {
		return r // tabs and new lines are spaces, not invisible: step 5 turns them into one space
	}
	if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) {
		return -1
	}
	return r
}

// cleanPath turns "\" into "/" and removes "//", "." and ".." detours. A
// trailing "/" is kept, because rules often say prefix "/etc/" and cleaning
// it to "/etc" would also match "/etcetera".
func cleanPath(s string) string {
	s = strings.ReplaceAll(s, `\`, "/")
	hadSlash := strings.HasSuffix(s, "/")
	s = path.Clean(s)
	if hadSlash && !strings.HasSuffix(s, "/") {
		s += "/"
	}
	return s
}

// canonicalArgs returns a copy of a call's arguments with every text value
// and every key cleaned by canonicalText. Numbers, booleans and null are
// left alone, and the original is never changed.
//
// Known limit: two keys that clean to the same name ("Path" and "path")
// collapse into one. The key that sorts first wins, so the result is the
// same on every run.
func canonicalArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	return canonicalValue(args).(map[string]any)
}

func canonicalValue(v any) any {
	switch x := v.(type) {
	case string:
		return canonicalText(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = canonicalValue(item)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // a fixed order, so a key collision resolves the same way every time
		out := make(map[string]any, len(x))
		for _, k := range keys {
			name := canonicalText(k)
			if _, taken := out[name]; !taken {
				out[name] = canonicalValue(x[k])
			}
		}
		return out
	default:
		return v
	}
}

// canonicalField cleans the path part of "args.a.b" the same way argument
// keys are cleaned ("args" and "facts" are already lower case).
func canonicalField(field string) string {
	parts := strings.Split(field, ".")
	for i, p := range parts {
		parts[i] = canonicalText(p)
	}
	return strings.Join(parts, ".")
}
