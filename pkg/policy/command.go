package policy

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"
)

// This file implements the "command" condition: it matches a shell command
// line by the commands in it, not by its text. With the pattern `git status`,
// the line `git status && git status` matches, but `git status; rm -rf /`
// doesn't, because rm -rf / isn't a command the pattern allows.
//
// A line goes through these steps:
//
//   - shellReader splits the line into simple commands and their words, the
//     way bash and zsh would. It refuses (ErrUnreadable) any line it can't be
//     sure about, such as one holding $VAR.
//   - parsePattern reads each of the rule's patterns with the same reader.
//   - matchCommand checks that every command in the line matches a pattern.
//
// ParseCommandValue's comment describes the condition in full, as a rule's
// author sees it.

// ErrUnreadable is wrapped by the error ParseCommandLine returns when a line
// holds shell syntax that could make it do something its text doesn't show.
// A command condition never matches such a line. The wrapping error names
// the syntax it found. For example, ParseCommandLine("echo $HOME") returns
//
//	the command line holds shell syntax whose effect its text alone doesn't show: an expansion
//
// because $HOME could expand to anything. Check for it with
// errors.Is(err, ErrUnreadable).
var ErrUnreadable = errors.New("the command line holds shell syntax whose effect its text alone doesn't show")

// ErrBadPattern is wrapped by the error ParseCommandValue returns when a
// command condition's value isn't a valid pattern or list of patterns. A
// condition with such a value matches nothing. For example, `rm *.log` is
// refused: a shell expands *.log into file names before rm runs, so no line
// could ever match it word for word.
var ErrBadPattern = errors.New("not a command pattern")

// unreadable returns an ErrUnreadable error naming what was found, e.g.
// unreadable("a comment").
func unreadable(what string) error { return fmt.Errorf("%w: %s", ErrUnreadable, what) }

// badPattern returns an ErrBadPattern error naming the pattern p and why it
// was refused.
func badPattern(p, why string) error { return fmt.Errorf("%w %q: %s", ErrBadPattern, p, why) }

// shellWord is one word of a simple command, after the shell has removed
// its quotes: the line `git commit -m 'fix it'` has the words git, commit,
// -m and "fix it". Besides the text, it keeps what quote removal throws away
// but matching needs:
//
//   - glob: the word holds an unquoted glob character, which a shell would
//     expand into file names. `ls *.go` has one; `ls '*.go'` doesn't.
//   - tildes: the offset in text of each unquoted ~, which a shell may
//     expand to a home directory. `cat ~/x` has tildes [0] and `cat '~/x'`
//     has none, so they are different words though both texts are "~/x".
//   - quoted: the offset in text of the first character that was quoted or
//     escaped, or -1 if none was. See assigns for why it matters.
type shellWord struct {
	text   string
	glob   bool
	tildes []int
	quoted int
}

// assigns reports whether a shell would read w, before a command's name, as
// setting a variable for that command. The name and the = must be unquoted:
//
//	FOO=1 git push   FOO=1 assigns: git runs with FOO set to 1
//	PATH+=:x ls      PATH+=:x assigns too: it appends :x to PATH
//	A""=1 ls         A""=1 doesn't: the shell runs a program named A=1
func (w shellWord) assigns() bool { return w.unquotedPrefix(shellAssignment) }

// positional reports whether w is a word like 1=x. Before a command's name,
// zsh reads it as setting the positional parameter $1, and bash as the
// command's name. Because the two disagree, a line with one there is
// refused (see endCmd).
func (w shellWord) positional() bool { return w.unquotedPrefix(positional) }

// unquotedPrefix reports whether w starts with a match of re, with no
// character of the match quoted or escaped. With re matching FOO=, it is
// true for FOO=1 and false for "FOO"=1 and FOO\=1.
func (w shellWord) unquotedPrefix(re *regexp.Regexp) bool {
	m := re.FindStringIndex(w.text)
	return m != nil && (w.quoted < 0 || w.quoted >= m[1])
}

// readLine splits a command line into its simple commands, each a list of
// words. The harmless redirections (2>&1, >/dev/null) are dropped, since
// they don't change which programs run:
//
//	readLine("make test 2>&1 | tee log")  →  [make test], [tee log]
func readLine(s string) ([][]shellWord, error) { return readShell(s, true) }

// readPatternWords reads a rule's pattern the way readLine reads a line,
// except that every redirection is refused: a pattern names commands, not
// where their output goes, so `make >/dev/null` isn't a valid pattern.
func readPatternWords(s string) ([][]shellWord, error) { return readShell(s, false) }

// readShell runs a shellReader over s. redirects is whether harmless
// redirections are dropped (true) or every redirection is refused (false).
func readShell(s string, redirects bool) ([][]shellWord, error) {
	r := shellReader{s: s, redirects: redirects}
	if err := r.read(); err != nil {
		return nil, err
	}
	return r.cmds, nil
}

// shellReader splits a command line into simple commands the way bash and
// zsh do when run with -c, and refuses (ErrUnreadable) any line that an
// interactive bash or zsh would read differently. A simple command is a
// program and its arguments: `git add . && git commit -m wip` holds two,
// [git add .] and [git commit -m wip].
//
// Why we wrote our own reader instead of using a shell parser such as
// mvdan.cc/sh: this isn't a parser. It only recognizes the small part of
// shell syntax whose every effect you can see in the text:
//
//   - simple commands made of literal words
//   - the operators between them: ; & && || | |& and line breaks
//   - a few harmless redirections: 2>&1, >/dev/null
//
// It refuses everything else, so it never has to work out what an
// expansion, a compound command (if, while, ...) or a redirection would do,
// and so can't get that wrong. A full parser would turn every line into a
// syntax tree we'd then have to judge, and it would read the line as one
// dialect, run with -c. This reader also refuses what an interactive bash or
// zsh reads differently from -c, since a rule can't know which shell a
// harness will hand the line to. For example:
//
//   - history expansion: in an interactive shell, `echo !!` reruns the
//     previous command
//   - zsh's =cmd (cmd's full path) and ^x (any file but x)
//   - a # starting a word: run with -c, `ls # all` runs ls and ignores the
//     rest as a comment; an interactive zsh, by default, passes # and all to
//     ls as arguments
//
// What keeps the reader honest is FuzzCommandAgreesWithShells. It runs every
// line the reader accepts through real bash and zsh, both with -c and
// interactively, in a sandbox, and checks that the shell never runs a
// command the reader didn't report. (The shell may run fewer: in
// `false && ls`, ls never runs.) If you teach the reader new syntax, add
// lines with it to that test too.
type shellReader struct {
	s         string // the line being read
	i         int    // the index in s of the next byte to read
	redirects bool   // drop the harmless redirections (true), or refuse every redirection (false)

	cmds [][]shellWord // the simple commands read so far
	cmd  []shellWord   // the words of the simple command being read

	word   strings.Builder // the text of the open word, quotes removed
	inWord bool            // a word is open, even an empty one written as ''
	glob   bool            // the open word holds an unquoted glob character
	tildes []int           // the offset in word of each unquoted ~
	quoted int             // the offset in word of its first quoted or escaped character, or -1
	digits bool            // the open word is only unquoted digits so far, like the 2 of 2>&1

	joined  bool // a line continuation (\ then a line break) came since the last blank or separator
	redir   bool // the command being read has a redirection
	needCmd bool // the last operator was && || | or |&, so a command must come next
}

// read reads the whole line into r.cmds, or returns an ErrUnreadable error.
func (r *shellReader) read() error {
	// Refuse every control character but a line break:
	//   - a NUL: some programs that run the line cut it short there
	//   - \x01 and \x7f: bash uses them to mark quoting, so it doesn't read
	//     them back as written
	//   - a tab: an interactive shell completes a word at a tab, as if you'd
	//     pressed the Tab key
	// So "ls\tfoo" is refused.
	if strings.IndexFunc(r.s, func(c rune) bool { return (c < 0x20 && c != '\n') || c == 0x7f }) >= 0 {
		return unreadable("a control character")
	}
	// Refuse bytes that aren't valid UTF-8: an interactive shell's line
	// editor may drop them, or read them as key presses. (A line that came
	// from JSON is always valid UTF-8.)
	if !utf8.ValidString(r.s) {
		return unreadable("text that isn't UTF-8")
	}
	for r.i < len(r.s) {
		if err := r.step(); err != nil {
			return err
		}
	}
	r.endWord()
	// The line ended right after && || | or |&, as in `git status &&`.
	if r.needCmd {
		return unreadable("an operator with no command after it")
	}
	// The last command. A line may end after ; or &, as in `ls;`, so there
	// may be none (allowEmpty).
	if err := r.endCmd(true); err != nil {
		return err
	}
	// An empty line, or only blanks and line breaks.
	if len(r.cmds) == 0 {
		return unreadable("no command")
	}
	return nil
}

// step reads what starts at r.i (a blank, an operator, a quoted string, one
// character of a word, ...) and moves r.i past it.
func (r *shellReader) step() error {
	ch, next := r.s[r.i], r.at(r.i+1)
	switch {
	case ch == ' ' || ch == '\t':
		// A blank ends the open word: `git status` is two words.
		r.endWord()
		r.joined = false
		r.i++
	case ch == '\n':
		// A line break ends the command, like ;.
		r.i++
		return r.endCmd(true)
	case ch == ';' || ch == '|' || (ch == '&' && next != '>'):
		// ; | || & && |&, but not &>, which redirects (&>/dev/null).
		return r.operator()
	case ch == '<' || ch == '>' || ch == '&':
		// < > and &> start a redirection: 2>&1, >/dev/null, &>/dev/null.
		return r.redirection()
	case ch == '\'':
		return r.singleQuoted()
	case ch == '"':
		return r.doubleQuoted()
	case ch == '\\':
		return r.escaped()
	case ch == '#' && !r.inWord:
		// # starting a word: run with -c, `ls # all` runs ls and ignores the
		// rest as a comment, but an interactive zsh passes # and all to ls.
		// (A # inside a word, as in a#b, is a zsh glob: see below.)
		return unreadable("a comment")
	case ch == '$' || ch == '`':
		// $VAR, ${VAR}, $(cmd) and `cmd`: the text doesn't show what they
		// expand to.
		return unreadable("an expansion")
	case ch == '!':
		// ! negates a command's status (`! grep x f`), and in an interactive
		// shell starts history expansion (`!!` reruns the last command).
		return unreadable("a ! (negation, or history in an interactive shell)")
	case ch == '{' && !r.inWord && next == '}' && r.wordEnds(r.i+2) && r.programRead():
		// {} as an argument, as in `find . -exec rm {} +`, is the text {} to
		// both shells. Only after the program's name: as a command's first
		// word, { starts a group.
		r.add("{}")
		r.i += 2
	case strings.IndexByte("(){}", ch) >= 0:
		// A subshell, `(cd x; make)`, a group, `{ ls; }`, or a brace
		// expansion inside a word, `a{b,c}`, which the shell turns into the
		// two words ab and ac.
		return unreadable("a subshell or group")
	case ch == '[' && !r.inWord && r.wordEnds(r.i+1):
		// [ on its own is the test command, as in `[ -f x ]` (where ] is its
		// last argument), not the start of a glob.
		r.add("[")
		r.i++
	case ch == '*' || ch == '?' || ch == '[' || ch == '^' || ch == '#' || (ch == '=' && !r.inWord && !r.wordEnds(r.i+1)):
		// A glob character: bash's * ? and [...], and zsh's ^x (any file but
		// x), x# (zero or more x) and =cmd (cmd's full path). zsh expands the
		// last three only with options that turn them on (extendedglob,
		// equals), but a user's shell may have them on. Only an = starting a
		// word, with more after it, is =cmd: FOO=1 and a lone = aren't.
		//
		// The word is kept, marked as a glob, so it never matches a pattern:
		// `ls *.go` is read, but no pattern allows it.
		r.add(r.s[r.i : r.i+1])
		r.glob = true
		r.i++
	case ch == '~':
		// An unquoted ~ may expand to a home directory (~/x, ~alice/x).
		// Record where it is, so matching can tell `~/x` from `'~/x'`.
		r.tildes = append(r.tildes, r.word.Len())
		r.add("~")
		r.i++
	default:
		// Any other byte is text. digits tracks whether the word so far is
		// all digits, so that redirection can tell the descriptor 2 of 2>&1
		// from a word like x2 in x2>&1.
		digits := ch >= '0' && ch <= '9' && (!r.inWord || r.digits)
		r.add(r.s[r.i : r.i+1]) // a byte: a multi-byte character is added a byte at a time
		r.digits = digits
		r.i++
	}
	return nil
}

// programRead reports whether the command being read already has its
// program's name: a word that isn't a variable assignment. In
// `FOO=1 find . -exec rm {} +`, it becomes true once find is read.
func (r *shellReader) programRead() bool {
	for _, w := range r.cmd {
		if !w.assigns() {
			return true
		}
	}
	return false
}

// at is the byte at i, or 0 past the end.
func (r *shellReader) at(i int) byte {
	if i < len(r.s) {
		return r.s[i]
	}
	return 0
}

// wordEnds reports whether a word ends right before r.s[i]: at a blank, a
// separator, a redirection or the end of the line. In `rm {} +`, a word
// ends after {}, at the blank.
func (r *shellReader) wordEnds(i int) bool {
	return i >= len(r.s) || strings.IndexByte(" \t\n;&|<>", r.s[i]) >= 0
}

// add appends text to the open word, opening a new word if none is open.
func (r *shellReader) add(text string) {
	if !r.inWord {
		r.quoted = -1
	}
	r.word.WriteString(text)
	r.inWord, r.digits = true, false
}

// addQuoted appends quoted or escaped text, which a shell takes literally,
// and records where the word's first quoted character is (shellWord.quoted).
func (r *shellReader) addQuoted(text string) {
	r.add(text)
	if r.quoted < 0 {
		r.quoted = r.word.Len() - len(text)
	}
}

// endWord closes the open word, if there is one, and adds it to the command
// being read.
func (r *shellReader) endWord() {
	if r.inWord {
		r.cmd = append(r.cmd, shellWord{text: r.word.String(), glob: r.glob, tildes: r.tildes, quoted: r.quoted})
		r.needCmd = false
	}
	r.word.Reset()
	r.inWord, r.glob, r.tildes, r.digits = false, false, nil, false
}

// endCmd ends the simple command being read, at a separator or the end of
// the line, and adds it to r.cmds.
//
// allowEmpty is whether there may be no command at all. Only a line break
// or the end of the line allows that, so `ls` then a blank line, or `ls;`,
// is fine, while `; ls` and `ls;;` aren't. Even then, a redirection with no
// command isn't allowed: zsh runs cat for a line like `>/dev/null`.
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
	// Check the command's name, its first word after any assignments
	// (FOO=1). It may not be:
	//   - a positional assignment, 1=x: zsh sets $1, bash runs a program
	//     named 1=x
	//   - a reserved word, if, while, time, ...: the line isn't a simple
	//     command
	//   - a job, %1: alone, it means `fg %1`
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

// operator reads the operator at r.i, one of ; & && || | |&, and ends the
// command before it. After && || | or |&, another command must follow:
// `ls &&` is refused, while `ls;` and `ls &` are fine.
func (r *shellReader) operator() error {
	ch, next := r.s[r.i], r.at(r.i+1)
	r.endWord()
	// Two operators in a row, as in `ls && && pwd`, or ;;, which only means
	// something inside a case statement.
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
		// && || and |&: two bytes.
		r.i++
		r.needCmd = true
	case ch == '|':
		r.needCmd = true
	}
	return nil
}

// redirection reads a redirection starting at r.i, at its <, > or &>.
//
// The word right before it is where bash and zsh disagree. bash reads any
// number written against it as a file descriptor (in `echo 3066>x`, 3066),
// zsh only a single digit (so it runs `echo 3066` with its output in x),
// and zsh reads `7&>x` as redirecting descriptor 7. A line continuation (\
// then a line break) also joins whatever is on either side of it. So a
// redirection is only accepted after a blank or separator (`ls >/dev/null`)
// or a lone 0, 1 or 2 (`ls 2>&1`), and &> only after a blank or separator.
func (r *shellReader) redirection() error {
	lone := r.digits && r.word.Len() == 1 && r.word.String() <= "2"
	if r.joined || (r.inWord && (r.s[r.i] == '&' || !lone)) {
		return unreadable("a redirection against a word")
	}
	if !r.redirects {
		return unreadable("a redirection")
	}
	// Drop the descriptor (the 2 of 2>&1): it isn't one of the command's
	// words.
	r.word.Reset()
	r.inWord, r.digits, r.redir = false, false, true
	return r.harmlessRedirection()
}

// harmlessRedirection reads the redirection at r.i, its operator and its
// target, and refuses it unless it's harmless. Only two kinds are:
//
//   - making a descriptor a copy of stdin, stdout or stderr: 2>&1, >&2
//   - output to, or input from, /dev/null: >/dev/null, &>/dev/null,
//     >& /dev/null, </dev/null
//
// Everything else is refused, closing a descriptor (>&-) included: the next
// file the program opened would get that descriptor's number, so output
// meant for, say, stdout could land in that file.
func (r *shellReader) harmlessRedirection() error {
	s, i := r.s, r.i
	op := ""
	// Longest first, so >> isn't read as > and then >.
	for _, o := range []string{"&>>", "&>", ">>", ">|", ">&", "<&", "<<", "<>", ">", "<"} {
		if strings.HasPrefix(s[i:], o) {
			op = o
			break
		}
	}
	if op == "<<" || op == "<>" {
		return unreadable("a here-document, here-string or read-write redirection")
	}
	// Skip blanks between the operator and its target: `> /dev/null`.
	j := i + len(op)
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	// The target runs to the next blank or separator. A quote, escape,
	// expansion, glob or brace in it is refused, as in >"$f".
	start := j
	for j < len(s) && strings.IndexByte(" \t\n;&|<>()", s[j]) < 0 {
		if strings.IndexByte("'\"\\$`*?[{}!", s[j]) >= 0 {
			return unreadable("a redirection to a quoted or expanded target")
		}
		j++
	}
	// Accept /dev/null, or a copy of descriptor 0, 1 or 2. Any other
	// descriptor (>&5) is whatever the program that runs the shell left open
	// there, which could be any file.
	if target := s[start:j]; target == "/dev/null" || ((op == ">&" || op == "<&") && (target == "0" || target == "1" || target == "2")) {
		r.i = j
		return nil
	}
	return unreadable(fmt.Sprintf("a redirection %s%s, which isn't to /dev/null or among descriptors 0 to 2", op, s[start:j]))
}

// singleQuoted reads a single-quoted string. Everything up to the next ' is
// literal text: 'a $b' is the text a $b.
func (r *shellReader) singleQuoted() error {
	end := strings.IndexByte(r.s[r.i+1:], '\'')
	if end < 0 {
		return unreadable("an unterminated quote")
	}
	r.addQuoted(r.s[r.i+1 : r.i+1+end])
	r.i += end + 2
	return nil
}

// doubleQuoted reads a double-quoted string. Inside one, a backslash only
// escapes $ ` " \ and a line break (which it removes, joining the lines);
// before anything else it stays as written, so "a\b" is the text a\b. The
// line is refused when the string holds:
//
//   - an expansion: "$HOME", "`date`"
//   - a ! that an interactive shell would read as history: "!!", "!-2",
//     "a\!b". A ! followed by a quote, blank or = is left alone by history
//     expansion, so "Done!" and "a ! b" are fine.
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

// escaped reads a backslash outside quotes. Before a line break, it's a
// line continuation, and both are removed. Before anything else, it makes
// that character literal text: \* is the text *, not a glob.
func (r *shellReader) escaped() error {
	switch {
	case r.at(r.i+1) == '!':
		// \!: an interactive shell can still read it as history, and an
		// interactive zsh as negation, like `! cmd`.
		return unreadable("an escaped !")
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

// reserved are the words that, as a command's first word after any
// assignments, make it something other than a simple command: bash's and
// zsh's reserved words (if, while, time, ...) and zsh's precommand
// modifiers (noglob, nocorrect, -), which change how the rest of the
// command is read. `if true; then rm x; fi` is refused at if. A first word
// starting with % is refused too (see endCmd): it's a job, and %1 alone
// means `fg %1`.
var reserved = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "case": true, "esac": true,
	"for": true, "select": true, "while": true, "until": true, "do": true, "done": true, "in": true,
	"function": true, "time": true, "coproc": true, "[[": true, "]]": true,
	"repeat": true, "foreach": true, "end": true, "nocorrect": true, "noglob": true, "-": true, "!": true,
}

// shellAssignment matches the start of a word a shell reads as setting a
// variable for the command after it, as in `FOO=1 make` or `PATH+=:x ls`.
// positional matches the start of a word like 1=x, which zsh reads as
// setting $1 and bash as a command's name.
var (
	shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)
	positional      = regexp.MustCompile(`^[0-9]+\+?=`)
)

// parsedPattern is a rule's pattern, ready for matching. For the pattern
// `FOO=1 gh pr merge *`:
//
//	words    FOO=1 gh pr merge
//	program  1, the index of gh: the first word that isn't an assignment
//	open     true: the last * lets any further words follow
type parsedPattern struct {
	words   []shellWord
	program int
	open    bool
}

// parsePattern reads one pattern. It must be a single simple command with
// no redirection, must name a program, and may hold no glob except a last
// word * after the program. `gh pr *` is fine; `gh * merge`, `rm *.log`,
// `ls; pwd` and `FOO=1` are refused.
func parsePattern(p string) (parsedPattern, error) {
	cmds, err := readPatternWords(p)
	if err != nil {
		return parsedPattern{}, badPattern(p, err.Error())
	}
	if len(cmds) != 1 {
		return parsedPattern{}, badPattern(p, "one command per pattern; give several as a list")
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
			return parsedPattern{}, badPattern(p, fmt.Sprintf("%q is a glob, which a shell expands, so no command it names matches; only a last word * stands for further words, after the program", w.text))
		}
	}
	// A pattern must name its program: `FOO=1` alone names only a variable.
	if out.program < 0 {
		return parsedPattern{}, badPattern(p, "name the command's program, not only variables for it")
	}
	return out, nil
}

// matches reports whether cmd, one simple command of a line, is one the
// pattern names: the same words in the same order, compared with quotes
// removed, so the pattern `git commit -m 'a b'` matches the line
// `git commit -m "a b"`. A closed pattern needs exactly its words; an open
// one (ending in *) needs them as a prefix, so `gh pr merge *` matches
// `gh pr merge 3066 --squash` but not `gh pr`.
func (p parsedPattern) matches(cmd []shellWord) bool {
	if len(cmd) < len(p.words) || (!p.open && len(cmd) != len(p.words)) {
		return false
	}
	for i, w := range p.words {
		// Beyond the text, the words must agree on what quote removal
		// hides: a line's word with a glob (*.go) never matches, a ~ must be
		// unquoted in both or in neither, and FOO=1 setting a variable
		// doesn't match 'FOO=1', a program's name.
		if cmd[i].glob || cmd[i].text != w.text || !slices.Equal(cmd[i].tildes, w.tildes) || cmd[i].assigns() != w.assigns() {
			return false
		}
	}
	return true
}

// commandPatterns reads a command condition's value: one pattern, or a
// non-empty list of them (from YAML or JSON, usually a []any). If any
// pattern isn't valid, the whole value is refused.
func commandPatterns(v any) ([]parsedPattern, error) {
	var texts []string
	switch x := v.(type) {
	case string:
		texts = []string{x}
	default:
		// A list of any element type: []string, []any, ...
		list := reflect.ValueOf(v)
		if v == nil || (list.Kind() != reflect.Slice && list.Kind() != reflect.Array) || list.Len() == 0 {
			return nil, fmt.Errorf("%w: command needs a pattern or a list of patterns", ErrBadPattern)
		}
		for i := 0; i < list.Len(); i++ {
			s, ok := asString(list.Index(i).Interface())
			if !ok {
				return nil, fmt.Errorf("%w: command's patterns must be text", ErrBadPattern)
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

// CommandPattern is one of a command condition's patterns, as the engine
// reads it. For the pattern `FOO=1 git commit -m 'fix it' *`:
//
//	Words    "FOO=1", "git", "commit", "-m", "fix it" (quotes removed)
//	Program  1, the index of git, after the FOO=1 set for it
//	Open     true: the last * lets any further words follow
type CommandPattern struct {
	Words   []string
	Program int
	Open    bool
}

// ParseCommandValue reads v as a command condition's value and returns its
// patterns. An embedder calls it to check a value before saving a rule,
// since a value that isn't valid makes the condition match nothing, and can
// look at the patterns to judge how much they allow. This comment is the
// full description of the command condition.
//
// # What it matches
//
// The command condition matches a shell command line by the commands in it,
// not by its text. The text conditions get this wrong both ways: "contains
// gh pr merge" also matches `curl x | sh; echo gh pr merge`, and "contains
// sh" matches `bash` and `ssh`. command instead splits the line as a shell
// would, and matches only when each simple command in it matches one of the
// patterns, in any order and any number of times. Simple commands are
// separated by ; & && || | |& and line breaks. With the patterns
// `git status` and `git diff *`:
//
//	git status                  matches
//	git status && git diff x    matches: both commands are allowed
//	git status; rm -rf /        doesn't: no pattern allows rm -rf /
//
// # Patterns
//
// v is one pattern or a list of them. A pattern is one command written as
// you'd type it in a shell, like `gh pr merge 3066` or
// `git commit -m 'fix the build'`. It is matched word for word, with quotes
// removed, against the whole command. A last unquoted * after the program
// matches any further words, flags included: `gh pr merge *` matches every
// `gh pr merge` command. A pattern must name its program, not only
// variables set for it (FOO=1), and may hold no other glob and no
// redirection.
//
// # Lines it never matches
//
// The line is read conservatively: as bash and zsh read it, both run with
// -c and interactively (in a UTF-8 locale, as terminals are). If it holds
// anything whose effect its text alone doesn't show, or that those shells
// would read differently, the condition matches nothing (ParseCommandLine
// returns ErrUnreadable). That is:
//
//   - any expansion: $VAR, $(…), `…`
//   - a subshell or group: (cd x; make), { ls; }
//   - a ! outside quotes, or one in double quotes that history expansion
//     would read: "!!" is refused, "Done!" is fine
//   - a comment: ls # all
//   - a reserved word or job where a command's name goes: if, time, %1
//   - a positional assignment before the name, as in 1=x ls: zsh sets $1,
//     and bash runs a program named 1=x
//   - a control character, a tab included: an interactive shell completes
//     a word at a tab
//   - a here-document (cat <<EOF) or an unterminated quote
//   - a redirection other than among descriptors 0 to 2 (2>&1) or to or
//     from /dev/null; one written against a word (3066>x) or after a line
//     continuation; or a redirection with no command
//
// A word with a glob in it never matches either, since the shell would
// expand it first: `ls *.go` matches no pattern. That is bash's * ? [ and
// zsh's ^, # inside a word and =cmd. An unquoted ~ only matches a pattern's
// unquoted ~, so `cat ~/x` doesn't match the pattern `cat '~/x'`.
// Assignments and wrappers are words like any other: to allow
// `FOO=1 git push`, `env git push`, `sudo make install` or `sh -c 'ls'`, a
// pattern has to name them.
//
// # The shell
//
// The reader models bash and zsh only. The embedder names the shell that
// will run the line with the fact "shell" ("bash", "/bin/zsh",
// "C:\\Git\\bin\\bash.exe"); any other value, such as "powershell" or
// "cmd", makes the condition match nothing. Without the fact, the shell is
// taken to be bash or zsh unless the program calling Evaluate runs on
// Windows. An embedder that decides calls for agents on other machines must
// set the fact: from a Linux server, `git status '& del x'` would be read as
// one git command, while cmd.exe, for which ' isn't a quote, runs del.
//
// # Allow rules only
//
// Use command to allow. A rule that denies or holds by it would miss a line
// with one more command in it (`rm -rf ~; true` doesn't match the pattern
// `rm *`), or one it can't read. Use contains or prefix there, which match
// more.
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
// reads it. ParseCommandLine("git add . && git commit -m 'a b'") returns
// two: git add . and git commit -m 'a b'.
type ShellCommand struct{ words []shellWord }

// Words returns the command's words, quotes removed: for
// `git commit -m 'a b'`, "git", "commit", "-m" and "a b".
func (c ShellCommand) Words() []string { return wordTexts(c.words) }

// Pattern writes a pattern that matches the command, for an embedder to
// suggest as a rule. It is the command's first n words, quoted where they
// need it, and when open, a last * for any further words. A closed pattern
// names every word, so then n must be the number of words. For the command
// `gh pr merge 3066 --squash`:
//
//	Pattern(3, true)   →  "gh pr merge *", true
//	Pattern(5, false)  →  "gh pr merge 3066 --squash", true
//	Pattern(3, false)  →  "", false: a closed pattern needs all 5 words
//
// ok is false when there's no such pattern: n is out of range, a word holds
// a glob (which a pattern can't name), a word has a ~ quoted in one place
// and not in another, or the * would come before the program (the first n
// words are all assignments, like FOO=1).
func (c ShellCommand) Pattern(n int, open bool) (string, bool) {
	if n < 1 || n > len(c.words) || (!open && n != len(c.words)) {
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
	// Check the pattern by reading it back: it must be valid, and match the
	// words it was written from.
	back, err := parsePattern(out)
	if err != nil || back.open != open || !(parsedPattern{words: back.words}).matches(c.words[:n]) {
		return "", false
	}
	return out, true
}

// plainWord matches a word that a shell and the pattern reader both read as
// written, so a pattern needs no quotes for it: git, -m, ./x, a=b. It can't
// start with =, which zsh reads as =cmd.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+:,./-][A-Za-z0-9_@%+=:,./-]*$`)

// patternWord writes w as a pattern should hold it: as it is when it needs
// no quotes, otherwise in single quotes, with each ' in it closed, escaped
// and reopened (see it's below). ok is false for a word no pattern can
// name:
//
//	git            →  git
//	fix the build  →  'fix the build'
//	it's           →  'it'\''s'
//	*.go, a glob   →  not ok
func patternWord(w shellWord) (string, bool) {
	switch {
	case w.glob:
		return "", false
	case len(w.tildes) > 0:
		// An unquoted ~ must stay unquoted, so the word can't be quoted:
		// every ~ in it must be unquoted, and the rest must need no quotes.
		// ~/x stays ~/x; a word with one ~ quoted and one not is not ok.
		if strings.Count(w.text, "~") != len(w.tildes) || !plainWord.MatchString(strings.ReplaceAll(w.text, "~", "_")) {
			return "", false
		}
		return w.text, true
	case (plainWord.MatchString(w.text) || w.text == "=") && w.assigns() == shellAssignment.MatchString(w.text):
		// A plain word, or a lone = as in `[ a = b ]`, as long as leaving it
		// unquoted doesn't change whether it's an assignment: FOO=1 that set
		// a variable stays FOO=1, but 'FOO=1', a program's name, stays
		// quoted.
		return w.text, true
	}
	return "'" + strings.ReplaceAll(w.text, "'", `'\''`) + "'", true
}

// ParseCommandLine reads a shell command line the way a command condition
// does and returns its simple commands. It returns an error wrapping
// ErrUnreadable when the line holds anything whose effect its text alone
// doesn't show; a command condition matches no such line. An embedder uses
// it to suggest conditions that a line would match, with
// ShellCommand.Pattern:
//
//	cmds, err := ParseCommandLine("make test 2>&1")
//	// cmds[0].Words() is "make", "test": 2>&1 is dropped
//	// cmds[0].Pattern(1, true) is "make *"
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

// asString returns v as a string when it is one, of any string type: a
// string, or a type defined as one (type Line string).
func asString(v any) (string, bool) {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.String {
		return rv.String(), true
	}
	return "", false
}

// wordTexts returns the text of each word.
func wordTexts(words []shellWord) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w.text
	}
	return out
}

// shellIsPOSIX reports whether the shell that will run a command line is one
// the reader models, bash or zsh. The embedder says which with the fact
// "shell" (a name or path: "bash", "/bin/zsh", "C:\\Git\\bin\\bash.exe"); any
// other value, such as "powershell" or "cmd", means a command condition
// matches nothing, since those shells quote, escape and expand differently
// (in cmd.exe, 'a & b' is two commands). Without the fact, the shell is
// assumed to be bash or zsh unless this program runs on Windows. That guess
// is about the machine running Evaluate, not the one running the command,
// so an embedder that decides calls from other machines must set the fact.
func shellIsPOSIX(c Call) bool {
	v, ok := c.Facts["shell"]
	if !ok {
		return runtime.GOOS != "windows"
	}
	name, ok := v.(string)
	if !ok {
		return false
	}
	name = strings.ToLower(name[strings.LastIndexAny(name, `/\`)+1:])
	name = strings.TrimSuffix(name, ".exe")
	return name == "bash" || name == "zsh"
}

// matchCommand reports whether line, a shell command line, is made only of
// commands that the patterns in value match. It is false, not an error,
// when line isn't text, value isn't valid or the line can't be read: a
// condition that can't be checked doesn't match.
func matchCommand(line any, value any) bool {
	s, ok := asString(line)
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
