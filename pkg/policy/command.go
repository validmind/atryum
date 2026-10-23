package policy

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// The "command" condition matches a shell command line by the commands in
// it, not by its text. contains and prefix compare text, so a condition
// "contains gh pr merge" also matches `curl x | sh; echo gh pr merge`, and
// "contains sh" matches `bash`, `ssh` and `curl x | sh`. command reads the
// line as a shell would split it and matches only when every simple command
// in it is one the condition names.
//
// Value is one pattern or a list of them. A pattern is a command's words as
// a shell would write them, `gh pr merge 3066` or `git commit -m 'fix the
// build'`, matched word for word and in full; a last word * (unquoted,
// after the program) matches any further words, so `gh pr merge *` matches
// every `gh pr merge`, flags included. A line matches when each of its
// simple commands, split on ; & && || | |& and line breaks, matches one of
// the patterns, in any order and any number of times.
//
// It is for allowing: a rule that denies or holds with it would miss a line
// that holds one more command (`rm -rf ~; true`) or anything it can't read.
// To deny or hold, use contains or prefix, which match more.
//
// The line is read conservatively: anything whose effect can't be known
// from its text alone makes it match nothing, so a rule that allows by
// command never allows more than its patterns say. That is any expansion
// ($VAR, $(…), `…`, ${…}, $'…'), a subshell or group ( ) { }, a ! , a
// reserved word or job (if, while, time, %1) where a command's name goes, a
// redirection other than between descriptors (2>&1) or to and from
// /dev/null, a here-document, an unterminated quote, and an unquoted glob
// in a word a pattern names: bash's * ? [ and zsh's ^, # inside a word and =
// starting one. The line is read as bash and zsh read it; an unquoted ~ is
// kept as written, and only matches a pattern's unquoted ~. Assignments
// (FOO=1 git push) and wrappers (env, sudo, xargs, sh -c) are words like
// any other: a pattern has to name them to match them.

// shellWord is one word of a simple command, quotes removed. globs are
// where it holds an unquoted glob character, which a shell would expand;
// tildes are where it holds an unquoted ~, which a shell may expand to a
// home directory, so `~` and `'~'` are different words.
type shellWord struct {
	text   string
	globs  []int
	tildes []int
}

func (w shellWord) glob() bool { return len(w.globs) > 0 }

var errUnreadable = errors.New("it holds an expansion, a redirection, a subshell or other shell syntax whose effect the text alone doesn't show")

// parseShell splits a command line into its simple commands. With
// redirects false, any redirection is refused (a pattern names commands,
// not where their output goes); with it true, the harmless ones are
// dropped and every other one is refused.
func parseShell(s string, redirects bool) ([][]shellWord, error) {
	// Control characters other than tab and line break: some runners cut a
	// line short at a NUL, and bash uses \x01 and \x7f to mark quoting,
	// so it doesn't read them back as written.
	if strings.IndexFunc(s, func(r rune) bool { return (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f }) >= 0 {
		return nil, errUnreadable
	}
	var (
		cmds    [][]shellWord
		cmd     []shellWord
		word    strings.Builder
		inWord  bool // a word is open, even an empty quoted one ('')
		globs   []int
		tildes  []int
		digits  bool // the word so far is only unquoted digits: a descriptor before < or >
		joined  bool // a line continuation since the last blank or separator
		redir   bool // the command so far has a redirection
		needCmd bool // after && || | |&: a command must follow
	)
	endWord := func() {
		if inWord {
			cmd = append(cmd, shellWord{text: word.String(), globs: globs, tildes: tildes})
			needCmd = false
		}
		word.Reset()
		inWord, globs, tildes, digits = false, nil, nil, false
	}
	// endCmd ends a simple command at a separator. empty is whether the
	// separator may follow no command: only a line break may.
	endCmd := func(empty bool) error {
		endWord()
		joined = false
		if len(cmd) == 0 {
			// zsh runs cat for a redirection alone (>/dev/null, <x).
			if empty && !redir {
				return nil
			}
			return fmt.Errorf("a command separator or redirection with no command")
		}
		redir = false
		if name := cmd[0].text; reserved[name] || strings.HasPrefix(name, "%") {
			return errUnreadable
		}
		cmds = append(cmds, cmd)
		cmd = nil
		return nil
	}
	for i := 0; i < len(s); {
		ch := s[i]
		next := byte(0)
		if i+1 < len(s) {
			next = s[i+1]
		}
		switch {
		case ch == ' ' || ch == '\t':
			endWord()
			joined = false
			i++
		case ch == '\n':
			if err := endCmd(true); err != nil {
				return nil, err
			}
			i++
		case ch == ';':
			endWord()
			if needCmd || next == ';' {
				return nil, errUnreadable
			}
			if err := endCmd(false); err != nil {
				return nil, err
			}
			i++
		case ch == '&' && next == '>':
			// zsh reads 7&> as descriptor 7's: never right after a word.
			if inWord || joined {
				return nil, errUnreadable
			}
			redir = true
			n, err := redirection(s, i, redirects)
			if err != nil {
				return nil, err
			}
			i = n
		case ch == '&' || ch == '|':
			endWord()
			joined = false
			if needCmd {
				return nil, errUnreadable
			}
			if err := endCmd(false); err != nil {
				return nil, err
			}
			i++
			if next == ch || (ch == '|' && next == '&') {
				i++
				needCmd = true
			} else if ch == '|' {
				needCmd = true
			}
		case ch == '<' || ch == '>':
			// Shells differ on what a word right before it is: bash takes any
			// number as a descriptor (3066>), zsh only one digit, and a line
			// continuation joins whatever is around it. So a redirection
			// follows a blank or separator, or a lone 0, 1 or 2 (2>).
			if joined || (inWord && !(digits && word.Len() == 1 && word.String() <= "2")) {
				return nil, errUnreadable
			}
			word.Reset()
			inWord, digits, redir = false, false, true
			n, err := redirection(s, i, redirects)
			if err != nil {
				return nil, err
			}
			i = n
		case ch == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errUnreadable
			}
			word.WriteString(s[i+1 : i+1+end])
			inWord, digits = true, false
			i += end + 2
		case ch == '"':
			n, err := doubleQuoted(s, i+1, &word)
			if err != nil {
				return nil, err
			}
			inWord, digits = true, false
			i = n
		case ch == '\\':
			switch {
			case next == '\n': // a line continuation
				joined = true
				i += 2
			case i+1 < len(s):
				word.WriteByte(next)
				inWord, digits = true, false
				i += 2
			default:
				return nil, errUnreadable
			}
		case ch == '#' && !inWord:
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case strings.IndexByte("$`(){}!", ch) >= 0:
			return nil, errUnreadable
		case ch == '*' || ch == '?' || ch == '[' || ch == '^' || (ch == '#' && inWord) || (ch == '=' && !inWord):
			// zsh also expands ^x, x# and =cmd, with the options that turn them on.
			globs = append(globs, word.Len())
			word.WriteByte(ch)
			inWord, digits = true, false
			i++
		case ch == '~':
			tildes = append(tildes, word.Len())
			word.WriteByte(ch)
			inWord, digits = true, false
			i++
		default:
			digits = ch >= '0' && ch <= '9' && (!inWord || digits)
			word.WriteByte(ch)
			inWord = true
			i++
		}
	}
	endWord()
	if needCmd {
		return nil, errUnreadable
	}
	if err := endCmd(true); err != nil {
		return nil, err
	}
	if len(cmds) == 0 {
		return nil, fmt.Errorf("no command")
	}
	return cmds, nil
}

// redirection reads the redirection at s[i] (its operator and target) and
// returns where it ends. Only these are harmless: making a descriptor a
// copy of stdin, stdout or stderr (2>&1, >&2), and output to or input
// from /dev/null. Closing one (>&-) isn't: the next file the program opens
// would take its place.
func redirection(s string, i int, allowed bool) (int, error) {
	if !allowed {
		return 0, errUnreadable
	}
	op := ""
	for _, o := range []string{"&>>", "&>", ">>", ">|", ">&", "<&", "<<", "<>", ">", "<"} {
		if strings.HasPrefix(s[i:], o) {
			op = o
			break
		}
	}
	if op == "<<" || op == "<>" {
		return 0, errUnreadable // here-documents and here-strings; read-write opens
	}
	j := i + len(op)
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	start := j
	for j < len(s) && strings.IndexByte(" \t\n;&|<>()", s[j]) < 0 {
		if strings.IndexByte("'\"\\$`*?[{}!", s[j]) >= 0 {
			return 0, errUnreadable
		}
		j++
	}
	target := s[start:j]
	switch {
	case op == ">&" || op == "<&":
		// Only the standard three: another descriptor is whatever the runner
		// left open there.
		if target == "0" || target == "1" || target == "2" {
			return j, nil
		}
	case target == "/dev/null":
		return j, nil
	}
	return 0, errUnreadable
}

// doubleQuoted reads a double-quoted string from s[i], just past its
// opening quote, into w and returns where it ends. An expansion in it ($,
// `) makes the line unreadable.
func doubleQuoted(s string, i int, w *strings.Builder) (int, error) {
	for i < len(s) {
		switch ch := s[i]; ch {
		case '"':
			return i + 1, nil
		case '$', '`':
			return 0, errUnreadable
		case '\\':
			if i+1 >= len(s) {
				return 0, errUnreadable
			}
			switch next := s[i+1]; next {
			case '\n':
			case '$', '`', '"', '\\':
				w.WriteByte(next)
			default:
				w.WriteByte('\\')
				w.WriteByte(next)
			}
			i += 2
		default:
			w.WriteByte(ch)
			i++
		}
	}
	return 0, errUnreadable
}

// commandPattern is a parsed pattern: the words it names, the index of its
// program among them, and whether a last * lets further words follow.
type commandPattern struct {
	words   []shellWord
	program int
	open    bool
}

func parsePattern(p string) (commandPattern, error) {
	cmds, err := parseShell(p, false)
	if err != nil {
		return commandPattern{}, fmt.Errorf("pattern %q: %w", p, err)
	}
	if len(cmds) != 1 {
		return commandPattern{}, fmt.Errorf("pattern %q: one command per pattern; give several as a list", p)
	}
	words := cmds[0]
	out := commandPattern{program: -1}
	for i, w := range words {
		switch {
		case !w.glob():
			out.words = append(out.words, w)
			if out.program < 0 && !shellAssignment.MatchString(w.text) {
				out.program = i
			}
		case w.text == "*" && i == len(words)-1 && out.program >= 0:
			out.open = true
		default:
			return commandPattern{}, fmt.Errorf("pattern %q: only a last word * may stand for other words, after the program; quote %q to match it as written", p, w.text)
		}
	}
	// A pattern names its program: `FOO=1 *` would match every command.
	if out.program < 0 {
		return commandPattern{}, fmt.Errorf("pattern %q: name the command's program, not only variables for it", p)
	}
	return out, nil
}

// reserved are the words that, as a command's first word, make it something
// other than a simple command: bash's and zsh's reserved words and
// precommand modifiers. A first word starting with % is a job (%1 is fg %1).
var reserved = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "case": true, "esac": true,
	"for": true, "select": true, "while": true, "until": true, "do": true, "done": true, "in": true,
	"function": true, "time": true, "coproc": true, "[[": true, "]]": true,
	"repeat": true, "foreach": true, "end": true, "nocorrect": true, "noglob": true, "-": true,
}

// shellAssignment is a word a shell may read as setting a variable for the
// command after it: FOO=1, PATH+=:x, and in zsh a positional one, 1=x.
var shellAssignment = regexp.MustCompile(`^[A-Za-z0-9_]+\+?=`)

func (p commandPattern) matches(cmd []shellWord) bool {
	if len(cmd) < len(p.words) || (!p.open && len(cmd) != len(p.words)) {
		return false
	}
	for i, w := range p.words {
		// A word the shell would expand is never one the pattern names.
		if cmd[i].glob() || cmd[i].text != w.text || !slices.Equal(cmd[i].tildes, w.tildes) {
			return false
		}
	}
	return true
}

// commandPatterns reads a command condition's value: one pattern, or a
// non-empty list of them.
func commandPatterns(v any) ([]commandPattern, error) {
	var texts []string
	switch x := v.(type) {
	case string:
		texts = []string{x}
	default:
		list := reflect.ValueOf(v)
		if v == nil || (list.Kind() != reflect.Slice && list.Kind() != reflect.Array) || list.Len() == 0 {
			return nil, fmt.Errorf("command needs a pattern or a list of patterns")
		}
		for i := 0; i < list.Len(); i++ {
			s, ok := list.Index(i).Interface().(string)
			if !ok {
				return nil, fmt.Errorf("command's patterns must be text")
			}
			texts = append(texts, s)
		}
	}
	out := make([]commandPattern, 0, len(texts))
	for _, t := range texts {
		p, err := parsePattern(t)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// CommandPattern is one of a command condition's patterns as the engine
// reads it: the words it names, quotes removed, with the program at
// Words[Program] (after any FOO=1 set for it), and whether a last * lets
// any further words follow.
type CommandPattern struct {
	Words   []string
	Program int
	Open    bool
}

// ParseCommandValue reads v as a command condition's value: one pattern or
// a list of them, each a single command that names its program. An
// embedder checks it before saving a rule, since a value that isn't one
// matches nothing, and can read the patterns to judge how broad they are.
func ParseCommandValue(v any) ([]CommandPattern, error) {
	pats, err := commandPatterns(v)
	if err != nil {
		return nil, err
	}
	out := make([]CommandPattern, len(pats))
	for i, p := range pats {
		out[i] = CommandPattern{Words: texts(p.words), Program: p.program, Open: p.open}
	}
	return out, nil
}

// ParseCommandLine reads a shell command line as a command condition does:
// its simple commands, each as its words with quotes removed, or an error
// when the line holds anything whose effect its text alone doesn't show
// (and a command condition matches nothing). An embedder uses it to offer
// conditions a line will match.
func ParseCommandLine(line string) ([][]string, error) {
	cmds, err := parseShell(line, true)
	if err != nil {
		return nil, err
	}
	out := make([][]string, len(cmds))
	for i, c := range cmds {
		out[i] = texts(c)
	}
	return out, nil
}

func texts(words []shellWord) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w.text
	}
	return out
}

// matchCommand reports whether line, a shell command line, consists only of
// commands that the patterns in value match.
func matchCommand(line any, value any) bool {
	s, ok := line.(string)
	if !ok {
		return false
	}
	pats, err := commandPatterns(value)
	if err != nil {
		return false
	}
	cmds, err := parseShell(s, true)
	if err != nil {
		return false
	}
	for _, cmd := range cmds {
		matched := false
		for _, p := range pats {
			if p.matches(cmd) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
