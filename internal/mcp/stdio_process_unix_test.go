//go:build !windows

package mcp

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestKillStdioProcessGroupTerminatesRunningProcess(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	configureStdioProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := killStdioProcessGroup(cmd.Process.Pid); err != nil {
		t.Fatalf("killStdioProcessGroup: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process was not killed within 5s")
	}
}

// TestKillStdioProcessGroupIsNoOpForAlreadyExitedProcess proves the
// Getpgid-verification guard: once a process has exited and been reaped,
// killStdioProcessGroup must not blindly signal its (now-recycled-eligible)
// pid. Getpgid on an already-reaped pid returns an error (no such
// process), so the function returns nil without calling Kill at all.
func TestKillStdioProcessGroupIsNoOpForAlreadyExitedProcess(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	configureStdioProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	if err := killStdioProcessGroup(pid); err != nil {
		t.Fatalf("expected a no-op (nil error) for an already-exited pid, got %v", err)
	}
}
