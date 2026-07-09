// Command probe acts as a synthetic Google Agent Gateway: it sends real Envoy
// ext_authz CheckRequests to a running Atryum callout and prints the allow/deny
// verdict. Use it to exercise the callout end to end over the real gRPC wire.
//
//	go run ./examples/google-agent-gateway/local-demo/probe [addr]   # default localhost:8090
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"time"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	rpccode "google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// bearer attaches a Cloud Run identity token as gRPC per-RPC credentials.
type bearer struct{ tok string }

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.tok}, nil
}
func (b bearer) RequireTransportSecurity() bool { return true }

func check(client authv3.AuthorizationClient, agent, tool string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Unique per run so re-runs are not deduped by Atryum's idempotency key.
	reqID := fmt.Sprintf("%s-%d", tool, time.Now().UnixNano())
	resp, err := client.Check(ctx, &authv3.CheckRequest{
		Attributes: &authv3.AttributeContext{
			ContextExtensions: map[string]string{"agent_id": agent, "mcp.toolName": tool},
			Request: &authv3.AttributeContext_Request{
				Http: &authv3.AttributeContext_HttpRequest{
					Headers: map[string]string{"x-request-id": reqID},
				},
			},
		},
	})
	if err != nil {
		fmt.Printf("  tool=%-14s ERROR  %v\n", tool, err)
		return
	}
	if resp.GetStatus().GetCode() == int32(rpccode.Code_OK) {
		fmt.Printf("  tool=%-14s \033[32mALLOW\033[0m  (agent forwards the tool)\n", tool)
		return
	}
	fmt.Printf("  tool=%-14s \033[31mDENY\033[0m   403 %q\n", tool, resp.GetDeniedResponse().GetBody())
}

func main() {
	addr := "localhost:8090"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	// Accept a full URL; gRPC targets are host:port, not scheme://host.
	addr = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(addr, "https://"), "http://"), "/")
	// TLS for Cloud Run (*.run.app:443); plaintext for local.
	creds := insecure.NewCredentials()
	if strings.Contains(addr, "run.app") || os.Getenv("PROBE_TLS") == "1" {
		if !strings.Contains(addr, ":") {
			addr += ":443"
		}
		host := addr[:strings.LastIndex(addr, ":")]
		creds = credentials.NewTLS(&tls.Config{ServerName: host})
	}
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if tok := os.Getenv("PROBE_TOKEN"); tok != "" {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(bearer{tok}))
	}
	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer conn.Close()
	client := authv3.NewAuthorizationClient(conn)

	fmt.Printf("=== synthetic Agent Gateway ext_authz probe -> %s ===\n", addr)
	fmt.Println("(rule: auto_deny bash; default policy: always_approve)")
	check(client, "demo-agent", "read_file")
	check(client, "demo-agent", "bash")
	check(client, "demo-agent", "transfer_funds")
}
