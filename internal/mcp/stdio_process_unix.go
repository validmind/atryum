//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
	"time"
)

// configureStdioProcessGroup puts cmd in its own process group (Setpgid) so
// killStdioProcessGroup can terminate it and any descendants it spawns
// (e.g. a wrapper script's own child process) in one signal, and sets a
// bounded WaitDelay so Wait() doesn't hang forever if the kill somehow
// doesn't land. Without this, canceling the command's context only kills
// the directly-spawned process: a grandchild that inherited the stdout
// pipe's write end keeps it open, so the parent's read never sees EOF and
// a cancellation-based timeout (idle/max-duration) never actually unblocks
// the read it was supposed to abort.
func configureStdioProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killStdioProcessGroup(cmd.Process.Pid)
	}
}

// killStdioProcessGroup sends SIGKILL to the process group headed by pid.
//
// os/exec's default Cancel (os.Process.Kill) is safe against PID reuse
// because os.Process tracks internally whether it has already reaped the
// process, and refuses to signal after that point. Our Cancel bypasses
// os.Process entirely — a raw syscall.Kill(-pid, ...) is the only way to
// reach the whole group, not just the one process — so it doesn't get that
// same protection for free. Getpgid closes most of that gap: since
// Setpgid made our child its own group leader at spawn time (pgid == pid),
// verifying that still holds immediately before signaling means we skip
// the kill if the process has already exited (Getpgid returns ESRCH) or if
// pid has been recycled by an unrelated process that is not a matching
// group leader. This narrows the residual race to the syscall gap between
// the Getpgid check and the Kill call, rather than the much larger window
// between process exit and this function running.
func killStdioProcessGroup(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// Already gone, or otherwise unqueryable — nothing safe to kill.
		return nil
	}
	if pgid != pid {
		// pid no longer heads the process group we spawned it into; refuse
		// to signal a group we don't recognize.
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}
