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
//  2. %XX escapes are decoded (up to three times, for "%252F"), and step 1 is
//     repeated on what they decode to.
//  3. Invisible and control characters are dropped (zero-width spaces, NUL).
//  4. Lower case.
//  5. Runs of white space become one space, with none at either end.
//  6. A "file://" prefix is dropped.
//  7. If it looks like a path, it is cleaned by cleanPath: "//a", "/a/./b"
//     and "/x/../a/b" lose their detours, and so do Windows spellings.
func canonicalText(s string) string {
	s = plainText(s)
	if strings.ContainsAny(s, `/\`) {
		s = cleanPath(s)
	}
	return s
}

// plainText is steps 1 to 6 of canonicalText: the text with its spelling
// settled but a path's detours still in it.
func plainText(s string) string {
	s = norm.NFKC.String(s)
	for i := 0; i < 3 && strings.Contains(s, "%"); i++ {
		decoded, err := url.PathUnescape(s)
		if err != nil || decoded == s {
			break
		}
		s = norm.NFKC.String(decoded) // "%EF%BC%8F" is a full-width "/"
	}
	s = strings.Map(dropInvisible, s)
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimPrefix(s, "file://")
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
// it to "/etc" would also match "/etcetera". s is already lower case.
//
// A Windows path gets what Windows itself does to it before opening it:
//   - the "\\?\" and "\\.\" prefixes are dropped ("\\?\UNC\host" is "\\host");
//   - a drive is a root, so "C:\..\Windows" is "c:/windows", not "windows";
//   - trailing dots and spaces leave each name: "C:\Windows. \x" is "c:/windows/x";
//   - a stream suffix is dropped: "SAM::$DATA" is "sam".
//
// Short names ("PROGRA~1"), shares ("\\host\C$"), environment variables and
// links name the same file in ways text alone can't tell.
func cleanPath(s string) string {
	windows := strings.Contains(s, `\`)
	s = strings.ReplaceAll(s, `\`, "/")
	for _, p := range []string{"//?/unc/", "//./unc/"} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			s, windows = "//"+rest, true
		}
	}
	for _, p := range []string{"//?/", "//./", "/??/"} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			s, windows = rest, true
		}
	}
	drive := ""
	if len(s) >= 3 && 'a' <= s[0] && s[0] <= 'z' && s[1] == ':' && s[2] == '/' {
		drive, s, windows = s[:2], s[2:], true
	}
	hadSlash := strings.HasSuffix(s, "/")
	if windows {
		names := strings.Split(s, "/")
		for i, name := range names {
			names[i] = windowsName(name)
		}
		s = strings.Join(names, "/")
	}
	s = path.Clean(s)
	if hadSlash && !strings.HasSuffix(s, "/") {
		s += "/"
	}
	return drive + s
}

// windowsName is one name of a Windows path as Windows opens it: without a
// stream suffix ("sam::$data", "windows:$i30:$index_allocation") and without
// trailing dots and spaces, except that "." and ".." keep their meaning.
func windowsName(name string) string {
	if i := strings.Index(name, ":$"); i >= 0 {
		name = strings.TrimRight(name[:i], ":")
	}
	if t := strings.TrimRight(name, " "); t == "." || t == ".." {
		return t
	}
	return strings.TrimRight(name, ". ")
}

// cleaner cleans the text of one call for the second pass of Evaluate. It
// cleans only the arguments a rule reads, and remembers what it cleaned,
// because many rules read the same argument and arguments can be large.
type cleaner struct {
	done map[string]string
}

func newCleaner() *cleaner { return &cleaner{done: map[string]string{}} }

func (cl *cleaner) text(s string) string {
	if t, ok := cl.done[s]; ok {
		return t
	}
	t := canonicalText(s)
	cl.done[s] = t
	return t
}

// value cleans every text value and key in v. Numbers, booleans and null are
// left alone, and v is never changed.
//
// Two keys of a map value that clean to the same name ("Path" and "path")
// collapse into one; the key that sorts first wins, so the result is the
// same on every run. Conditions don't depend on this: args finds every key.
func (cl *cleaner) value(v any) any {
	switch x := v.(type) {
	case string:
		return cl.text(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cl.value(item)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			name := cl.text(k)
			if _, taken := out[name]; !taken {
				out[name] = cl.value(x[k])
			}
		}
		return out
	default:
		return v
	}
}

// args returns the cleaned value of every argument that "args.a.b" names
// once keys are cleaned. There can be more than one: an agent that sends
// both "path" and "PATH" must not hide one behind the other, so a condition
// holds when it holds for any of them.
func (cl *cleaner) args(field string, args map[string]any) []any {
	rest, ok := strings.CutPrefix(field, "args.")
	if !ok || rest == "" {
		return nil
	}
	found := []any{args}
	for _, name := range strings.Split(rest, ".") {
		name = cl.text(name)
		var next []any
		for _, v := range found {
			m, _ := v.(map[string]any)
			for k, item := range m {
				if item != nil && cl.text(k) == name {
					next = append(next, item)
				}
			}
		}
		found = next
	}
	for i, v := range found {
		found[i] = cl.value(v)
	}
	return found
}
