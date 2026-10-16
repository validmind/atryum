package atryum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A call that stays pending is denied once ATRYUM_HOLD_WAIT_MS has passed,
// rather than waiting until the host kills the hook and lets the call run.
func TestHookDeniesAfterHoldWait(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"invocation_id": "inv_held", "status": "pending_approval"})
	}))
	defer srv.Close()
	cmd := exec.Command(node, filepath.Join("..", "..", "examples", "shared-agent-hook", "atryum-hook.mjs"))
	cmd.Env = append(cmd.Environ(), "HOME="+t.TempDir(), "ATRYUM_URL="+srv.URL, "ATRYUM_HOOK_HOST=claude", "ATRYUM_POLL_MS=50", "ATRYUM_HOLD_WAIT_MS=500")
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"Bash","tool_input":{"command":"make deploy"},"tool_use_id":"t1"}`)
	start := time.Now()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	var got struct {
		HookSpecificOutput struct{ PermissionDecision, PermissionDecisionReason string }
	}
	if err := json.Unmarshal(out, &got); err != nil || got.HookSpecificOutput.PermissionDecision != "deny" ||
		!strings.Contains(got.HookSpecificOutput.PermissionDecisionReason, "still pending approval") {
		t.Fatalf("hook output = %s (%v)", out, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the hook waited %s", took)
	}
}
