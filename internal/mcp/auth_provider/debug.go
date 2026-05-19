package authprovider

import (
	"log"
	"os"
	"strings"
)

// debugEnabled mirrors the mcp.Client debug switch so the OAuth flow can be
// traced end-to-end with a single env var (ATRYUM_MCP_DEBUG=1).
func debugEnabled() bool {
	v := os.Getenv("ATRYUM_MCP_DEBUG")
	return strings.EqualFold(v, "1") || strings.EqualFold(v, "true")
}

func debugf(format string, args ...any) {
	if !debugEnabled() {
		return
	}
	log.Printf("[mcp-auth] "+format, args...)
}
