package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/khangphantz/aatmt/internal/store"
)

// handle implements a tiny MCP server: initialize, tools/list, tools/call.
func handle(m map[string]any) map[string]any {
	id, hasID := m["id"]
	if !hasID {
		return nil // notification
	}
	res := map[string]any{"jsonrpc": "2.0", "id": id}
	switch m["method"] {
	case "initialize":
		res["result"] = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "fake", "version": "1.0"}}
	case "tools/list":
		res["result"] = map[string]any{"tools": []any{
			map[string]any{"name": "echo", "description": "Echo text", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
			map[string]any{"name": "fail", "description": "Always fails", "inputSchema": map[string]any{"type": "object"}},
		}}
	case "tools/call":
		p := m["params"].(map[string]any)
		args, _ := p["arguments"].(map[string]any)
		if p["name"] == "fail" {
			res["result"] = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "boom"}}}
		} else {
			res["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprint("echo: ", args["text"])}}}
		}
	default:
		res["error"] = map[string]any{"code": -32601, "message": "method not found"}
	}
	return res
}

// TestMain doubles as a stdio MCP server when AATMT_FAKE_MCP=1.
func TestMain(m *testing.M) {
	if os.Getenv("AATMT_FAKE_MCP") == "1" {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			var msg map[string]any
			if json.Unmarshal(sc.Bytes(), &msg) != nil {
				continue
			}
			if out := handle(msg); out != nil {
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func exercise(t *testing.T, cfg store.MCPServer) {
	t.Helper()
	ctx := context.Background()
	c, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Server.Name != "fake" {
		t.Fatalf("server info %+v", c.Server)
	}
	tools, err := c.ListTools(ctx)
	if err != nil || len(tools) != 2 || tools[0].Name != "echo" {
		t.Fatalf("tools %v %v", tools, err)
	}
	out, isErr, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || isErr || out != "echo: hi" {
		t.Fatalf("call: %q %v %v", out, isErr, err)
	}
	out, isErr, _ = c.CallTool(ctx, "fail", nil)
	if !isErr || out != "boom" {
		t.Fatalf("isError not propagated: %q %v", out, isErr)
	}
}

func TestStreamableHTTP(t *testing.T) {
	var sawSession bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(200)
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(401)
			return
		}
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		if m["method"] == "initialize" {
			w.Header().Set("Mcp-Session-Id", "s1")
		} else if r.Header.Get("Mcp-Session-Id") == "s1" {
			sawSession = true
		}
		out := handle(m)
		if out == nil {
			w.WriteHeader(202)
			return
		}
		ob, _ := json.Marshal(out)
		if m["method"] == "tools/call" { // answer tool calls as an SSE stream
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", ob)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ob)
	}))
	defer srv.Close()
	t.Setenv("MCP_TOKEN", "Bearer secret")
	exercise(t, store.MCPServer{Transport: "http", URL: srv.URL, Headers: map[string]string{"Authorization": "env:MCP_TOKEN"}})
	if !sawSession {
		t.Fatal("session id was not sent back")
	}
}

func TestLegacySSE(t *testing.T) {
	var mu sync.Mutex
	var stream chan string = make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: endpoint\ndata: /message?sessionId=abc\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case msg := <-stream:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case r.Method == http.MethodPost && r.URL.Path == "/message" && r.URL.Query().Get("sessionId") == "abc":
			var m map[string]any
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &m)
			if out := handle(m); out != nil {
				ob, _ := json.Marshal(out)
				mu.Lock()
				stream <- string(ob)
				mu.Unlock()
			}
			w.WriteHeader(202)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	exercise(t, store.MCPServer{Transport: "sse", URL: srv.URL + "/sse"})
}

func TestStdio(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	exercise(t, store.MCPServer{Transport: "stdio", Command: exe, Args: []string{"-test.run=^$"}, Env: map[string]string{"AATMT_FAKE_MCP": "1"}})
}

func TestManagerRoutingAndAllowlist(t *testing.T) {
	exe, _ := os.Executable()
	st, _ := store.Open(t.TempDir())
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.MCP = []*store.MCPServer{{ID: "m1", Name: "my tools", Transport: "stdio", Command: exe, Args: []string{"-test.run=^$"},
			Env: map[string]string{"AATMT_FAKE_MCP": "1"}, Roles: []string{"worker"}, Tools: []string{"echo"}, Enabled: true}}
		return nil
	})
	m := NewManager(st)
	defer m.Close()
	if got := m.ToolsFor(context.Background(), "chat"); len(got) != 0 {
		t.Fatalf("chat role should get no tools, got %d", len(got))
	}
	tools := m.ToolsFor(context.Background(), "worker")
	if len(tools) != 1 || tools[0].Name != "mcp__my_tools__echo" {
		t.Fatalf("allowlist / namespacing: %+v", tools)
	}
	out, _, err := m.Call(context.Background(), "mcp__my_tools__echo", json.RawMessage(`{"text":"x"}`))
	if err != nil || out != "echo: x" {
		t.Fatalf("routed call: %q %v", out, err)
	}
	if st := m.Status()["m1"]; !st.Connected || len(st.Tools) != 2 || !strings.Contains(st.Server, "fake") {
		t.Fatalf("status %+v", st)
	}
}
