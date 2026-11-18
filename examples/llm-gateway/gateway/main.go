// Command gateway is a small AI gateway with Atryum (or Agent Authority) in
// the path of the agents' model traffic. It is a reference for wiring a real
// gateway, and the gateway the walkthrough in ../README.md runs.
//
// For every model request it does four things, in order:
//
//  1. Shows Atryum the request (POST /api/v1/gateway/llm/input) and sends
//     the model whatever comes back.
//  2. Calls the model and waits for the whole response. A client that asked
//     for a stream still gets one, written from the finished response: tool
//     calls cannot be judged in pieces.
//  3. Shows Atryum the response (POST /api/v1/gateway/llm/output) and takes
//     back the response the agent may see.
//  4. Sends that to the agent.
//
// If Atryum cannot be reached, or answers without a body to send on, the
// agent gets an error: nothing passes unchecked.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:4100", "address agents send model requests to")
	upstream := flag.String("upstream", "https://api.anthropic.com", "the model provider (or ../mockmodel)")
	aa := flag.String("aa", "http://127.0.0.1:8080", "Atryum or Agent Authority")
	flag.Parse()
	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatal(err)
	}
	key := os.Getenv("AA_GATEWAY_KEY")
	if key == "" {
		log.Fatal("set AA_GATEWAY_KEY to an agent key with the asserter capability (a gateway's key)")
	}
	g := &gateway{
		upstream: target, aa: strings.TrimRight(*aa, "/"), key: key,
		passThrough: httputil.NewSingleHostReverseProxy(target),
		models:      &http.Client{Timeout: 10 * time.Minute},
		checks:      &http.Client{Timeout: 8 * time.Second},
	}
	log.Printf("AI gateway on http://%s -> model at %s, checked by %s", *listen, target, g.aa)
	log.Fatal(http.ListenAndServe(*listen, g))
}

type gateway struct {
	upstream    *url.URL
	aa, key     string
	passThrough http.Handler
	models      *http.Client
	checks      *http.Client
}

// formats maps the model APIs this gateway checks to Atryum's name for each.
var formats = map[string]string{
	"/v1/messages":         "anthropic",
	"/v1/chat/completions": "openai_chat",
	"/v1/responses":        "openai_responses",
}

// passThrough lists the only other requests sent on to the model provider:
// ones that cannot carry a tool call. Anything else is refused, because a
// path this gateway does not check could return one unchecked (fetching a
// background response with GET /v1/responses/{id}, or batch results).
var passThrough = map[string]bool{
	"GET /v1/models":                 true,
	"POST /v1/messages/count_tokens": true,
}

// who is sent with every check.
//
// WALKTHROUGH ONLY: this gateway reads who the call is for from headers the
// agent sets, so the walkthrough can name Emma. The agent can write any name
// there. A real gateway takes the person, agent and session from its own
// sign-in (the caller's verified key or token), never from the request.
type who struct {
	User      *principal `json:"user,omitempty"`
	Agent     *principal `json:"agent,omitempty"`
	SessionID string     `json:"session_id,omitempty"`
}

type principal struct {
	ID      string `json:"id"`
	Display string `json:"display,omitempty"`
}

func identity(r *http.Request) who {
	w := who{SessionID: r.Header.Get("X-Gateway-Session")}
	if u := r.Header.Get("X-Gateway-User"); u != "" {
		w.User = &principal{ID: u, Display: r.Header.Get("X-Gateway-User-Name")}
	}
	if a := r.Header.Get("X-Gateway-Agent"); a != "" {
		w.Agent = &principal{ID: a}
	}
	return w
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	format := formats[r.URL.Path]
	if format == "" || r.Method != http.MethodPost {
		if !passThrough[r.Method+" "+r.URL.Path] && !(r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/models/")) {
			http.Error(w, "this gateway does not pass "+r.Method+" "+r.URL.Path+" on: Agent Authority could not check the tool calls in its answer", http.StatusNotFound)
			return
		}
		g.passThrough.ServeHTTP(w, r)
		return
	}
	request, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		refuse(w, format, http.StatusBadRequest, "could not read the request")
		return
	}
	id := identity(r)

	// 1. The request, before the model sees it.
	var in struct {
		Request  json.RawMessage `json:"request"`
		Reason   string          `json:"reason"`
		Changed  bool            `json:"changed"`
		Outcomes []struct {
			CallID string `json:"call_id"`
			Status string `json:"status"`
		} `json:"outcomes"`
	}
	if err := g.ask("llm/input", map[string]any{"format": format, "request": json.RawMessage(request), "user": id.User, "agent": id.Agent, "session_id": id.SessionID}, &in); err != nil {
		refuse(w, format, http.StatusServiceUnavailable, "Agent Authority unavailable: "+err.Error())
		return
	}
	if in.Request == nil {
		refuse(w, format, http.StatusForbidden, in.Reason)
		return
	}
	for _, o := range in.Outcomes {
		log.Printf("  llm/input   result of %s -> %s", o.CallID, o.Status)
	}
	request = in.Request

	// 2. The model, for the whole response.
	var fields map[string]any
	_ = json.Unmarshal(request, &fields)
	wantStream, _ := fields["stream"].(bool)
	toModel := request
	if wantStream {
		if format == "openai_responses" {
			refuse(w, format, http.StatusBadRequest, "this reference gateway does not stream the Responses API; send the request without \"stream\": true")
			return
		}
		toModel = withoutStream(request)
	}
	status, header, response, err := g.callModel(r, toModel)
	if err != nil {
		refuse(w, format, http.StatusBadGateway, "the model could not be reached: "+err.Error())
		return
	}
	if status != http.StatusOK { // the provider's own error: nothing to check
		copyHeader(w.Header(), header)
		w.WriteHeader(status)
		_, _ = w.Write(response)
		return
	}

	// 3. The response, before the agent sees it.
	var out struct {
		Response json.RawMessage `json:"response"`
		Reason   string          `json:"reason"`
		Changed  bool            `json:"changed"`
		Calls    []struct {
			Tool    string `json:"tool"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"calls"`
	}
	if err := g.ask("llm/output", map[string]any{"format": format, "response": json.RawMessage(response), "request": json.RawMessage(request), "user": id.User, "agent": id.Agent, "session_id": id.SessionID}, &out); err != nil {
		refuse(w, format, http.StatusServiceUnavailable, "Agent Authority unavailable: "+err.Error())
		return
	}
	if out.Response == nil {
		refuse(w, format, http.StatusForbidden, out.Reason)
		return
	}
	for _, c := range out.Calls {
		log.Printf("  llm/output  %-22s -> %s", c.Tool, c.Verdict)
	}
	if out.Changed {
		log.Printf("  llm/output  response adjusted: the agent will not see the calls that may not run")
	}

	// 4. The agent.
	if wantStream {
		stream(w, format, out.Response, fields)
		return
	}
	copyHeader(w.Header(), header)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out.Response)
}

// ask posts one check to Atryum. Any failure is an error: the caller refuses.
func (g *gateway) ask(hook string, body any, into any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, g.aa+"/api/v1/gateway/"+hook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.checks.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d: %s", hook, resp.StatusCode, strings.TrimSpace(string(answer)))
	}
	return json.Unmarshal(answer, into)
}

func (g *gateway) callModel(r *http.Request, body []byte) (int, http.Header, []byte, error) {
	target := *g.upstream
	target.Path = strings.TrimRight(target.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	copyHeader(req.Header, r.Header)
	req.Header.Set("Accept-Encoding", "identity") // the body is read here, so ask for it plain
	resp, err := g.models.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, answer, err
}

// copyHeader copies end-to-end headers. Lengths and encodings describe a
// body this gateway rewrites, and the X-Gateway-* headers are its own.
func copyHeader(dst, src http.Header) {
	for k, values := range src {
		switch lk := strings.ToLower(k); {
		case lk == "content-length", lk == "content-encoding", lk == "accept-encoding", lk == "connection", lk == "transfer-encoding", strings.HasPrefix(lk, "x-gateway-"):
			continue
		}
		dst[k] = append([]string(nil), values...)
	}
}

func withoutStream(request []byte) []byte {
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(request))
	dec.UseNumber() // keep numbers exactly as the agent wrote them
	if dec.Decode(&fields) != nil {
		return request
	}
	fields["stream"] = false
	delete(fields, "stream_options")
	out, err := json.Marshal(fields)
	if err != nil {
		return request
	}
	return out
}

// refuse answers the agent in the provider's own error shape, so its SDK
// reports the reason instead of a parse failure.
func refuse(w http.ResponseWriter, format string, status int, reason string) {
	if reason == "" {
		reason = "Agent Authority refused this request."
	}
	log.Printf("  refused (%d): %s", status, reason)
	var body any = map[string]any{"error": map[string]any{"message": reason, "type": "permission_error", "code": "agent_authority"}}
	if format == "anthropic" {
		body = map[string]any{"type": "error", "error": map[string]any{"type": "permission_error", "message": reason}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
