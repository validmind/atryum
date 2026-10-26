package policy

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A shell, and how to make it report each simple command's words instead
// of running it: PATH is empty and every builtin but printf is disabled, so
// each command goes to the not-found handler, which prints its words to
// descriptor 3 (redirections in the line can't hide them), one write per
// command so background ones don't interleave, ending in a NUL, which no
// word holds (a word may hold a line break). An interactive one reads the
// line on its standard input, as from a terminal, with history expansion
// and its other interactive readings on.
type shell struct {
	name, bin, prelude string
	args               []string
	interactive        bool
}

var (
	bashLike = shell{prelude: `command_not_found_handle() { printf -v l '<%s>' "$@"; printf '%s\0' "$l" >&3; }
for b in $(compgen -b); do [[ $b == printf || $b == enable ]] || enable -n "$b"; done
enable -n enable
`, args: []string{"--noprofile", "--norc"}}
	// nonomatch: a glob that matches nothing stays as written, as in bash.
	zshLike = shell{prelude: `setopt nonomatch
command_not_found_handler() { printf -v l '<%s>' "$@"; printf '%s\0' "$l" >&3; }
for b in ${(k)builtins}; do [[ $b == printf || $b == disable ]] || disable $b; done
disable disable
`, args: []string{"-f"}}
)

// shells are every bash and zsh on this machine the comparison can run, each
// run with -c and interactively: the ones on PATH and the system's own
// (macOS's /bin/bash is 3.2), each in a sandbox (sandbox) that lets it
// start no other program, write no file but /dev/null and reach no
// network, checked before use. Without one there are none, rather than any
// run unconfined, and the tests that compare skip; with
// ATRYUM_SHELL_COMPARISON=required, as CI sets it, they fail instead, so
// the comparison can't quietly stop running.
func shells(t testing.TB) []shell {
	t.Helper()
	shellsOnce.Do(findShells)
	if shellsErr != nil {
		t.Fatal(shellsErr)
	}
	if len(sandboxedShells) == 0 && os.Getenv("ATRYUM_SHELL_COMPARISON") == "required" {
		t.Fatalf("ATRYUM_SHELL_COMPARISON=required, but no shell can be compared with: %s", noSandbox())
	}
	return sandboxedShells
}

var (
	shellsOnce      sync.Once
	sandboxedShells []shell
	shellsErr       error
)

// sandbox is the command that runs bin, a shell, confined: on macOS
// sandbox-exec with a profile that denies starting any other program,
// writing any file but /dev/null and the network; on Linux bubblewrap
// (bwrap), in a mount namespace where /usr/bin holds only the shell (so a
// program named by its path doesn't exist), /usr and the temporary
// directory are read-only, /dev holds only the device files, and no
// network, PID or IPC namespace is shared. Neither needs root. ok is false
// when this machine has neither.
func sandbox(bin string) (name string, args []string, ok bool) {
	switch runtime.GOOS {
	case "darwin":
		sb, err := exec.LookPath("sandbox-exec")
		if err != nil {
			return "", nil, false
		}
		profile := fmt.Sprintf(`(version 1)(allow default)(deny network*)(deny process-exec)(allow process-exec (literal %q))(deny file-write*)(allow file-write* (literal "/dev/null"))`, bin)
		return sb, []string{"-p", profile, bin}, true
	case "linux":
		bw, err := exec.LookPath("bwrap")
		if err != nil {
			return "", nil, false
		}
		// /usr first, then /usr/bin and the other program directories
		// emptied over it, then the shell alone bound back in: bwrap applies
		// these in order. The test's temporary directory is where a line
		// runs (report's dir), read-only.
		args := []string{"--ro-bind", "/usr", "/usr", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--symlink", "usr/bin", "/bin",
			"--tmpfs", "/usr/bin", "--tmpfs", "/usr/sbin", "--tmpfs", "/usr/libexec", "--tmpfs", "/usr/local", "--ro-bind", bin, bin,
			"--ro-bind", os.TempDir(), os.TempDir(), "--dev", "/dev", "--unshare-all", "--die-with-parent", bin}
		return bw, args, true
	}
	return "", nil, false
}

// noSandbox says what's missing for the comparison on this machine.
func noSandbox() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS's sandbox-exec wasn't found"
	case "linux":
		return "bubblewrap (bwrap) isn't installed, or bash and zsh aren't; on Ubuntu 24.04 bwrap also needs kernel.apparmor_restrict_unprivileged_userns=0"
	}
	return "the comparison runs on macOS (sandbox-exec) and Linux (bwrap) only"
}

func findShells() {
	seen := map[string]bool{}
	for _, c := range []struct {
		name string
		like shell
	}{{"bash", bashLike}, {"/bin/bash", bashLike}, {"zsh", zshLike}, {"/bin/zsh", zshLike}} {
		bin, err := exec.LookPath(c.name)
		if err == nil {
			bin, err = filepath.EvalSymlinks(bin)
		}
		if err != nil || seen[bin] {
			continue
		}
		seen[bin] = true
		sb, sbArgs, ok := sandbox(bin)
		if !ok {
			return
		}
		for _, interactive := range []bool{false, true} {
			sh := c.like
			sh.bin, sh.interactive = sb, interactive
			sh.args = append(slices.Clone(sbArgs), c.like.args...)
			sh.name = bin + " -c"
			if interactive {
				sh.args, sh.name = append(sh.args, "-i"), bin+" -i"
			}
			// The sandbox is checked, not assumed: the shell starts in it,
			// and can't start a program it names.
			// The words quoted apart: an interactive shell may echo what it
			// reads, and only what runs prints them whole.
			if out := run(sh, "echo in''side; /usr/bin/true && echo r''an", ""); !strings.Contains(out, "inside") || strings.Contains(out, "ran") {
				shellsErr = fmt.Errorf("%s in the sandbox: %q", sh.name, out)
				return
			}
			// And it can report a command's words: bash before 4 has no
			// command_not_found_handle (macOS's /bin/bash is 3.2), so it can't.
			if got, err := report(sh, "a b", os.TempDir()); err != nil || got != "<a><b>\x00" {
				continue
			}
			sandboxedShells = append(sandboxedShells, sh)
		}
	}
}

// run runs script in sh, with no prelude, and returns all it printed.
func run(sh shell, script, dir string) string {
	cmd := command(context.Background(), sh, script)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// command is sh set to run script: as -c's argument, or, interactive, on
// its standard input.
func command(ctx context.Context, sh shell, script string) *exec.Cmd {
	if sh.interactive {
		cmd := exec.CommandContext(ctx, sh.bin, sh.args...)
		cmd.Stdin = strings.NewReader(script)
		return cmd
	}
	return exec.CommandContext(ctx, sh.bin, append(slices.Clone(sh.args), "-c", script)...)
}

// ran is the commands sh runs for line, each as its words, or ok false
// when this way of asking can't show it: a program named by a path is run
// (and stopped by the sandbox), not reported; printf is the shell's own and
// prints elsewhere; a glob as a program's name (A[, a[0]=x) is read
// differently by each shell, and never matches a pattern, which names its
// program; ~user is that user's home. HOME is "~", so a ~ the shell expands
// reads back as itself.
func ran(t testing.TB, sh shell, line string, cmds [][]shellWord) ([]string, bool) {
	for _, cmd := range cmds {
		prog := 0
		for prog < len(cmd)-1 && cmd[prog].assigns() {
			prog++
		}
		if p := cmd[prog]; strings.Contains(p.text, "/") || p.text == "printf" || p.glob {
			return nil, false
		}
		for _, w := range cmd {
			for _, i := range w.tildes {
				if i+1 < len(w.text) && w.text[i+1] != '/' {
					return nil, false
				}
			}
		}
	}
	out, err := report(sh, line, t.TempDir())
	if err != nil {
		t.Fatalf("%s on %q: %v", sh.name, line, err)
	}
	if out == "" {
		return nil, true
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00"), true
}

// report runs line in sh, in dir, and returns what its not-found handler
// printed: each command it ran, each ending in a NUL.
func report(sh shell, line, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := command(ctx, sh, sh.prelude+line+"\n")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/nonexistent", "HOME=~", "LC_ALL=C"}
	if sh.interactive {
		// A terminal's: in the C locale, readline drops bytes past ASCII.
		// glibc has C.UTF-8 built in; macOS has en_US.UTF-8.
		cmd.Env[2] = "LC_ALL=en_US.UTF-8"
		if runtime.GOOS == "linux" {
			cmd.Env[2] = "LC_ALL=C.UTF-8"
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return "", err
	}
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	_ = cmd.Wait()
	return string(out), ctx.Err()
}

// passed is what a shell passes to each command of cmds: its words after
// any variables set for it, in the not-found handler's form. From a glob
// on, it's only what the words start with: the shell passes the files the
// glob matches, which a pattern never names (only a last * takes them).
func passed(cmds [][]shellWord) []string {
	var out []string
	for _, cmd := range cmds {
		for len(cmd) > 0 && cmd[0].assigns() {
			cmd = cmd[1:]
		}
		if len(cmd) == 0 {
			continue // only variables: nothing runs
		}
		var b strings.Builder
		for _, w := range cmd {
			if w.glob {
				b.WriteString("…")
				break
			}
			b.WriteString("<" + w.text + ">")
		}
		out = append(out, b.String())
	}
	return out
}

// runs reports whether c, a command a shell ran, is p, one of passed's.
func runs(c, p string) bool {
	if prefix, ok := strings.CutSuffix(p, "…"); ok {
		return strings.HasPrefix(c, prefix)
	}
	return c == p
}

// FuzzCommandAgreesWithShells checks two things for every line the reader
// reads. The line's commands, each drafted as an exact pattern
// (ShellCommand.Pattern), match the line, everywhere. And, where shells can
// be compared (shells), each command bash or zsh runs, with -c or
// interactively, is one the reader read, word for word: a command condition
// can't let one run that it didn't see. A shell may run fewer (a || that
// stops early, a glob zsh refuses), never others. go test runs the seeds;
// go test -fuzz=FuzzCommandAgreesWithShells ./pkg/policy explores.
func FuzzCommandAgreesWithShells(f *testing.F) {
	for _, s := range []string{
		`gh pr merge 3066`, "  gh  pr  merge 3066 ", `cd web && gh pr merge 3066 --squash | tail -3`,
		`git commit -m "fix the build"`, `git commit -m 'fix the build'`, `git commit -m "a; rm -rf ~"`,
		`git commit -m "Done!"`, `echo "a != b"`, `echo a\; rm -rf ~`, `ls a\*`, "gh pr merge \\\n 3066",
		`echo "a\"b\\c\d"`, `echo 'it'"'"'s'`, `echo a''b ""`, `go test ./... >/dev/null 2>&1`,
		`echo 2>/dev/null x`, `git status; git diff`, `a && b && c |& d`, `a|b&c;d`, "a\n\nb\n",
		`ls ~/x '~' "~" ~`, `echo a#b`, `echo é ü`, `echo "a b"c'd e'`, `echo \"`, `echo "\$x"`,
		`go test ./... >&2 2>&1 &>/dev/null`, `x=1 y`, `a; FOO=1 b c`, `rm *.log`, `ls [ab]`,
		"make;true;", "echo a\\\nb", `find . -name x -exec rm {} +`, `test a = b`, `git show HEAD^`,
		"gh pr merge 3066\\\n>/dev/null", "echo x 2\\\n2>/dev/null", "a\\\n&& b", "a || b",
		`echo "'" 'x!-2'`, `echo "a ! b"`, `[ -f go.mod ] && make`, `go test >& /dev/null`, `find . -exec rm {} +`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		cmds, err := readLine(line)
		if err != nil {
			return
		}
		var exact []any
		for _, c := range cmds {
			if p, ok := (ShellCommand{words: c}).Pattern(len(c), false); ok {
				exact = append(exact, p)
			}
		}
		// Unless a word is a glob, which no pattern names.
		if len(exact) == len(cmds) && !matchCommand(line, exact) {
			t.Fatalf("%q: its own patterns %q don't match it", line, exact)
		}
		for _, sh := range shells(t) {
			got, ok := ran(t, sh, line, cmds)
			if !ok {
				continue
			}
			left := passed(cmds)
			for _, c := range got {
				i := slices.IndexFunc(left, func(p string) bool { return runs(c, p) })
				if i < 0 {
					t.Fatalf("%q: %s ran %q, not one of ours: %q", line, sh.name, c, passed(cmds))
				}
				left = slices.Delete(left, i, i+1)
			}
		}
	})
}

// The harness reports what each shell reads, and runs nothing: a test of
// the test, so a shell that printed nothing can't pass the fuzz target.
func TestShellHarness(t *testing.T) {
	if len(shells(t)) == 0 {
		t.Skip("comparing with a shell needs a sandbox, so that nothing a line names can run: " + noSandbox())
	}
	for _, sh := range shells(t) {
		t.Logf("comparing with %s", sh.name)
		line := "a 'b c' && FOO=1 d >/dev/null"
		cmds, err := readLine(line)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := ran(t, sh, line, cmds)
		if want := []string{"<a><b c>", "<d>"}; !ok || !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", sh.name, got, want)
		}
	}
}
