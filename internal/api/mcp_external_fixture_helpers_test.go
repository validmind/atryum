//go:build mcpeverything || mcpstandalone

package api

import (
	"net"
	"testing"
)

// freePort allocates an ephemeral TCP port for a spawned fixture server to
// listen on, closing the probe listener immediately so the port is free
// again by the time the caller passes it to the child process. Shared by
// mcp_everything_test.go and mcp_standalone_stream_test.go.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
