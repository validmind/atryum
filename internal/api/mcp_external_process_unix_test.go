//go:build (mcpeverything || mcpstandalone) && !windows

package api

import (
	"os/exec"
	"syscall"
)

// configureExternalProcessGroup puts cmd in its own process group so
// killExternalProcessGroup can terminate it and any descendants it spawns
// (e.g. npx execs the actual server binary as a child process; uv run may
// do likewise) in one signal. Without this, killing just the directly
// spawned process can leave that child running — the same class of leak
// internal/mcp's stdio upstream handling guards against (see
// stdio_process_unix.go) for the same reason. Shared by both real-server
// e2e fixtures (mcp_everything_test.go, mcp_standalone_stream_test.go).
func configureExternalProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killExternalProcessGroup sends SIGKILL to the process group headed by
// pid, verifying pid still heads that group (Setpgid made it the group
// leader at spawn time) immediately before signaling — see
// internal/mcp/stdio_process_unix.go's killStdioProcessGroup for the full
// reasoning on why this check matters and what residual race it leaves.
func killExternalProcessGroup(pid int) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
