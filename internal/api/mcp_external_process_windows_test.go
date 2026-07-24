//go:build (mcpeverything || mcpstandalone) && windows

package api

import "os/exec"

// configureExternalProcessGroup is a no-op on Windows: process-group
// signaling works differently there, and these test fixtures aren't
// exercised on Windows CI. killExternalProcessGroup falls back to killing
// just the directly-spawned process, which may leak a grandchild on this
// platform — see mcp_external_process_unix_test.go for the Unix behavior
// this stands in for.
func configureExternalProcessGroup(cmd *exec.Cmd) {}

func killExternalProcessGroup(pid int) {}
