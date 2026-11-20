package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
)

// Manager keeps one lazy connection per configured server, caches tool
// lists, and routes namespaced tool calls (mcp__<server>__<tool>).
type Manager struct {
	st    *store.Store
	mu    sync.Mutex
	conns map[string]*conn
	names map[string]route // namespaced tool name -> server/tool
}

type conn struct {
	key     string // config fingerprint; a change forces a reconnect
	client  *Client
	tools   []Tool
	err     string
	checked time.Time
}

type route struct{ serverID, tool string }

// Status is what the UI shows for a server.
type Status struct {
	ID        string    `json:"id"`
	Connected bool      `json:"connected"`
	Server    string    `json:"server,omitempty"`
	Error     string    `json:"error,omitempty"`
	Tools     []Tool    `json:"tools"`
	Checked   time.Time `json:"checked"`
}

// Bound is a tool exposed to an agent.
type Bound struct {
	Name     string // namespaced
	Server   store.MCPServer
	Tool     Tool
	Approval bool
}

func NewManager(st *store.Store) *Manager {
	return &Manager{st: st, conns: map[string]*conn{}, names: map[string]route{}}
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(s, "_"), "_")
	if s == "" {
		s = "x"
	}
	return s
}

// ToolName builds the namespaced tool name the model sees (max 64 chars).
func ToolName(server, tool string) string {
	n := "mcp__" + slug(server) + "__" + slug(tool)
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

func fingerprint(s store.MCPServer) string {
	s.Roles, s.Tools, s.Approval, s.Enabled, s.Name = nil, nil, false, false, ""
	b, _ := json.Marshal(s)
	return string(b)
}

func (m *Manager) server(id string) (store.MCPServer, bool) {
	for _, s := range m.st.Config().MCP {
		if s.ID == id {
			return *s, true
		}
	}
	return store.MCPServer{}, false
}

// get returns a connected client with a fresh-enough tool list.
func (m *Manager) get(ctx context.Context, srv store.MCPServer, force bool) (*conn, error) {
	key := fingerprint(srv)
	m.mu.Lock()
	c := m.conns[srv.ID]
	if c != nil && !force && c.key == key && c.client != nil {
		m.mu.Unlock()
		return c, nil
	}
	// back off after a recent failure instead of reconnecting on every prompt
	if c != nil && !force && c.key == key && c.client == nil && time.Since(c.checked) < 30*time.Second {
		m.mu.Unlock()
		return nil, errors.New(c.err)
	}
	if c != nil && c.client != nil {
		_ = c.client.Close()
	}
	delete(m.conns, srv.ID)
	m.mu.Unlock()

	nc := &conn{key: key, checked: time.Now().UTC()}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := Connect(cctx, srv)
	if err == nil {
		nc.tools, err = client.ListTools(cctx)
		if err != nil {
			_ = client.Close()
		} else {
			nc.client = client
		}
	}
	if err != nil {
		nc.err = err.Error()
		log.Printf("mcp %s: %v", srv.Name, err)
	} else {
		log.Printf("mcp %s: connected (%s, %d tools)", srv.Name, client.Server.Name, len(nc.tools))
	}
	m.mu.Lock()
	m.conns[srv.ID] = nc
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return nc, nil
}

// Refresh reconnects a server and returns its status (used by Test / Reconnect).
func (m *Manager) Refresh(ctx context.Context, srv store.MCPServer) Status {
	c, err := m.get(ctx, srv, true)
	st := Status{ID: srv.ID, Tools: []Tool{}, Checked: time.Now().UTC()}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Connected, st.Tools, st.Server = true, c.tools, c.client.Server.Name+" "+c.client.Server.Version
	return st
}

// Status reports the cached state of every server without connecting.
func (m *Manager) Status() map[string]Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Status{}
	for id, c := range m.conns {
		st := Status{ID: id, Connected: c.client != nil, Error: c.err, Tools: c.tools, Checked: c.checked}
		if st.Tools == nil {
			st.Tools = []Tool{}
		}
		if c.client != nil {
			st.Server = strings.TrimSpace(c.client.Server.Name + " " + c.client.Server.Version)
		}
		out[id] = st
	}
	return out
}

// ToolsFor lists the MCP tools an agent role may use. Unreachable servers
// are skipped (logged) so one broken server never blocks a task.
func (m *Manager) ToolsFor(ctx context.Context, role string) []Bound {
	var out []Bound
	for _, s := range m.st.Config().MCP {
		if !s.Enabled || !contains(s.Roles, role) {
			continue
		}
		c, err := m.get(ctx, *s, false)
		if err != nil {
			continue
		}
		for _, t := range c.tools {
			if len(s.Tools) > 0 && !contains(s.Tools, t.Name) {
				continue
			}
			name := ToolName(s.Name, t.Name)
			m.mu.Lock()
			m.names[name] = route{s.ID, t.Name}
			m.mu.Unlock()
			out = append(out, Bound{Name: name, Server: *s, Tool: t, Approval: s.Approval})
		}
	}
	return out
}

// Lookup resolves a namespaced tool name.
func (m *Manager) Lookup(name string) (store.MCPServer, string, bool) {
	m.mu.Lock()
	r, ok := m.names[name]
	m.mu.Unlock()
	if !ok {
		return store.MCPServer{}, "", false
	}
	srv, ok := m.server(r.serverID)
	return srv, r.tool, ok
}

// Call runs a namespaced tool; a broken connection is re-established once.
func (m *Manager) Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	srv, tool, ok := m.Lookup(name)
	if !ok {
		return "", false, fmt.Errorf("unknown MCP tool %q", name)
	}
	if !srv.Enabled {
		return "", false, fmt.Errorf("MCP server %s is disabled", srv.Name)
	}
	for attempt := 0; attempt < 2; attempt++ {
		c, err := m.get(ctx, srv, attempt > 0)
		if err != nil {
			return "", false, err
		}
		cctx, cancel := context.WithTimeout(ctx, timeout(srv))
		text, isErr, err := c.client.CallTool(cctx, tool, args)
		cancel()
		if err == nil {
			return text, isErr, nil
		}
		if ctx.Err() != nil || strings.HasPrefix(err.Error(), "MCP error") {
			return "", false, err
		}
		log.Printf("mcp %s: call %s failed (%v), reconnecting", srv.Name, tool, err)
	}
	return "", false, errors.New("MCP server unreachable")
}

// Close shuts every connection (stdio processes included).
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.conns {
		if c.client != nil {
			_ = c.client.Close()
		}
		delete(m.conns, id)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
