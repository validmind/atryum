package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type invokeRequest struct {
	Tool           string          `json:"tool"`
	Input          json.RawMessage `json:"input"`
	RequestID      string          `json:"request_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

func main() {
	baseURL := flag.String("base-url", "http://localhost:8080", "Atryum base URL")
	server := flag.String("server", "shortcut", "configured upstream server name")
	tool := flag.String("tool", "workflows-list", "tool name to invoke")
	input := flag.String("input", `{}`, "JSON object for tool input")
	requestID := flag.String("request-id", "", "optional request id")
	idempotencyKey := flag.String("idempotency-key", fmt.Sprintf("cli-%d", time.Now().UnixNano()), "optional idempotency key")
	pretty := flag.Bool("pretty", true, "pretty-print JSON response")
	flag.Parse()

	if strings.TrimSpace(*server) == "" {
		log.Fatal("-server is required")
	}
	if !json.Valid([]byte(*input)) {
		log.Fatal("-input must be valid JSON")
	}

	payload := invokeRequest{
		Tool:           *tool,
		Input:          json.RawMessage(*input),
		RequestID:      *requestID,
		IdempotencyKey: *idempotencyKey,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("marshal request: %v", err)
	}

	endpoint := strings.TrimRight(*baseURL, "/") + "/mcp/" + strings.Trim(*server, "/")
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("invoke request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("read response: %v", err)
	}

	if *pretty && json.Valid(respBody) {
		var out bytes.Buffer
		if err := json.Indent(&out, respBody, "", "  "); err == nil {
			_, _ = out.WriteTo(os.Stdout)
			_, _ = os.Stdout.Write([]byte("\n"))
			os.Exit(exitCode(resp.StatusCode))
		}
	}

	fmt.Println(string(respBody))
	os.Exit(exitCode(resp.StatusCode))
}

func exitCode(statusCode int) int {
	if statusCode >= 200 && statusCode < 300 {
		return 0
	}
	return 1
}
