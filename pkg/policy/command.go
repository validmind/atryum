package policy

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// ErrUnreadable is why a command condition matches a line, or reads a
// pattern, as nothing: it holds shell syntax whose effect its text alone
// doesn't show. The error that wraps it says which.
var ErrUnreadable = errors.New("the command line holds shell syntax whose effect its text alone doesn't show")

func unreadable(what string) error { return fmt.Errorf("%w: %s", ErrUnreadable, what) }

// shellWord is one word of a simple command, quotes removed. glob is whether
// it holds an unquoted glob character, which a shell would expand; tildes
// are where it holds an unquoted ~, which a shell may expand to a home
// directory, so `~` and `'~'` are different words; quoted is where its
// first quoted or escaped character is, or -1.
type shellWord struct {
	text   string
	glob   bool
	tildes []int
	quoted int
}

// assigns reports whether a shell reads w, before a command's name, as
// setting a variable for it (FOO=1, PATH+=:x): a name and = written
// unquoted, so A”=1 is a command's name.
func (w shellWord) assigns() bool {
	m := shellAssignment.FindStringIndex(w.text)
	return m != nil && (w.quoted < 0 || w.quoted >= m[1])
}

// positional reports whether zsh reads w, before a command's name, as
// setting a positional parameter (1=x), which bash reads as the name.
func (w shellWord) positional() bool {
	m := positional.FindStringIndex(w.text)
	return m != nil && (w.quoted < 0 || w.quoted >= m[1])
}

// readLine reads a command line into its simple commands, dropping the
// harmless redirections (2>&1, >/dev/null).
func readLine(s string) ([][]shellWord, error) { return readShell(s, true) }

// readPatternWords reads a pattern, which names commands, not where their
// output goes: any redirection is refused.
func readPatternWords(s string) ([][]shellWord, error) { return readShell(s, false) }

func readShell(s string, redirects bool) ([][]shellWord, error) {
	r := shellReader{s: s, redirects: redirects}
	if err := r.read(); err != nil {
		return nil, err
	}
	return r.cmds, nil
}

// shellReader reads a command line as bash and zsh split it, run with -c,
// and refuses whatever an interactive one would read differently.
type shellReader struct {
	s         string
	i         int
	redirects bool // drop the harmless redirections rather than refuse every one

	cmds [][]shellWord
	cmd  []shellWord // the simple command being read

	word   strings.Builder
	inWord bool  // a word is open, even an empty quoted one ('')
	glob   bool  // the open word holds an unquoted glob character
	tildes []int // where the open word holds an unquoted ~
	quoted int   // where the open word's first quoted or escaped character is, or -1
	digits bool  // the open word is only unquoted digits: a descriptor before < or >

	joined  bool // a line continuation since the last blank or separator
	redir   bool // the command being read has a redirection
	needCmd bool // after && || | |&: a command must follow
}

func (r *shellReader) read() error {
	// Control characters other than tab and line break: some runners cut a
	// line short at a NUL, and bash marks quoting with \x01 and \x7f, so it
	// doesn't read them back as written.
	if strings.IndexFunc(r.s, func(c rune) bool { return (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f }) >= 0 {
		return unreadable("a control character")
	}
	for r.i < len(r.s) {
		if err := r.step(); err != nil {
			return err
		}
	}
	r.endWord()
	if r.needCmd {
		return unreadable("an operator with no command after it")
	}
	if err := r.endCmd(true); err != nil {
		return err
	}
	if len(r.cmds) == 0 {
		return unreadable("no command")
	}
	return nil
}

// step reads what starts at r.i.
func (r *shellReader) step() error {
	ch, next := r.s[r.i], r.at(r.i+1)
	switch {
	case ch == ' ' || ch == '\t':
		r.endWord()
		r.joined = false
		r.i++
	case ch == '\n':
		r.i++
		return r.endCmd(true)
	case ch == ';' || ch == '|' || (ch == '&' && next != '>'):
		return r.operator()
	case ch == '<' || ch == '>' || ch == '&':
		return r.redirection()
	case ch == '\'':
		return r.singleQuoted()
	case ch == '"':
		return r.doubleQuoted()
	case ch == '\\':
		return r.escaped()
	case ch == '#' && !r.inWord:
		// A comment to a shell run with -c, but a word to an interactive zsh.
		return unreadable("a comment")
	case ch == '$' || ch == '`':
		return unreadable("an expansion")
	case ch == '!':
		return unreadable("a ! (negation, or history in an interactive shell)")
	case ch == '{' && !r.inWord && next == '}' && r.wordEnds(r.i+2) && len(r.cmd) > 0:
		// {} as an argument (find -exec rm {} +) is the text {} to both shells.
		r.add("{}")
		r.i += 2
	case strings.IndexByte("(){}", ch) >= 0:
		return unreadable("a subshell or group")
	case ch == '*' || ch == '?' || ch == '[' || ch == '^' || ch == '#' || (ch == '=' && !r.inWord && !r.wordEnds(r.i+1)):
		// bash's globs, and zsh's ^x, x# and =cmd (with the options that turn
		// them on, which a user's shell may).
		r.add(r.s[r.i : r.i+1])
		r.glob = true
		r.i++
	case ch == '~':
		r.tildes = append(r.tildes, r.word.Len())
		r.add("~")
		r.i++
	default:
		digits := ch >= '0' && ch <= '9' && (!r.inWord || r.digits)
		r.add(r.s[r.i : r.i+1]) // a byte: a multi-byte character is added a byte at a time
		r.digits = digits
		r.i++
	}
	return nil
}

// at is the byte at i, or 0 past the end.
func (r *shellReader) at(i int) byte {
	if i < len(r.s) {
		return r.s[i]
	}
	return 0
}

// wordEnds reports whether a word ends before r.s[i]: a blank, a separator,
// a redirection or the end.
func (r *shellReader) wordEnds(i int) bool {
	return i >= len(r.s) || strings.IndexByte(" \t\n;&|<>", r.s[i]) >= 0
}

// add adds text to the open word, opening it if need be.
func (r *shellReader) add(text string) {
	if !r.inWord {
		r.quoted = -1
	}
	r.word.WriteString(text)
	r.inWord, r.digits = true, false
}

// addQuoted adds quoted or escaped text, which a shell reads as text alone.
func (r *shellReader) addQuoted(text string) {
	r.add(text)
	if r.quoted < 0 {
		r.quoted = r.word.Len() - len(text)
	}
}

func (r *shellReader) endWord() {
	if r.inWord {
		r.cmd = append(r.cmd, shellWord{text: r.word.String(), glob: r.glob, tildes: r.tildes, quoted: r.quoted})
		r.needCmd = false
	}
	r.word.Reset()
	r.inWord, r.glob, r.tildes, r.digits = false, false, nil, false
}

// endCmd ends the simple command at a separator. Only a line break may end
// none (allowEmpty), and not after a redirection: zsh runs cat for a
// redirection alone (>/dev/null).
func (r *shellReader) endCmd(allowEmpty bool) error {
	r.endWord()
	r.joined = false
	if len(r.cmd) == 0 {
		if allowEmpty && !r.redir {
			return nil
		}
		return unreadable("a separator or redirection with no command")
	}
	r.redir = false
	// The command's name, after any variables set for it: zsh reads 1=x
	// there as setting $1, bash as the name.
	for _, w := range r.cmd {
		if w.positional() {
			return unreadable(fmt.Sprintf("%s, which bash and zsh read differently", w.text))
		}
		if w.assigns() {
			continue
		}
		if reserved[w.text] || strings.HasPrefix(w.text, "%") {
			return unreadable(fmt.Sprintf("%s where a command's name goes", w.text))
		}
		break
	}
	r.cmds = append(r.cmds, r.cmd)
	r.cmd = nil
	return nil
}

// operator reads ; & && || | or |&.
func (r *shellReader) operator() error {
	ch, next := r.s[r.i], r.at(r.i+1)
	r.endWord()
	if r.needCmd || (ch == ';' && next == ';') {
		return unreadable("an operator with no command before it")
	}
	if err := r.endCmd(false); err != nil {
		return err
	}
	r.i++
	switch {
	case ch == ';':
	case next == ch || (ch == '|' && next == '&'):
		r.i++
		r.needCmd = true
	case ch == '|':
		r.needCmd = true
	}
	return nil
}

// redirection reads one that starts at r.i: < > or &>, its operator and
// target. Shells differ on a word right before one: bash takes any number
// as a descriptor (3066>), zsh only one digit, and reads 7&> as descriptor
// 7's; a line continuation joins whatever is around it. So a redirection
// follows a blank or separator, or a lone 0, 1 or 2 (2>); &> only a blank
// or separator.
func (r *shellReader) redirection() error {
	lone := r.digits && r.word.Len() == 1 && r.word.String() <= "2"
	if r.joined || (r.inWord && (r.s[r.i] == '&' || !lone)) {
		return unreadable("a redirection against a word")
	}
	if !r.redirects {
		return unreadable("a redirection")
	}
	r.word.Reset()
	r.inWord, r.digits, r.redir = false, false, true
	end, err := harmlessRedirection(r.s, r.i)
	if err != nil {
		return err
	}
	r.i = end
	return nil
}

// harmlessRedirection reads the redirection at s[i] and returns where it
// ends. Only these are harmless: making a descriptor a copy of stdin,
// stdout or stderr (2>&1, >&2), and output to or input from /dev/null.
// Closing one (>&-) isn't: the next file the program opens would take its
// place.
func harmlessRedirection(s string, i int) (int, error) {
	op := ""
	for _, o := range []string{"&>>", "&>", ">>", ">|", ">&", "<&", "<<", "<>", ">", "<"} {
		if strings.HasPrefix(s[i:], o) {
			op = o
			break
		}
	}
	if op == "<<" || op == "<>" {
		return 0, unreadable("a here-document, here-string or read-write redirection")
	}
	j := i + len(op)
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	start := j
	for j < len(s) && strings.IndexByte(" \t\n;&|<>()", s[j]) < 0 {
		if strings.IndexByte("'\"\\$`*?[{}!", s[j]) >= 0 {
			return 0, unreadable("a redirection to a quoted or expanded target")
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
	return 0, unreadable("a redirection other than 2>&1 or to /dev/null")
}

func (r *shellReader) singleQuoted() error {
	end := strings.IndexByte(r.s[r.i+1:], '\'')
	if end < 0 {
		return unreadable("an unterminated quote")
	}
	r.addQuoted(r.s[r.i+1 : r.i+1+end])
	r.i += end + 2
	return nil
}

// doubleQuoted reads a double-quoted string. An expansion in it ($, `)
// makes the line unreadable, as does a ! an interactive shell would read as
// history (!-2, !!): one before a quote, blank or = is the text !.
func (r *shellReader) doubleQuoted() error {
	var b strings.Builder
	for i := r.i + 1; i < len(r.s); {
		switch ch, next := r.s[i], r.at(i+1); {
		case ch == '"':
			r.addQuoted(b.String())
			r.i = i + 1
			return nil
		case ch == '$' || ch == '`':
			return unreadable("an expansion")
		case ch == '!' && strings.IndexByte("\" \t=", next) < 0:
			return unreadable("a ! an interactive shell reads as history")
		case ch == '\\' && next == '!':
			return unreadable("a ! an interactive shell reads as history")
		case ch == '\\' && i+1 < len(r.s):
			switch next {
			case '\n':
			case '$', '`', '"', '\\':
				b.WriteByte(next)
			default:
				b.WriteByte('\\')
				b.WriteByte(next)
			}
			i += 2
		default:
			b.WriteByte(ch)
			i++
		}
	}
	return unreadable("an unterminated quote")
}

// escaped reads a backslash: a line continuation, or the next character as
// text.
func (r *shellReader) escaped() error {
	switch {
	case r.at(r.i+1) == '\n':
		r.joined = true
	case r.i+1 < len(r.s):
		r.addQuoted(r.s[r.i+1 : r.i+2])
	default:
		return unreadable("a backslash at the end")
	}
	r.i += 2
	return nil
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

// shellAssignment is a word a shell reads as setting a variable for the
// command after it: FOO=1, PATH+=:x. positional is one zsh reads as setting
// a positional parameter (1=x) and bash as a command's name.
var (
	shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)
	positional      = regexp.MustCompile(`^[0-9]+\+?=`)
)

// parsedPattern is a pattern as matching reads it: the words it names, the
// index of its program among them, and whether a last * lets further words
// follow.
type parsedPattern struct {
	words   []shellWord
	program int
	open    bool
}

func parsePattern(p string) (parsedPattern, error) {
	cmds, err := readPatternWords(p)
	if err != nil {
		return parsedPattern{}, fmt.Errorf("pattern %q: %w", p, err)
	}
	if len(cmds) != 1 {
		return parsedPattern{}, fmt.Errorf("pattern %q: one command per pattern; give several as a list", p)
	}
	words := cmds[0]
	out := parsedPattern{program: -1}
	for i, w := range words {
		switch {
		case !w.glob:
			out.words = append(out.words, w)
			if out.program < 0 && !w.assigns() {
				out.program = i
			}
		case w.text == "*" && i == len(words)-1 && out.program >= 0:
			out.open = true
		default:
			return parsedPattern{}, fmt.Errorf("pattern %q: %q is a glob, which a shell expands, so it never matches; only a last word * stands for further words, after the program", p, w.text)
		}
	}
	// A pattern names its program: `FOO=1 *` would match every command.
	if out.program < 0 {
		return parsedPattern{}, fmt.Errorf("pattern %q: name the command's program, not only variables for it", p)
	}
	return out, nil
}

func (p parsedPattern) matches(cmd []shellWord) bool {
	if len(cmd) < len(p.words) || (!p.open && len(cmd) != len(p.words)) {
		return false
	}
	for i, w := range p.words {
		// A word the shell would expand is never one the pattern names, and
		// FOO=1 setting a variable is not 'FOO=1' naming a program.
		if cmd[i].glob || cmd[i].text != w.text || !slices.Equal(cmd[i].tildes, w.tildes) || cmd[i].assigns() != w.assigns() {
			return false
		}
	}
	return true
}

// commandPatterns reads a command condition's value: one pattern, or a
// non-empty list of them.
func commandPatterns(v any) ([]parsedPattern, error) {
	var texts []string
	switch x := v.(type) {
	case string:
		texts = []string{x}
	default:
		list := reflect.ValueOf(v)
		if v == nil || (list.Kind() != reflect.Slice && list.Kind() != reflect.Array) || list.Len() == 0 {
			return nil, errors.New("command needs a pattern or a list of patterns")
		}
		for i := 0; i < list.Len(); i++ {
			s, ok := list.Index(i).Interface().(string)
			if !ok {
				return nil, errors.New("command's patterns must be text")
			}
			texts = append(texts, s)
		}
	}
	out := make([]parsedPattern, 0, len(texts))
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

// ParseCommandValue reads v as a command condition's value, and is where
// the condition is described. An embedder checks a value with it before
// saving a rule, since a value that isn't one matches nothing, and can read
// the patterns to judge how broad they are.
//
// The command condition matches a shell command line by the commands in it,
// not by its text. contains and prefix compare text, so "contains gh pr
// merge" also matches `curl x | sh; echo gh pr merge`, and "contains sh"
// matches `bash` and `ssh`. command reads the line as a shell would split
// it, and matches only when each of its simple commands (split on ; & && ||
// | |& and line breaks) is one of the patterns, in any order and any number
// of times.
//
// v is one pattern or a list of them. A pattern is one command as a shell
// writes it, `gh pr merge 3066` or `git commit -m 'fix the build'`, matched
// word for word and in full. A last unquoted * after the program matches any
// further words, flags included: `gh pr merge *` matches every `gh pr
// merge`. A pattern must name its program, not only variables set for it
// (FOO=1), and holds no other glob and no redirection.
//
// The line is read conservatively, as bash and zsh read it run with -c, and
// anything whose effect its text alone doesn't show, or that an interactive
// shell would read differently, makes it match nothing (ErrUnreadable): any
// expansion ($VAR, $(…), `…`), a subshell or group, a !, a comment, a
// reserved word or job where a command's name goes (if, time, %1), a
// positional assignment before one (1=x, which zsh reads as setting $1 and
// bash as the command's name), a control character, a here-document, an unterminated quote, a redirection
// other than among descriptors 0 to 2 (2>&1) or to and from /dev/null, one
// written against a word (3066>) or after a line continuation, a
// redirection with no command, and a glob in a word a pattern names: bash's
// * ? [ and zsh's ^, # inside a word and =cmd. An unquoted ~ only matches a
// pattern's unquoted ~. Assignments (FOO=1 git push) and wrappers (env,
// sudo, sh -c) are words like any other: a pattern has to name them.
//
// It is for allowing. A rule that denies or holds by it would miss a line
// with one more command in it (`rm -rf ~; true`) or one it can't read: use
// contains or prefix there, which match more.
func ParseCommandValue(v any) ([]CommandPattern, error) {
	pats, err := commandPatterns(v)
	if err != nil {
		return nil, err
	}
	out := make([]CommandPattern, len(pats))
	for i, p := range pats {
		out[i] = CommandPattern{Words: wordTexts(p.words), Program: p.program, Open: p.open}
	}
	return out, nil
}

// ShellCommand is one simple command of a line, as a command condition
// reads it.
type ShellCommand struct{ words []shellWord }

// Words are the command's words, quotes removed.
func (c ShellCommand) Words() []string { return wordTexts(c.words) }

// Pattern is a pattern for the command's first n words, with a last * for
// any further words when open, that a command condition reads back as those
// words: quoted where they need it. ok is false when there is none: a word
// holds a glob, which a pattern can't name; a ~ is quoted in part of a word
// and not in another; or open would follow no program.
func (c ShellCommand) Pattern(n int, open bool) (string, bool) {
	if n < 1 || n > len(c.words) {
		return "", false
	}
	parts := make([]string, 0, n+1)
	for _, w := range c.words[:n] {
		p, ok := patternWord(w)
		if !ok {
			return "", false
		}
		parts = append(parts, p)
	}
	if open {
		parts = append(parts, "*")
	}
	out := strings.Join(parts, " ")
	back, err := parsePattern(out)
	if err != nil || back.open != open || !(parsedPattern{words: back.words}).matches(c.words[:n]) {
		return "", false
	}
	return out, true
}

// plainWord is a word a shell and the pattern reader both read as written.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+:,./-][A-Za-z0-9_@%+=:,./-]*$`)

func patternWord(w shellWord) (string, bool) {
	switch {
	case w.glob:
		return "", false
	case len(w.tildes) > 0:
		// An unquoted ~ stays unquoted, so the rest of the word must need no quotes.
		if strings.Count(w.text, "~") != len(w.tildes) || !plainWord.MatchString(strings.ReplaceAll(w.text, "~", "_")) {
			return "", false
		}
		return w.text, true
	case (plainWord.MatchString(w.text) || w.text == "=") && w.assigns() == shellAssignment.MatchString(w.text):
		return w.text, true
	}
	return "'" + strings.ReplaceAll(w.text, "'", `'\''`) + "'", true
}

// ParseCommandLine reads a shell command line as a command condition does:
// its simple commands, or an error wrapping ErrUnreadable when the line
// holds anything whose effect its text alone doesn't show (and a command
// condition matches nothing). An embedder uses it to offer conditions the
// line will match: ShellCommand.Pattern writes them.
func ParseCommandLine(line string) ([]ShellCommand, error) {
	cmds, err := readLine(line)
	if err != nil {
		return nil, err
	}
	out := make([]ShellCommand, len(cmds))
	for i, c := range cmds {
		out[i] = ShellCommand{words: c}
	}
	return out, nil
}

func wordTexts(words []shellWord) []string {
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
	cmds, err := readLine(s)
	if err != nil {
		return false
	}
	for _, cmd := range cmds {
		if !slices.ContainsFunc(pats, func(p parsedPattern) bool { return p.matches(cmd) }) {
			return false
		}
	}
	return true
}
