//go:build windows

package mcp

import "os/exec"

// configureStdioProcessGroup is a no-op on Windows: process-group signalling
// is POSIX-specific (see stdio_process_unix.go). Atryum's release targets
// are darwin/linux only; this stub exists solely so the package still
// builds on Windows, at today's level of process cleanup (Cmd's default
// context-cancellation behavior, which only kills the direct child).
func configureStdioProcessGroup(cmd *exec.Cmd) {}
