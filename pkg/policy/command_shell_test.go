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

// shell is one way of running a real shell for the comparison: a bash or zsh
// binary in a sandbox, how it's started, and whether it reads the line
// interactively.
//
// The trick that shows what a shell would run, without running it: report
// sets PATH to a directory that doesn't exist, and the prelude disables every
// builtin but printf, so every command the line names goes to the shell's
// "command not found" handler. The prelude defines that handler to print the
// command's words instead. For the line `git status && make`, it prints
//
//	<git><status>\x00<make>\x00
//
// Each word is wrapped in <>, and each command ends in a NUL, which no word
// can hold (a word may hold a line break). The handler writes to descriptor
// 3, so a redirection in the line (>/dev/null) can't hide what it prints, and
// writes each command at once, so commands run in the background don't
// interleave.
//
// An interactive shell reads the line on its standard input, as if typed at a
// terminal, so history expansion and its other interactive behavior are on.
type shell struct {
	// name is shown in messages ("/bin/zsh -i"); bin is the program started,
	// the sandbox, which starts the shell; prelude is the script run before
	// the line, which sets up the handler.
	name, bin, prelude string
	args               []string // bin's arguments: the sandbox's, the shell's path and the shell's flags
	interactive        bool     // read the line on stdin, as typed (true), or as -c's argument (false)
}

var (
	// bashLike and zshLike are each shell's prelude and flags; findShells adds
	// the binary and the sandbox. bash runs with --noprofile --norc, and zsh with
	// -f, so no startup file changes how they read a line.
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

// shells returns every bash and zsh on this machine the comparison can run,
// each in two ways: with -c and interactively. It looks for the ones on PATH
// and the system's own (macOS's /bin/bash is 3.2). Each runs in a sandbox
// (see sandbox) that lets it start no other program, write no file but
// /dev/null and reach no network, and each is checked before use (see
// findShells).
//
// With no sandbox on this machine there are no shells, rather than any
// running unconfined, and the tests that compare skip. With
// ATRYUM_SHELL_COMPARISON=required, as CI sets it, they fail instead. They
// also fail then when any of bash -c, bash -i, zsh -c and zsh -i can't be
// compared, so the comparison can't quietly stop running, or quietly shrink to
// fewer shells.
func shells(t testing.TB) []shell {
	t.Helper()
	shellsOnce.Do(findShells)
	if shellsErr != nil {
		t.Fatal(shellsErr)
	}
	if os.Getenv("ATRYUM_SHELL_COMPARISON") == "required" {
		if len(sandboxedShells) == 0 {
			t.Fatalf("ATRYUM_SHELL_COMPARISON=required, but no shell can be compared with: %s", noSandbox())
		}
		var missing []string
		for _, k := range shellKinds {
			if why, ok := shellsMissing[k]; ok {
				missing = append(missing, fmt.Sprintf("%s (%s)", k, strings.Join(why, "; ")))
			}
		}
		if len(missing) > 0 {
			t.Fatalf("ATRYUM_SHELL_COMPARISON=required, but these can't be compared with: %s", strings.Join(missing, ", "))
		}
	}
	return sandboxedShells
}

// shellKinds are the shells the comparison needs, whichever binary runs
// them.
var shellKinds = []string{"bash -c", "bash -i", "zsh -c", "zsh -i"}

var (
	shellsOnce      sync.Once
	sandboxedShells []shell
	shellsErr       error
	// shellsMissing holds each of shellKinds that no binary could run, with
	// why each binary tried for it failed.
	shellsMissing map[string][]string
)

// sandbox returns the command that runs bin, a shell, confined, so that
// nothing a test line names can actually run. Neither way needs root:
//
//   - macOS: sandbox-exec, with a profile that denies starting any other
//     program, writing any file but /dev/null, and the network
//   - Linux: bubblewrap (bwrap), in a mount namespace where /usr/bin holds
//     only the shell (so a program named by its path, like /usr/bin/rm,
//     doesn't exist), /usr and the temporary directory are read-only, /dev
//     holds only the device files, and no network, PID or IPC namespace is
//     shared
//
// ok is false when this machine has neither.
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

// noSandbox says what's missing for the comparison on this machine, for a
// skip or failure message.
func noSandbox() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS's sandbox-exec wasn't found"
	case "linux":
		return "bubblewrap (bwrap) isn't installed, or bash and zsh aren't; on Ubuntu 24.04 bwrap also needs kernel.apparmor_restrict_unprivileged_userns=0"
	}
	return "the comparison runs on macOS (sandbox-exec) and Linux (bwrap) only"
}

// findShells fills sandboxedShells, shellsMissing and shellsErr, once (see
// shells). It starts each bash and zsh it finds in the sandbox, and checks two
// things before using it: that the sandbox really stops it starting a
// program, and that it can report a command's words (see shell).
func findShells() {
	shellsMissing = map[string][]string{}
	for _, k := range shellKinds {
		shellsMissing[k] = nil
	}
	// miss records why a binary couldn't run kind, unless another already
	// does: the system's bash 3.2 failing doesn't matter once Homebrew's
	// bash 5 has passed.
	miss := func(kind, why string) {
		if whys, ok := shellsMissing[kind]; ok {
			shellsMissing[kind] = append(whys, why)
		}
	}
	seen := map[string]bool{}
	for _, c := range []struct {
		name string
		like shell
	}{{"bash", bashLike}, {"/bin/bash", bashLike}, {"zsh", zshLike}, {"/bin/zsh", zshLike}} {
		family := filepath.Base(c.name)
		bin, err := exec.LookPath(c.name)
		if err == nil {
			bin, err = filepath.EvalSymlinks(bin)
		}
		if err != nil {
			for _, mode := range []string{" -c", " -i"} {
				miss(family+mode, err.Error())
			}
			continue
		}
		// The same binary can turn up twice: bash on PATH may be /bin/bash.
		if seen[bin] {
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
			kind := family + " -c"
			sh.name = bin + " -c"
			if interactive {
				sh.args, sh.name, kind = append(sh.args, "-i"), bin+" -i", family+" -i"
			}
			// Check the sandbox rather than assume it: the shell must start in
			// it (it prints inside) and fail to start /usr/bin/true (it never
			// prints ran). The words are written in''side and r''an because
			// an interactive shell may echo the line it reads; only an echo
			// that runs prints them whole.
			if out := run(sh, "echo in''side; /usr/bin/true && echo r''an", ""); !strings.Contains(out, "inside") || strings.Contains(out, "ran") {
				shellsErr = fmt.Errorf("%s in the sandbox: %q", sh.name, out)
				return
			}
			// Check that the shell can report a command's words. bash before
			// 4.0 has no command_not_found_handle, so it can't, and is
			// skipped: macOS's /bin/bash is 3.2.
			if got, err := report(sh, "a b", os.TempDir()); err != nil || got != "<a><b>\x00" {
				miss(kind, fmt.Sprintf("%s reported %q, %v", sh.name, got, err))
				continue
			}
			sandboxedShells = append(sandboxedShells, sh)
			delete(shellsMissing, kind)
		}
	}
}

// run runs script in sh, in dir, without the prelude, and returns everything
// it printed, stdout and stderr together.
func run(sh shell, script, dir string) string {
	cmd := command(context.Background(), sh, script)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// command returns the exec.Cmd that runs script in sh: as the argument of -c,
// or, for an interactive shell, on its standard input, as if typed.
func command(ctx context.Context, sh shell, script string) *exec.Cmd {
	if sh.interactive {
		cmd := exec.CommandContext(ctx, sh.bin, sh.args...)
		cmd.Stdin = strings.NewReader(script)
		return cmd
	}
	return exec.CommandContext(ctx, sh.bin, append(slices.Clone(sh.args), "-c", script)...)
}

// ran returns the commands sh runs for line, each in the handler's form
// (<git><status>). ok is false, and the caller skips the line, when the
// handler can't show them:
//
//   - a program named by a path (./x, /bin/ls) is started, not reported
//     (and the sandbox stops it)
//   - printf is the builtin the handler itself uses, so it prints instead of
//     being reported
//   - a glob as a program's name (A[, a[0]=x) is read differently by each
//     shell, and never matches a pattern anyway, since a pattern names its
//     program
//   - ~user is that user's home directory, which differs by machine
//
// report sets HOME to "~", so a lone ~ that the shell expands reads back as
// itself: `ls ~/x` reports <ls><~/x>.
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

// report runs line in sh, in dir, after the prelude, and returns what the
// handler printed: each command the shell ran, ending in a NUL. For
// `a b && c` that is "<a><b>\x00<c>\x00". It gives up after 10 seconds.
func report(sh shell, line, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := command(ctx, sh, sh.prelude+line+"\n")
	cmd.Dir = dir
	// PATH holds no programs, so every command reaches the handler; HOME is
	// ~ (see ran); the C locale, so the machine's locale can't change how
	// the line is read.
	cmd.Env = []string{"PATH=/nonexistent", "HOME=~", "LC_ALL=C"}
	if sh.interactive {
		// A UTF-8 locale, as a terminal has: in the C locale, readline drops
		// bytes past ASCII, so é would vanish. glibc has C.UTF-8 built in;
		// macOS has en_US.UTF-8.
		cmd.Env[2] = "LC_ALL=en_US.UTF-8"
		if runtime.GOOS == "linux" {
			cmd.Env[2] = "LC_ALL=C.UTF-8"
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	// The pipe's write end is the shell's descriptor 3 (ExtraFiles start at
	// 3), where the handler writes.
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return "", err
	}
	// Close our copy of the write end, so ReadAll ends when the shell exits.
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	_ = cmd.Wait()
	return string(out), ctx.Err()
}

// passed returns what the reader says a shell passes to each command of
// cmds, in the handler's form, to compare with what ran reports. Variables
// set for a command (FOO=1) aren't words it receives, so they're left out.
// From a glob on, it's only a prefix ending in …: the shell passes the files
// the glob matches, which the test can't know. For `FOO=1 rm a *.log x`:
//
//	<rm><a>…
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

// runs reports whether c, a command a shell ran, is p, one of passed's:
// equal to it, or, when p ends in …, starting with what comes before the ….
func runs(c, p string) bool {
	if prefix, ok := strings.CutSuffix(p, "…"); ok {
		return strings.HasPrefix(c, prefix)
	}
	return c == p
}

// FuzzCommandAgreesWithShells checks every line the reader accepts in two
// ways:
//
//  1. Each of the line's commands, written as an exact pattern by
//     ShellCommand.Pattern, matches the line, unless a word is a glob, which
//     no pattern can name.
//  2. Where shells can be compared (see shells), every command bash or zsh
//     runs for the line, with -c or interactively, is one the reader read,
//     word for word. So a command condition can never let a command run that
//     it didn't see. A shell may run fewer (in `a || b`, b may never run; zsh
//     may refuse a glob and run nothing), never others.
//
// `go test` runs the seeds below and those saved in testdata/fuzz. To search
// for new lines that fail, run
//
//	go test -fuzz=FuzzCommandAgreesWithShells ./pkg/policy
//
// The fuzzer saves each failing line it finds to testdata/fuzz, so it runs as
// a seed from then on.
func FuzzCommandAgreesWithShells(f *testing.F) {
	// Lines that use each piece of syntax the reader accepts.
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
		// Check 1. A command with a glob in it has no pattern, so exact is
		// short and the check is skipped.
		if len(exact) == len(cmds) && !matchCommand(line, exact) {
			t.Fatalf("%q: its own patterns %q don't match it", line, exact)
		}
		// Check 2. Each command a shell ran must be one of ours, and each of
		// ours can account for only one.
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

// TestShellHarness tests the test. Each shell must report the commands of a
// known line, and run nothing. Without this, a shell whose handler printed
// nothing would let the fuzz target pass while comparing nothing. For
// `a 'b c' && FOO=1 d >/dev/null` it expects <a><b c> and <d>.
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
