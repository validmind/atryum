package policy

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Cleaning (normalize.go) settles how a path is spelled, but some paths name
// a file only once the machine that opens them resolves them: a variable, a
// home directory, a Windows short name, a share, a path relative to a working
// directory nobody here knows, or a /proc link. A rule can't tell where they
// lead, so AmbiguousPath flags them for a rule that sends them to a person.
var (
	// %SystemRoot%, %ProgramFiles(x86)%, $HOME, ${HOME}, $env:USERPROFILE
	pathVariable = regexp.MustCompile(`%[a-z_][a-z0-9_()]*%|\$\{?[a-z_][a-z0-9_]*`)
	// PROGRA~1, MICROS~2.TXT: a Windows 8.3 short name
	shortName = regexp.MustCompile(`^[^~ ]{1,6}~[0-9]+(\.[^.]*)?$`)
	// \\?\C:\ and \\.\C:\ are only another spelling of C:\ (cleanPath drops them)
	driveDevice = regexp.MustCompile(`^(//[?.]/|/\?\?/)([a-z]:/)`)
	// C:foo is relative to C:'s own working directory
	driveRelative = regexp.MustCompile(`^[a-z]:([^/ ]|$)`)
	// /proc/self/root/etc/passwd is /etc/passwd; /dev/fd/3 is whatever is open
	procLink = regexp.MustCompile(`^/proc/(self|thread-self|[0-9]+)/(root|cwd|fd|map_files)(/|$)|^/dev/fd(/|$)`)
)

// AmbiguousPath reports whether v (typically a call's arguments, or just its
// path arguments) holds a path that only the machine running the tool can
// resolve: a variable (%SystemRoot%, $HOME), a leading "~", a Windows short
// name (PROGRA~1), a share or device (\\host\C$, \\.\pipe), a drive-relative
// path (C:foo), a relative path that climbs out of where it starts
// (../../etc), or a /proc link. Embedders set it as facts.ambiguous_path, and
// a firm rule on file tools sends such calls to a person. A value that
// cannot be inspected counts as ambiguous.
//
// Only single-line text is read as a path, so file contents and messages
// don't raise it; scope the rule to tools whose arguments are paths.
func AmbiguousPath(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return true
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		return true
	}
	return ambiguousIn(tree)
}

func ambiguousIn(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for _, val := range x {
			if ambiguousIn(val) {
				return true
			}
		}
	case []any:
		for _, it := range x {
			if ambiguousIn(it) {
				return true
			}
		}
	case string:
		return ambiguousPath(x)
	}
	return false
}

func ambiguousPath(s string) bool {
	if strings.ContainsAny(s, "\n\r") {
		return false // free text, not a path
	}
	s = strings.ReplaceAll(plainText(s), `\`, "/")
	s = driveDevice.ReplaceAllString(s, "$2") // "\\?\C:\" is only another spelling of "C:\"
	switch {
	case s == "":
		return false
	case pathVariable.MatchString(s), strings.HasPrefix(s, "~"):
		return true
	case strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/??/"):
		return true // a share, a device or a pipe
	case driveRelative.MatchString(s):
		return true
	}
	for _, name := range strings.Split(s, "/") {
		if shortName.MatchString(name) {
			return true
		}
	}
	cleaned := cleanPath(s)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return true
	}
	return procLink.MatchString(cleaned)
}
