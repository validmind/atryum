package policy

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A shell, and how to make it report each simple command's words instead
// of running it: PATH is empty and every builtin but printf is disabled, so
// each command goes to the not-found handler, which prints its words to
// descriptor 3 (redirections in the line can't hide them).
type shell struct {
	name, prelude string
	args          []string
}

var shells = []shell{
	{"bash", `command_not_found_handle() { printf -v l '<%s>' "$@"; printf '%s\n' "$l" >&3; }
for b in $(compgen -b); do [[ $b == printf || $b == enable ]] || enable -n "$b"; done
enable -n enable
`, []string{"--noprofile", "--norc", "-c"}},
	// nonomatch: a glob that matches nothing stays as written, as in bash.
	{"zsh", `setopt nonomatch
command_not_found_handler() { printf -v l '<%s>' "$@"; printf '%s\n' "$l" >&3; }
for b in ${(k)builtins}; do [[ $b == printf || $b == disable ]] || disable $b; done
disable disable
`, []string{"-f", "-c"}},
}

// shellWords is how sh itself splits line into simple commands and words.
// Nothing line names can run, whatever parseShell made of it: the shell runs
// in a sandbox (sandboxed) that lets it start no other program and write no
// file but /dev/null, with no network. HOME is "~", so a ~ the shell expands
// reads back as itself. ok is false when this way of asking can't show what
// the shell reads.
func shellWords(t testing.TB, sh shell, line string, cmds [][]shellWord) (string, bool) {
	sandbox := sandboxed(t, sh.name)
	for _, cmd := range cmds {
		// A command named by a path is run (and stopped by the sandbox), not
		// reported. printf is still the shell's own, and prints elsewhere;
		// ~user is that user's home. A
		// leading FOO=1 is a word to parseShell (a pattern has to name it),
		// not to the shell. A glob as a command's name (A[, a[0]=x) never
		// matches a pattern, which fixes the name, so how a shell reads it
		// doesn't matter.
		if strings.Contains(cmd[0].text, "/") || cmd[0].text == "printf" || shellAssignment.MatchString(cmd[0].text) || cmd[0].glob() {
			return "", false
		}
		for _, w := range cmd {
			for _, i := range w.tildes {
				if i+1 < len(w.text) && w.text[i+1] != '/' {
					return "", false
				}
			}
		}
	}
	// One write per command, so commands run in the background don't interleave.
	cmd := exec.Command(sandbox[0], append(sandbox[1:], sh.prelude+line+"\n")...)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/nonexistent", "HOME=~", "LC_ALL=C"}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	_ = cmd.Wait()
	return sortedLines(strings.TrimRight(string(out), "\n")), true
}

// sortedLines puts a list of commands in order: a command run in the
// background (a & b) may print after the next one, and a command condition
// doesn't depend on their order.
func sortedLines(s string) string {
	lines := strings.Split(s, "\n")
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// sandboxed is the command that runs the named shell so it can start no
// other program, write no file but /dev/null and reach no network: macOS's
// sandbox-exec. Elsewhere the test is skipped rather than run unconfined.
func sandboxed(t testing.TB, name string) []string {
	t.Helper()
	sandboxOnce.Do(newSandboxes)
	sb, ok := sandboxes[name]
	if !ok {
		t.Skip(sandboxSkip)
	}
	if sb.err != nil {
		t.Fatal(sb.err)
	}
	return sb.cmd
}

var (
	sandboxOnce sync.Once
	sandboxes   = map[string]struct {
		cmd []string
		err error
	}{}
	sandboxSkip = "comparing with a shell needs macOS's sandbox-exec, so that nothing a line names can run"
)

func newSandboxes() {
	sb, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return
	}
	for _, sh := range shells {
		bin, err := exec.LookPath(sh.name)
		if err == nil {
			bin, err = filepath.EvalSymlinks(bin)
		}
		if err != nil {
			continue
		}
		profile := fmt.Sprintf(`(version 1)(allow default)(deny network*)(deny process-exec)(allow process-exec (literal %q))(deny file-write*)(allow file-write* (literal "/dev/null"))`, bin)
		cmd := append([]string{sb, "-p", profile, bin}, sh.args...)
		// The sandbox is checked, not assumed: a program the shell names must not start.
		out, _ := exec.Command(cmd[0], append(cmd[1:], "/usr/bin/true && echo ran")...).CombinedOutput()
		var bad error
		if !strings.Contains(string(out), "not permitted") || strings.Contains(string(out), "ran") {
			bad = fmt.Errorf("the sandbox let %s run a program: %s", sh.name, out)
		}
		sandboxes[sh.name] = struct {
			cmd []string
			err error
		}{cmd, bad}
	}
}

func ourWords(cmds [][]shellWord) string {
	var lines []string
	for _, cmd := range cmds {
		var b strings.Builder
		for _, w := range cmd {
			b.WriteString("<" + w.text + ">")
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
}

// FuzzCommandAgreesWithShells checks, for every line parseShell reads, that
// each command bash or zsh runs is one parseShell read, word for word: a
// command condition can't let one run that it didn't see. A shell may run
// fewer (a || that stops early, a glob zsh refuses), never others. go test
// runs the seeds; go test -fuzz=FuzzCommandAgreesWithShells ./pkg/policy
// explores.
func FuzzCommandAgreesWithShells(f *testing.F) {
	for _, s := range []string{
		`gh pr merge 3066`, "  gh  pr\tmerge 3066 ", `cd web && gh pr merge 3066 --squash | tail -3`,
		`git commit -m "fix the build"`, `git commit -m 'fix the build'`, `git commit -m "a; rm -rf ~"`,
		`echo a\; rm -rf ~`, `ls a\*`, "gh pr merge \\\n 3066", `gh pr merge 1 # then; rm -rf ~`,
		`echo "a\"b\\c\d"`, `echo 'it'"'"'s'`, `echo a''b ""`, `go test ./... >/dev/null 2>&1`,
		`echo '2'>/dev/null`, `echo 2>/dev/null x`, `echo \2>/dev/null`, `git status; git diff`,
		`a && b && c |& d`, `a|b&c;d`, "a\n\nb\n", `ls ~/x '~' "~" ~`, `echo a#b`, `echo é ü`,
		`echo "a b"c'd e'`, `echo \"`, `echo "\$x"`, `go test ./... <&- >&2 2>&1 &>/dev/null`,
		`x=1 y`, `rm *.log`, `ls [ab]`, "printf x", "make;true;", "echo a\\\nb",
		"gh pr merge 3066\\\n>/dev/null", "echo x 2\\\n2>/dev/null", "a\\\n&& b",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		cmds, err := parseShell(line, true)
		if err != nil {
			return
		}
		ours := ourWords(cmds)
		for _, sh := range shells {
			got, ok := shellWords(t, sh, line, cmds)
			if !ok || got == "" {
				continue
			}
			left := strings.Split(ours, "\n")
			for _, ran := range strings.Split(got, "\n") {
				i := slices.Index(left, ran)
				if i < 0 {
					t.Fatalf("%q: %s ran %q, not one of ours:\n %q", line, sh.name, ran, ours)
				}
				left = slices.Delete(left, i, i+1)
			}
		}
	})
}

// The harness reports what each shell reads, and runs nothing: a test of
// the test, so a shell that printed nothing can't pass the fuzz target.
func TestShellHarness(t *testing.T) {
	for _, sh := range shells {
		line := "a 'b c' && d >/dev/null"
		cmds, err := parseShell(line, true)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := shellWords(t, sh, line, cmds)
		if want := "<a><b c>\n<d>"; !ok || got != want {
			t.Errorf("%s: %q, want %q", sh.name, got, want)
		}
	}
}
