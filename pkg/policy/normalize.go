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
// So Evaluate decides every call three times: as sent, with the arguments
// (and the rules' own values) cleaned by plainText, and with them cleaned by
// canonicalText, which also cleans paths. It keeps the strictest answer (see
// Evaluate). Cleaning can only make a decision stricter, never more
// permissive.

import (
	"path"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// canonicalText is the one cleaned-up spelling of a piece of text, so that
// different spellings of the same thing compare equal: plainText, and then,
// if the text holds a slash, cleanPath. Evaluate decides a call with each
// of the two, because a shell command holds slashes too and cleaning it as
// a path can take away the words a rule looks for.
func canonicalText(s string) string {
	s = plainText(s)
	if strings.ContainsAny(s, `/\`) {
		s = cleanPath(s)
	}
	return s
}

// plainText settles how text is spelled. In order:
//
//  1. Unicode compatibility form (NFKC): full-width "ｆｏｒｂｉｄｄｅｎ" becomes "forbidden".
//  2. %XX escapes are decoded (up to three times, for "%252F"), and step 1 is
//     repeated on what they decode to. A "%" that isn't an escape is kept,
//     so "100%" and "%2Fetc%" are decoded as far as they go.
//  3. Invisible and control characters are dropped (zero-width spaces, NUL).
//  4. Lower case.
//  5. Runs of white space become one space, with none at either end.
//  6. A "file:" URL becomes its path: "file:///etc/x", "file://localhost/etc/x"
//     and "file:/etc/x" are "/etc/x", and "file:///C:/x" is "c:/x".
func plainText(s string) string {
	s = norm.NFKC.String(s)
	for i := 0; i < 3 && strings.Contains(s, "%"); i++ {
		decoded := unescape(s)
		if decoded == s {
			break
		}
		s = norm.NFKC.String(decoded) // "%EF%BC%8F" is a full-width "/"
	}
	s = strings.Map(dropInvisible, s)
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	if rest, ok := strings.CutPrefix(s, "file:"); ok && strings.HasPrefix(rest, "/") {
		s = strings.TrimPrefix(rest, "//localhost")
	}
	return s
}

// unescape decodes every valid %XX escape in s and keeps the rest as it is,
// the way lenient decoders do (url.PathUnescape gives up on the first bad one).
func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if hi, lo := unhex(s[i+1]), unhex(s[i+2]); hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
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
//   - a drive is a root, so "C:\..\Windows" is "c:/windows", not "windows",
//     and "/C:/x" (from a file URL) is "c:/x";
//   - a share keeps its two leading slashes, so "\\host\share" is not "/host/share";
//   - trailing dots and spaces leave each name: "C:\Windows. \x" is "c:/windows/x";
//   - a stream suffix is dropped: "SAM::$DATA" and "SAM:evil" are "sam".
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
	if t := strings.TrimLeft(s, "/"); len(t) >= 3 && isDrive(t) {
		s = t // "/c:/x" or "///c:/x", as a file URL spells a drive
	}
	drive, share := "", ""
	switch {
	case len(s) >= 3 && isDrive(s):
		drive, s, windows = s[:2], s[2:], true
	case windows && strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "///"):
		share, s = "/", s[1:]
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
	return share + drive + s
}

// isDrive reports whether s starts with a drive root such as "c:/".
func isDrive(s string) bool {
	return 'a' <= s[0] && s[0] <= 'z' && s[1] == ':' && s[2] == '/'
}

// windowsName is one name of a Windows path as Windows opens it: without a
// stream ("sam::$data", "sam:evil", "windows:$i30:$index_allocation") and
// without trailing dots and spaces, except that "." and ".." keep their meaning.
func windowsName(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	if t := strings.TrimRight(name, " "); t == "." || t == ".." {
		return t
	}
	return strings.TrimRight(name, ". ")
}

// cleaner cleans the text of one call for a later pass of Evaluate, either
// by plainText alone or by canonicalText (paths cleaned too). It cleans only
// the arguments a rule reads, and remembers what it cleaned, because many
// rules read the same argument and arguments can be large.
type cleaner struct {
	paths bool
	done  map[string]string
}

func newCleaner(paths bool) *cleaner { return &cleaner{paths: paths, done: map[string]string{}} }

func (cl *cleaner) text(s string) string {
	if t, ok := cl.done[s]; ok {
		return t
	}
	t := plainText(s)
	if cl.paths {
		t = canonicalText(t)
	}
	cl.done[s] = t
	return t
}

// args returns every argument that "args.a.b" names once keys are cleaned.
// There can be more than one: an agent that sends both "path" and "PATH"
// must not hide one behind the other, so a condition holds when it holds
// for any of them. The values are returned as sent; compare cleans them.
func (cl *cleaner) args(field string, args map[string]any) []any {
	rest, ok := strings.CutPrefix(field, "args.")
	if !ok || rest == "" {
		return nil
	}
	found := []any{args}
	for _, part := range strings.Split(rest, ".") {
		name := cl.text(part)
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
	return found
}
