// Package mcp is a small Model Context Protocol client (JSON-RPC 2.0) with
// three transports: Streamable HTTP, legacy HTTP+SSE and stdio. It lets the
// agents use tools from any MCP server (issue trackers, docs, databases,
// browsers, internal company services…).
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
	"github.com/khangphantz/aatmt/internal/version"
)

const protocolVersion = "2025-03-26"

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// transport sends a request and returns the matching response.
type transport interface {
	request(ctx context.Context, id int64, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	close() error
}

// Client is one live connection to an MCP server.
type Client struct {
	cfg    store.MCPServer
	t      transport
	nextID atomic.Int64
	Server struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
}

func resolve(v string) string { return store.ResolveKey(v) }

// Connect opens the transport and performs the MCP initialize handshake.
func Connect(ctx context.Context, cfg store.MCPServer) (*Client, error) {
	c := &Client{cfg: cfg}
	var err error
	switch cfg.Transport {
	case "stdio":
		c.t, err = newStdio(cfg)
	case "sse":
		c.t, err = newSSE(ctx, cfg)
	default:
		c.t, err = newHTTP(cfg)
	}
	if err != nil {
		return nil, err
	}
	var res struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	raw, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "aatmt", "version": version.Version},
	})
	if err != nil {
		_ = c.t.close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	_ = json.Unmarshal(raw, &res)
	c.Server.Name, c.Server.Version = res.ServerInfo.Name, res.ServerInfo.Version
	_ = c.t.notify(ctx, "notifications/initialized", nil)
	return c, nil
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.t.request(ctx, c.nextID.Add(1), method, params)
}

// ListTools returns every tool the server offers (follows pagination).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return all, nil
}

// CallTool runs a tool and flattens its content to text.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	var a any = map[string]any{}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": a})
	if err != nil {
		return "", false, err
	}
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, p := range res.Content {
		switch p.Type {
		case "text":
			b.WriteString(p.Text)
		case "resource":
			fmt.Fprintf(&b, "[resource %s]\n%s", p.Resource.URI, p.Resource.Text)
		default:
			fmt.Fprintf(&b, "[%s content omitted (%s)]", p.Type, p.MimeType)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 && len(res.StructuredContent) > 0 {
		b.Write(res.StructuredContent)
	}
	return strings.TrimSpace(b.String()), res.IsError, nil
}

func (c *Client) Close() error { return c.t.close() }

func decodeResult(m rpcMsg) (json.RawMessage, error) {
	if m.Error != nil {
		return nil, fmt.Errorf("MCP error %d: %s", m.Error.Code, m.Error.Message)
	}
	return m.Result, nil
}

func timeout(cfg store.MCPServer) time.Duration {
	if cfg.TimeoutSec > 0 {
		return time.Duration(cfg.TimeoutSec) * time.Second
	}
	return time.Duration(store.DefaultLimits().MCPCallTimeoutSec) * time.Second
}

// ---------------------------------------------------------------- Streamable HTTP

type httpT struct {
	cfg     store.MCPServer
	client  *http.Client
	mu      sync.Mutex
	session string
}

func newHTTP(cfg store.MCPServer) (*httpT, error) {
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return nil, errors.New("MCP URL must start with http(s)://")
	}
	return &httpT{cfg: cfg, client: &http.Client{Timeout: timeout(cfg), Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}}, nil
}

func (h *httpT) post(ctx context.Context, msg rpcMsg) (*http.Response, error) {
	b, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	for k, v := range h.cfg.Headers {
		req.Header.Set(k, resolve(v))
	}
	h.mu.Lock()
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	h.mu.Unlock()
	res, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		h.mu.Lock()
		h.session = sid
		h.mu.Unlock()
	}
	if res.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		res.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return res, nil
}

func (h *httpT) request(ctx context.Context, id int64, method string, params any) (json.RawMessage, error) {
	res, err := h.post(ctx, rpcMsg{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		var out json.RawMessage
		var outErr error
		found := false
		err := readSSE(res.Body, func(event, data string) bool {
			var m rpcMsg
			if json.Unmarshal([]byte(data), &m) == nil && m.ID != nil && *m.ID == id && m.Method == "" {
				out, outErr = decodeResult(m)
				found = true
				return false
			}
			return true
		})
		if !found {
			if err == nil {
				err = errors.New("stream ended without a response")
			}
			return nil, err
		}
		return out, outErr
	}
	var m rpcMsg
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return decodeResult(m)
}

func (h *httpT) notify(ctx context.Context, method string, params any) error {
	res, err := h.post(ctx, rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

func (h *httpT) close() error {
	h.mu.Lock()
	sid := h.session
	h.mu.Unlock()
	if sid == "" {
		return nil
	}
	req, _ := http.NewRequest(http.MethodDelete, h.cfg.URL, nil)
	req.Header.Set("Mcp-Session-Id", sid)
	if res, err := h.client.Do(req); err == nil {
		res.Body.Close()
	}
	return nil
}

// readSSE parses a text/event-stream; fn returns false to stop.
func readSSE(r io.Reader, fn func(event, data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 32<<20)
	event, data := "", []string{}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				if !fn(event, strings.Join(data, "\n")) {
					return nil
				}
			}
			event, data = "", data[:0]
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	return sc.Err()
}

// ---------------------------------------------------------------- pending map (stdio & legacy SSE)

type pending struct {
	mu   sync.Mutex
	wait map[int64]chan rpcMsg
	dead error
}

func newPending() *pending { return &pending{wait: map[int64]chan rpcMsg{}} }

func (p *pending) add(id int64) (chan rpcMsg, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead != nil {
		return nil, p.dead
	}
	ch := make(chan rpcMsg, 1)
	p.wait[id] = ch
	return ch, nil
}

func (p *pending) resolve(m rpcMsg) {
	if m.ID == nil || m.Method != "" {
		return // notifications / server requests are ignored
	}
	p.mu.Lock()
	ch := p.wait[*m.ID]
	delete(p.wait, *m.ID)
	p.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (p *pending) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead = err
	for id, ch := range p.wait {
		close(ch)
		delete(p.wait, id)
	}
}

func (p *pending) await(ctx context.Context, id int64, ch chan rpcMsg, d time.Duration) (json.RawMessage, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case m, ok := <-ch:
		if !ok {
			p.mu.Lock()
			err := p.dead
			p.mu.Unlock()
			if err == nil {
				err = errors.New("connection closed")
			}
			return nil, err
		}
		return decodeResult(m)
	case <-t.C:
		p.mu.Lock()
		delete(p.wait, id)
		p.mu.Unlock()
		return nil, fmt.Errorf("MCP request timed out after %s", d)
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.wait, id)
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

// ---------------------------------------------------------------- stdio

type stdioT struct {
	cfg    store.MCPServer
	cmd    *exec.Cmd
	in     io.WriteCloser
	wmu    sync.Mutex
	p      *pending
	exited chan struct{}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() > 8000 {
		l.b.Reset()
	}
	return l.b.Write(p)
}

func newStdio(cfg store.MCPServer) (*stdioT, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("stdio MCP server needs a command")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+resolve(v))
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &lockedBuf{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
	}
	s := &stdioT{cfg: cfg, cmd: cmd, in: in, p: newPending(), exited: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 32<<20)
		for sc.Scan() {
			var m rpcMsg
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				s.p.resolve(m)
			}
		}
		_ = cmd.Wait()
		close(s.exited)
		stderr.mu.Lock()
		tail := strings.TrimSpace(stderr.b.String())
		stderr.mu.Unlock()
		if len(tail) > 500 {
			tail = tail[len(tail)-500:]
		}
		msg := "MCP process exited"
		if tail != "" {
			msg += ": " + tail
		}
		s.p.fail(errors.New(msg))
	}()
	return s, nil
}

func (s *stdioT) write(m rpcMsg) error {
	b, _ := json.Marshal(m)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.in.Write(append(b, '\n'))
	return err
}

func (s *stdioT) request(ctx context.Context, id int64, method string, params any) (json.RawMessage, error) {
	ch, err := s.p.add(id)
	if err != nil {
		return nil, err
	}
	if err := s.write(rpcMsg{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	return s.p.await(ctx, id, ch, timeout(s.cfg))
}

func (s *stdioT) notify(ctx context.Context, method string, params any) error {
	return s.write(rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *stdioT) close() error {
	_ = s.in.Close() // a well-behaved server exits on EOF
	select {
	case <-s.exited:
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
	}
	return nil
}

// ---------------------------------------------------------------- legacy HTTP+SSE

type sseT struct {
	cfg      store.MCPServer
	client   *http.Client
	endpoint string
	cancel   context.CancelFunc
	p        *pending
}

func newSSE(ctx context.Context, cfg store.MCPServer) (*sseT, error) {
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return nil, errors.New("MCP URL must start with http(s)://")
	}
	sctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(sctx, http.MethodGet, cfg.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range cfg.Headers {
		req.Header.Set(k, resolve(v))
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	res, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		res.Body.Close()
		cancel()
		return nil, fmt.Errorf("SSE HTTP %d", res.StatusCode)
	}
	s := &sseT{cfg: cfg, client: &http.Client{Timeout: timeout(cfg), Transport: client.Transport}, cancel: cancel, p: newPending()}
	endpoint := make(chan string, 1)
	go func() {
		defer res.Body.Close()
		err := readSSE(res.Body, func(event, data string) bool {
			if event == "endpoint" {
				select {
				case endpoint <- data:
				default:
				}
				return true
			}
			var m rpcMsg
			if json.Unmarshal([]byte(data), &m) == nil {
				s.p.resolve(m)
			}
			return true
		})
		if err == nil {
			err = errors.New("SSE stream closed")
		}
		s.p.fail(err)
	}()
	select {
	case ep := <-endpoint:
		base, _ := url.Parse(cfg.URL)
		ref, err := url.Parse(strings.TrimSpace(ep))
		if err != nil {
			cancel()
			return nil, err
		}
		s.endpoint = base.ResolveReference(ref).String()
	case <-time.After(timeout(cfg)):
		cancel()
		return nil, errors.New("SSE server did not send an endpoint event")
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	return s, nil
}

func (s *sseT) post(ctx context.Context, m rpcMsg) error {
	b, _ := json.Marshal(m)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, resolve(v))
	}
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

func (s *sseT) request(ctx context.Context, id int64, method string, params any) (json.RawMessage, error) {
	ch, err := s.p.add(id)
	if err != nil {
		return nil, err
	}
	if err := s.post(ctx, rpcMsg{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	return s.p.await(ctx, id, ch, timeout(s.cfg))
}

func (s *sseT) notify(ctx context.Context, method string, params any) error {
	return s.post(ctx, rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *sseT) close() error { s.cancel(); return nil }
