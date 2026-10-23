package policy

import (
	"context"
	"testing"
)

func TestAmbiguousPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		// Paths a rule can settle.
		{"absolute Unix path", "/etc/passwd", false},
		{"absolute Windows path", `C:\Windows\System32`, false},
		{"Windows path with spaces", `C:\Program Files\App\x.exe`, false},
		{"detours cleaning settles", "/tmp/../etc/passwd", false},
		{"long-path prefix", `\\?\C:\Windows`, false},
		{"device prefix on a drive", `\\.\C:\Windows`, false},
		{"relative path that stays put", "src/main.go", false},
		{"relative path with a detour inside", "src/../docs/x.md", false},
		{"a tilde inside a name", "/home/me/notes~", false},
		{"a dollar amount", "costs $5/month", false},
		{"a percent", "50%/100%", false},
		{"a URL", "https://example.com/a/b", false},
		{"multi-line text is not a path", "cd ~\ncat $HOME/x", false},
		{"empty", "", false},

		// Paths only the machine that opens them can resolve.
		{"Windows variable", `%SystemRoot%\System32`, true},
		{"Windows variable with parentheses", `%ProgramFiles(x86)%\App`, true},
		{"PowerShell variable", `$env:USERPROFILE\.ssh`, true},
		{"Unix variable", "$HOME/.ssh/id_rsa", true},
		{"braced variable", "${HOME}/.ssh/id_rsa", true},
		{"home directory", "~/.ssh/id_rsa", true},
		{"another user's home", "~root/.ssh", true},
		{"short name", `C:\PROGRA~1\App`, true},
		{"short name behind a long-path prefix", `\\?\C:\PROGRA~1\App`, true},
		{"climb behind a device prefix", `\\.\C:\..\x`, false},
		{"short name with an extension", `C:\Docs\SECRET~1.TXT`, true},
		{"admin share", `\\localhost\C$\Windows`, true},
		{"UNC share", `\\fileserver\share\x`, true},
		{"long-path UNC", `\\?\UNC\fileserver\share`, true},
		{"named pipe", `\\.\pipe\x`, true},
		{"raw disk", `\\.\PhysicalDrive0`, true},
		{"NT device path", `\??\GLOBALROOT\Device`, true},
		{"drive-relative", `C:Windows\System32`, true},
		{"climbs out", "../../etc/passwd", true},
		{"climbs out on Windows", `..\..\Windows`, true},
		{"climbs out after a detour", "a/../../etc", true},
		{"proc root link", "/proc/self/root/etc/passwd", true},
		{"proc cwd of a process", "/proc/1/cwd/x", true},
		{"open file descriptor", "/dev/fd/3", true},
		{"escaped climb", "%2E%2E%2F%2E%2E%2Fetc", true},
		{"full-width tilde", "～/.ssh", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AmbiguousPath(map[string]any{"path": tc.path}); got != tc.want {
				t.Errorf("AmbiguousPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestAmbiguousPathLooksEverywhere(t *testing.T) {
	args := map[string]any{"ops": []any{map[string]any{"from": "/tmp/a", "to": `%APPDATA%\x`}}, "n": float64(1), "dry": true}
	if !AmbiguousPath(args) {
		t.Error("a nested path should be found")
	}
	if AmbiguousPath(map[string]any{"n": float64(1), "dry": true, "nothing": nil}) {
		t.Error("no text: nothing to flag")
	}
	if !AmbiguousPath(map[string]any{"f": func() {}}) {
		t.Error("a value that cannot be inspected counts as ambiguous")
	}
}

// The setup the package doc recommends: allow rules on the paths a tool may
// use, everything else to a person, and a firm rule on ambiguous paths. An
// odd spelling never matches the allow rule, so it reaches a person rather
// than slipping past a deny rule.
func TestAllowListedPathsFailSafe(t *testing.T) {
	firm := []Rule{{ID: "F-PATH", Tool: "read_file|write_file", When: []Cond{{Field: "facts.ambiguous_path", Op: "eq", Value: true}}, Action: ActionHuman}}
	agent := []Rule{
		{ID: "A-WORK", Tool: "read_file|write_file", When: []Cond{{Field: "args.path", Op: "prefix", Value: `C:\work\`}}, Action: ActionAllow},
		{ID: "A-REST", Tool: "read_file|write_file", Action: ActionHuman},
	}
	for path, want := range map[string]Action{
		`C:\work\a.txt`:          ActionAllow,
		`c:/WORK/b.txt`:          ActionHuman, // cleaning never loosens: an allow rule matches the spelling it names
		`C:\work\..\Windows\SAM`: ActionHuman,
		`C:\Windows\SAM`:         ActionHuman,
		`C:\work\PROGRA~1\x`:     ActionHuman, // inside the allowed folder, but a short name could lead anywhere
		`\\localhost\C$\work\a`:  ActionHuman,
		`%USERPROFILE%\x`:        ActionHuman,
	} {
		args := map[string]any{"path": path}
		call := Call{Tool: "read_file", Args: args, Facts: map[string]any{"ambiguous_path": AmbiguousPath(args)}}
		if res := Evaluate(context.Background(), firm, agent, call, nil); res.Action != want {
			t.Errorf("%q: got %s by %v, want %s", path, res.Action, res.Rule, want)
		}
	}
}
