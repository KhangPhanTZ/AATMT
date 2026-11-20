// Package server exposes the JSON/SSE API and serves the embedded web UI.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/agent"
	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/logbuf"
	"github.com/khangphantz/aatmt/internal/store"
)

type Server struct {
	Store   *store.Store
	Engine  *agent.Engine
	Pool    *llm.Pool
	Token   string // optional shared token (required when listening beyond loopback)
	UI      fs.FS
	Local   bool // listening on loopback only
	Logs    *logbuf.Buffer
	Version string
}

// lim returns the configured limits (Settings → Limits).
func (s *Server) lim() store.Limits { return s.Store.Config().Settings.Limits }

// Apply pushes settings that live outside the store into the running
// components (provider pool, model defaults, log buffer). Called at startup
// and whenever settings are saved.
func (s *Server) Apply(st store.Settings) {
	l := st.Limits
	s.Pool.Configure(llm.Policy{Attempts: st.RetryAttempts, Cooldown: time.Duration(st.BreakerCooldownSec) * time.Second,
		TripAfter: l.BreakerTripAfter, Backoff: time.Duration(l.RetryBackoffSec) * time.Second,
		RetryAfterCap: time.Duration(l.RetryAfterCapSec) * time.Second})
	llm.SetDefaults(l)
	if s.Logs != nil {
		s.Logs.SetMax(l.LogLines)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// config & providers
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("PUT /api/roles", s.putRoles)
	mux.HandleFunc("POST /api/profiles", s.saveProfile)
	mux.HandleFunc("PUT /api/profiles/{id}", s.saveProfile)
	mux.HandleFunc("DELETE /api/profiles/{id}", s.deleteProfile)
	mux.HandleFunc("POST /api/profiles/{id}/test", s.testProfile)
	mux.HandleFunc("POST /api/profiles/{id}/models", s.listModels)
	mux.HandleFunc("POST /api/profiles/{id}/reset", s.resetBreaker)
	mux.HandleFunc("GET /api/agents", s.agents)
	mux.HandleFunc("PUT /api/agents/{role}", s.putAgent)
	mux.HandleFunc("PUT /api/skills", s.putSkills)
	mux.HandleFunc("PUT /api/rules", s.putRules)
	mux.HandleFunc("PUT /api/webhooks", s.putWebhooks)
	mux.HandleFunc("POST /api/webhooks/test", s.testWebhook)
	mux.HandleFunc("PUT /api/templates", s.putTemplates)
	mux.HandleFunc("GET /api/mcp", s.listMCP)
	mux.HandleFunc("PUT /api/mcp", s.putMCP)
	mux.HandleFunc("POST /api/mcp/{id}/test", s.testMCP)
	// workspace
	mux.HandleFunc("GET /api/dashboard", s.dashboard)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/approvals", s.approvalRecords)
	mux.HandleFunc("GET /api/llm-records", s.llmRecords)
	mux.HandleFunc("GET /api/logs", s.logs)
	// projects
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects/{id}", s.getProject)
	mux.HandleFunc("PATCH /api/projects/{id}", s.patchProject)
	mux.HandleFunc("DELETE /api/projects/{id}", s.deleteProject)
	mux.HandleFunc("POST /api/projects/{id}/sync", s.syncProject)
	mux.HandleFunc("GET /api/projects/{id}/files", s.projectFiles)
	mux.HandleFunc("GET /api/projects/{id}/file", s.projectFile)
	mux.HandleFunc("POST /api/projects/{id}/chat", s.projectChat)
	mux.HandleFunc("DELETE /api/projects/{id}/chat", s.clearProjectChat)
	// tasks
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.patchTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/messages", s.addMessage)
	mux.HandleFunc("DELETE /api/tasks/{id}/messages/{mid}", s.deleteMessage)
	mux.HandleFunc("POST /api/tasks/{id}/images", s.addImages)
	mux.HandleFunc("GET /api/tasks/{id}/images/{img}", s.getImage)
	mux.HandleFunc("PUT /api/tasks/{id}/images/{img}", s.putImage)
	mux.HandleFunc("DELETE /api/tasks/{id}/images/{img}", s.deleteImage)
	mux.HandleFunc("POST /api/tasks/{id}/source", s.setSource)
	mux.HandleFunc("GET /api/tasks/{id}/estimate", s.estimate)
	mux.HandleFunc("POST /api/tasks/{id}/analyze", s.analyze)
	mux.HandleFunc("POST /api/tasks/{id}/transcribe", s.transcribe)
	mux.HandleFunc("POST /api/tasks/{id}/run", s.run)
	mux.HandleFunc("POST /api/tasks/{id}/pause", s.pause)
	mux.HandleFunc("POST /api/tasks/{id}/note", s.note)
	mux.HandleFunc("POST /api/tasks/{id}/chat", s.taskChat)
	mux.HandleFunc("POST /api/tasks/{id}/approval", s.approval)
	mux.HandleFunc("POST /api/tasks/{id}/duplicate", s.duplicate)
	mux.HandleFunc("POST /api/tasks/{id}/promote", s.promote)
	mux.HandleFunc("POST /api/tasks/{id}/template", s.saveTemplate)
	mux.HandleFunc("GET /api/tasks/{id}/report", s.report)
	mux.HandleFunc("GET /api/tasks/{id}/events", s.events)
	mux.HandleFunc("GET /api/tasks/{id}/llm", s.taskLLM)
	mux.HandleFunc("GET /api/tasks/{id}/diff", s.diff)
	mux.HandleFunc("POST /api/tasks/{id}/revert", s.revert)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": s.Version})
	})

	if s.UI != nil {
		static := http.FileServer(http.FS(s.UI))
		mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			static.ServeHTTP(w, r)
		}))
	}
	return s.guard(mux)
}

// guard: auth token, CSRF and DNS-rebinding protection. The server can read
// local folders and run commands, so a random web page must not reach it.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; connect-src 'self'")
		if s.Local && !loopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			if r.Method != http.MethodGet && r.Header.Get("X-AATMT") != "1" {
				http.Error(w, "missing X-AATMT header", http.StatusForbidden)
				return
			}
			if s.Token != "" {
				tok := r.Header.Get("Authorization")
				tok = strings.TrimPrefix(tok, "Bearer ")
				if tok == "" {
					tok = r.URL.Query().Get("token")
				}
				if subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
					writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "need_token": true})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func sameOrigin(origin, host string) bool {
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return o == host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, agent.ErrBusy):
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

func (s *Server) readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, int64(s.lim().MaxUploadMB)<<20))
	return dec.Decode(v)
}

// parseMultipart caps the request at max_upload_mb; parts beyond the
// in-memory share are spooled to temporary files by net/http.
func (s *Server) parseMultipart(w http.ResponseWriter, r *http.Request) error {
	max := int64(s.lim().MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, max)
	return r.ParseMultipartForm(max)
}

// ---------- SSE ----------

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := s.Store.Hub().Subscribe(r.URL.Query().Get("task"))
	defer s.Store.Hub().Unsubscribe(ch)
	_, _ = w.Write([]byte(": ok\n\n"))
	fl.Flush()
	ping := time.NewTicker(time.Duration(s.lim().StreamPingSec) * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			fl.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(ev)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			fl.Flush()
		}
	}
}
