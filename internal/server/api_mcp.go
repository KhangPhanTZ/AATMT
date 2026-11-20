package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/mcp"
	"github.com/khangphantz/aatmt/internal/store"
)

// maskMap hides literal secret values in headers/env (env:NAME references stay visible).
func maskMap(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if strings.HasPrefix(v, "env:") {
			out[k] = v
		} else {
			out[k] = keyHint(v)
		}
	}
	return out
}

// unmask restores stored values the UI sent back as their masked hint.
func unmask(in, old map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if ov, ok := old[k]; ok && v == keyHint(ov) && !strings.HasPrefix(ov, "env:") {
			v = ov
		}
		out[k] = v
	}
	return out
}

func (s *Server) mcpOut(w http.ResponseWriter) {
	cfg := s.Store.Config()
	status := s.Engine.MCP.Status()
	type row struct {
		store.MCPServer
		Status *mcp.Status `json:"status,omitempty"`
	}
	out := []row{}
	for _, m := range cfg.MCP {
		r := row{MCPServer: *m}
		r.Headers, r.Env = maskMap(m.Headers), maskMap(m.Env)
		if r.Roles == nil {
			r.Roles = []string{}
		}
		if r.Tools == nil {
			r.Tools = []string{}
		}
		if r.Args == nil {
			r.Args = []string{}
		}
		if st, ok := status[m.ID]; ok {
			st := st
			r.Status = &st
		}
		out = append(out, r)
	}
	writeJSON(w, 200, out)
}

func (s *Server) listMCP(w http.ResponseWriter, r *http.Request) { s.mcpOut(w) }

var mcpNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,30}$`)

func (s *Server) putMCP(w http.ResponseWriter, r *http.Request) {
	var in []*store.MCPServer
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	old := map[string]*store.MCPServer{}
	for _, m := range s.Store.Config().MCP {
		old[m.ID] = m
	}
	seen := map[string]bool{}
	for _, m := range in {
		m.Name = strings.TrimSpace(m.Name)
		if !mcpNameRe.MatchString(m.Name) {
			writeErr(w, errors.New("MCP server name: 1-31 chars, letters, digits, - or _ (it prefixes tool names)"))
			return
		}
		if seen[strings.ToLower(m.Name)] {
			writeErr(w, errors.New("duplicate MCP server name "+m.Name))
			return
		}
		seen[strings.ToLower(m.Name)] = true
		switch m.Transport {
		case "http", "sse":
			if !strings.HasPrefix(m.URL, "http://") && !strings.HasPrefix(m.URL, "https://") {
				writeErr(w, errors.New(m.Name+": URL must start with http(s)://"))
				return
			}
		case "stdio":
			if strings.TrimSpace(m.Command) == "" {
				writeErr(w, errors.New(m.Name+": stdio needs a command"))
				return
			}
		default:
			writeErr(w, errors.New(m.Name+": transport must be http, sse or stdio"))
			return
		}
		for _, role := range m.Roles {
			if role != store.RoleWorker && role != store.RoleChat {
				writeErr(w, errors.New("MCP roles: worker and/or chat"))
				return
			}
		}
		if m.ID == "" {
			m.ID = store.ShortID()
		}
		if o := old[m.ID]; o != nil {
			m.Headers, m.Env = unmask(m.Headers, o.Headers), unmask(m.Env, o.Env)
		}
	}
	if err := s.Store.UpdateConfig(func(c *store.Config) error { c.MCP = in; return nil }); err != nil {
		writeErr(w, err)
		return
	}
	s.mcpOut(w)
}

// testMCP (re)connects a saved server and lists its tools.
func (s *Server) testMCP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, m := range s.Store.Config().MCP {
		if m.ID == id {
			ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
			defer cancel()
			writeJSON(w, 200, s.Engine.MCP.Refresh(ctx, *m))
			return
		}
	}
	writeErr(w, store.ErrNotFound)
}
