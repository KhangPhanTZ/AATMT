package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/store"
)

type profileDTO struct {
	store.Profile
	APIKey  string       `json:"api_key"` // always blank in responses
	KeyHint string       `json:"key_hint"`
	KeySet  bool         `json:"key_set"`
	Usage   store.Usage  `json:"usage"`
	Breaker *llm.Breaker `json:"breaker,omitempty"`
}

func keyHint(k string) string {
	if strings.HasPrefix(k, "env:") {
		return k
	}
	if len(k) <= 8 {
		return strings.Repeat("•", len(k))
	}
	return k[:4] + "…" + k[len(k)-4:]
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Config()
	brk := s.Pool.State()
	profiles := make([]profileDTO, 0, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		d := profileDTO{Profile: *p, KeyHint: keyHint(p.APIKey), KeySet: p.APIKey != "", Usage: cfg.Usage[p.ID]}
		if b, ok := brk[p.ID]; ok {
			b := b
			d.Breaker = &b
		}
		profiles = append(profiles, d)
	}
	settings := cfg.Settings
	settings.WebSearchKey = keyHint(settings.WebSearchKey)
	skills, rules, hooks, tpls := cfg.Skills, cfg.Rules, cfg.Webhooks, cfg.Templates
	if skills == nil {
		skills = []*store.Skill{}
	}
	if rules == nil {
		rules = []*store.Rule{}
	}
	if hooks == nil {
		hooks = []*store.Webhook{}
	}
	if tpls == nil {
		tpls = []*store.Template{}
	}
	writeJSON(w, 200, map[string]any{
		"profiles": profiles, "roles": cfg.Roles, "settings": settings, "skills": skills, "rules": rules,
		"webhooks": hooks, "templates": tpls, "stages": store.Stages, "data_dir": s.Store.Dir(),
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var st store.Settings
	if err := readJSON(r, &st); err != nil {
		writeErr(w, err)
		return
	}
	switch st.CommandPolicy {
	case "off", "ask", "allowlist", "auto":
	default:
		writeErr(w, errors.New("command_policy must be off|ask|allowlist|auto"))
		return
	}
	err := s.Store.UpdateConfig(func(c *store.Config) error {
		// the UI only sees a hint of the search key: keep the stored one unless replaced
		if st.WebSearchKey == "" || st.WebSearchKey == keyHint(c.Settings.WebSearchKey) {
			st.WebSearchKey = c.Settings.WebSearchKey
		}
		if st.WebSearchKey == "-" {
			st.WebSearchKey = ""
		}
		c.Settings = st
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Pool.SetPolicy(st.RetryAttempts, time.Duration(st.BreakerCooldownSec)*time.Second)
	s.getConfig(w, r)
}

func (s *Server) putRoles(w http.ResponseWriter, r *http.Request) {
	var roles store.Roles
	if err := readJSON(r, &roles); err != nil {
		writeErr(w, err)
		return
	}
	err := s.Store.UpdateConfig(func(c *store.Config) error { c.Roles = roles; return nil })
	if err != nil {
		writeErr(w, err)
		return
	}
	s.getConfig(w, r)
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	var in store.Profile
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("id")
	in.Name = strings.TrimSpace(in.Name)
	in.Model = strings.TrimSpace(in.Model)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if in.Format != "anthropic" {
		in.Format = "openai"
	}
	if in.ToolMode != "json" {
		in.ToolMode = "native"
	}
	if in.Model == "" {
		writeErr(w, errors.New("model is required"))
		return
	}
	if in.Name == "" {
		in.Name = in.Model
	}
	err := s.Store.UpdateConfig(func(c *store.Config) error {
		if id == "" {
			in.ID = store.ShortID()
			c.Profiles = append(c.Profiles, &in)
			// first profile: make it usable for every role right away
			return nil
		}
		for i, p := range c.Profiles {
			if p.ID == id {
				in.ID = id
				if in.APIKey == "" {
					in.APIKey = p.APIKey // blank = keep the stored key
				}
				if in.APIKey == "-" {
					in.APIKey = ""
				}
				c.Profiles[i] = &in
				return nil
			}
		}
		return store.ErrNotFound
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Pool.Reset(in.ID)
	s.getConfig(w, r)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Store.UpdateConfig(func(c *store.Config) error {
		out := c.Profiles[:0]
		for _, p := range c.Profiles {
			if p.ID != id {
				out = append(out, p)
			}
		}
		c.Profiles = out
		strip := func(ids []string) []string {
			var o []string
			for _, x := range ids {
				if x != id {
					o = append(o, x)
				}
			}
			return o
		}
		c.Roles.Vision, c.Roles.Analyzer, c.Roles.Worker = strip(c.Roles.Vision), strip(c.Roles.Analyzer), strip(c.Roles.Worker)
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.getConfig(w, r)
}

func (s *Server) testProfile(w http.ResponseWriter, r *http.Request) {
	p := s.Store.Profile(r.PathValue("id"))
	if p == nil {
		writeErr(w, store.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	p.Enabled = true
	resp, err := llm.Test(ctx, p)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.Store.AddProfileUsage(p.ID, resp.Usage)
	writeJSON(w, 200, map[string]any{"ok": true, "reply": resp.Text, "ms": resp.Latency.Milliseconds(), "usage": resp.Usage})
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	p := s.Store.Profile(r.PathValue("id"))
	if p == nil {
		writeErr(w, store.ErrNotFound)
		return
	}
	ids, err := llm.ListModels(r.Context(), p)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "models": ids})
}

func (s *Server) resetBreaker(w http.ResponseWriter, r *http.Request) {
	s.Pool.Reset(r.PathValue("id"))
	s.getConfig(w, r)
}
