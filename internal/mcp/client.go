package mcp

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"atryum/internal/config"
)

type UpstreamMode string

const (
	UpstreamModeHTTP  UpstreamMode = "http"
	UpstreamModeStdio UpstreamMode = "stdio"
)

type Upstream struct {
	Name      string
	Mode      UpstreamMode
	BaseURL   string
	AuthToken string
	Timeout   time.Duration
	Command   string
	Args      []string
	Env       map[string]string
	Enabled   bool
}

type ServerStore interface {
	GetServer(ctx context.Context, name string) (Upstream, error)
	ListServers(ctx context.Context, filter ServerFilter) ([]Upstream, int, error)
	CountServers(ctx context.Context) (int, error)
	CreateServer(ctx context.Context, upstream Upstream) error
}

type ServerFilter struct {
	Offset  uint64
	Limit   uint64
	Enabled *bool
}

type Resolver struct {
	store     ServerStore
	bootstrap map[string]Upstream
}

type Client struct {
	httpClient *http.Client
	nextID     atomic.Int64
}

type InvokeResult struct {
	StatusCode int
	Body       []byte
	Failed     bool
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func NewResolver(store ServerStore, cfg config.Config) *Resolver {
	bootstrap := make(map[string]Upstream)
	for _, u := range cfg.Upstreams {
		upstream := fromConfig(u)
		if !upstream.Enabled {
			continue
		}
		bootstrap[upstream.Name] = upstream
	}
	return &Resolver{store: store, bootstrap: bootstrap}
}

func NewHTTPClient() *Client { return &Client{httpClient: &http.Client{}} }

func (r *Resolver) Resolve(name string) (Upstream, error) {
	return r.ResolveContext(context.Background(), name)
}

func (r *Resolver) ResolveContext(ctx context.Context, name string) (Upstream, error) {
	if r.store != nil {
		upstream, err := r.store.GetServer(ctx, name)
		if err == nil {
			return upstream, nil
		}
		if err != nil && err != sql.ErrNoRows {
			return Upstream{}, err
		}
	}
	upstream, ok := r.bootstrap[name]
	if !ok || !upstream.Enabled {
		return Upstream{}, fmt.Errorf("upstream %q not configured or disabled", name)
	}
	return upstream, nil
}

func (r *Resolver) BootstrapIfEmpty(ctx context.Context) error {
	if r.store == nil {
		return nil
	}
	count, err := r.store.CountServers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, upstream := range r.bootstrap {
		if err := r.store.CreateServer(ctx, upstream); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Invoke(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string) (InvokeResult, error) {
	switch upstream.Mode {
	case UpstreamModeStdio:
		return c.invokeStdio(ctx, upstream, tool, input)
	case UpstreamModeHTTP, "":
		return c.invokeHTTP(ctx, upstream, tool, input, requestID)
	default:
		return InvokeResult{}, fmt.Errorf("unsupported upstream mode %q", upstream.Mode)
	}
}

func (c *Client) invokeHTTP(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string) (InvokeResult, error) {
	payload := map[string]any{"tool": tool, "input": input}
	if requestID != nil {
		payload["request_id"] = *requestID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return InvokeResult{}, err
	}
	endpoint := upstream.BaseURL + "/mcp/tools/call"
	client := c.httpClient
	if upstream.Timeout > 0 {
		client = &http.Client{Timeout: upstream.Timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return InvokeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if upstream.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+upstream.AuthToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return InvokeResult{}, err
	}
	defer resp.Body.Close()
	respBody := new(bytes.Buffer)
	_, err = respBody.ReadFrom(resp.Body)
	if err != nil {
		return InvokeResult{}, err
	}
	bodyBytes := respBody.Bytes()
	failed := resp.StatusCode >= http.StatusBadRequest || looksLikeToolError(bodyBytes)
	return InvokeResult{StatusCode: resp.StatusCode, Body: bodyBytes, Failed: failed}, nil
}

func (c *Client) invokeStdio(ctx context.Context, upstream Upstream, tool string, input map[string]any) (InvokeResult, error) {
	if upstream.Command == "" {
		return InvokeResult{}, fmt.Errorf("stdio upstream %q missing command", upstream.Name)
	}
	cmd := exec.CommandContext(ctx, upstream.Command, upstream.Args...)
	cmd.Env = os.Environ()
	for k, v := range upstream.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return InvokeResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return InvokeResult{}, err
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return InvokeResult{}, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)
	if err := writeRPC(stdin, c.nextRPCID(), "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]any{"name": "atryum", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	}); err != nil {
		return InvokeResult{}, err
	}
	if _, err := readRPC(reader); err != nil {
		return InvokeResult{}, err
	}
	_ = writeRPC(stdin, c.nextRPCID(), "notifications/initialized", map[string]any{})
	if err := writeRPC(stdin, c.nextRPCID(), "tools/call", map[string]any{
		"name":      tool,
		"arguments": input,
	}); err != nil {
		return InvokeResult{}, err
	}
	resp, err := readRPC(reader)
	if err != nil {
		if stderr.Len() > 0 {
			return InvokeResult{}, fmt.Errorf("stdio upstream error: %s", strings.TrimSpace(stderr.String()))
		}
		return InvokeResult{}, err
	}
	if len(resp.Error) > 0 && string(resp.Error) != "null" {
		return InvokeResult{StatusCode: http.StatusBadGateway, Body: resp.Error, Failed: true}, nil
	}
	body := resp.Result
	if len(body) == 0 {
		body = []byte(`{"ok":true}`)
	}
	return InvokeResult{StatusCode: http.StatusOK, Body: body, Failed: looksLikeToolError(body)}, nil
}

func FromConfig(cfg config.UpstreamConfig) Upstream {
	return fromConfig(cfg)
}

func fromConfig(u config.UpstreamConfig) Upstream {
	mode := UpstreamModeHTTP
	if u.Mode != "" {
		mode = UpstreamMode(u.Mode)
	}
	return Upstream{
		Name:      u.Name,
		Mode:      mode,
		BaseURL:   strings.TrimRight(u.BaseURL, "/"),
		AuthToken: u.AuthToken,
		Timeout:   time.Duration(u.TimeoutSeconds) * time.Second,
		Command:   u.Command,
		Args:      append([]string(nil), u.Args...),
		Env:       cloneMap(u.Env),
		Enabled:   u.Enabled,
	}
}

func writeRPC(w interface{ Write([]byte) (int, error) }, id int64, method string, params map[string]any) error {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if strings.HasPrefix(method, "notifications/") {
		delete(msg, "id")
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func readRPC(reader *bufio.Reader) (rpcResponse, error) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return rpcResponse{}, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID == 0 && len(resp.Result) == 0 && len(resp.Error) == 0 {
			continue
		}
		return resp, nil
	}
}

func looksLikeToolError(body []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	if isErr, ok := payload["isError"].(bool); ok && isErr {
		return true
	}
	if content, ok := payload["content"].([]any); ok {
		for _, item := range content {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if isErr, ok := obj["isError"].(bool); ok && isErr {
				return true
			}
			if text, ok := obj["text"].(string); ok && strings.Contains(strings.ToLower(text), "not found") {
				return true
			}
		}
	}
	return false
}

func (c *Client) nextRPCID() int64 {
	return c.nextID.Add(1)
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
