package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/agent"
	"github.com/khangphantz/aatmt/internal/notify"
	"github.com/khangphantz/aatmt/internal/store"
)

// ---------- dashboard ----------

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Config()
	tasks := s.Store.Tasks()
	stages := map[string]int{}
	titles := map[string]string{}
	type attention struct {
		TaskID string `json:"task_id"`
		Title  string `json:"title"`
		Kind   string `json:"kind"`
		Text   string `json:"text"`
	}
	att := []attention{}
	running := 0
	type top struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Tokens int    `json:"tokens"`
		Stage  string `json:"stage"`
	}
	var tops []top
	doneWeek := 0
	weekAgo := time.Now().AddDate(0, 0, -7)
	for _, t := range tasks {
		titles[t.ID] = t.Title
		if t.DoneAt != nil && t.DoneAt.After(weekAgo) {
			doneWeek++
		}
		if t.Archived {
			continue
		}
		stages[t.Stage]++
		if s.Engine.Running(t.ID) != "" {
			running++
		}
		if t.Pending != nil {
			att = append(att, attention{t.ID, t.Title, "approval", t.Pending.Command})
		}
		if t.Stage == store.StagePaused && strings.HasPrefix(t.Status, "question:") {
			att = append(att, attention{t.ID, t.Title, "question", strings.TrimSpace(strings.TrimPrefix(t.Status, "question:"))})
		} else if t.Error != "" {
			att = append(att, attention{t.ID, t.Title, "error", t.Error})
		} else if t.Stage == store.StageReview {
			att = append(att, attention{t.ID, t.Title, "review", t.Status})
		}
		tops = append(tops, top{t.ID, t.Title, t.Usage.Total(), t.Stage})
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].Tokens > tops[j].Tokens })
	if len(tops) > 6 {
		tops = tops[:6]
	}
	type day struct {
		Date string `json:"date"`
		store.Usage
	}
	var days []day
	var week store.Usage
	l := cfg.Settings.Limits
	for i := l.DashboardDays - 1; i >= 0; i-- {
		d := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
		u := cfg.Daily[d]
		days = append(days, day{d, u})
		if i < 7 {
			week.Add(u)
		}
	}
	var total store.Usage
	brk := s.Pool.State()
	type prov struct {
		ID      string      `json:"id"`
		Name    string      `json:"name"`
		Model   string      `json:"model"`
		Enabled bool        `json:"enabled"`
		Usage   store.Usage `json:"usage"`
		Healthy bool        `json:"healthy"`
		Error   string      `json:"error,omitempty"`
	}
	provs := []prov{}
	for _, p := range cfg.Profiles {
		u := cfg.Usage[p.ID]
		total.Add(u)
		pv := prov{ID: p.ID, Name: p.Name, Model: p.Model, Enabled: p.Enabled, Usage: u, Healthy: true}
		if b, ok := brk[p.ID]; ok && time.Now().Before(b.OpenUntil) {
			pv.Healthy, pv.Error = false, b.LastError
		}
		provs = append(provs, pv)
	}
	recent := s.Store.Recent(l.DashboardItems)
	type ev struct {
		store.Event
		TaskTitle string `json:"task_title"`
	}
	evs := []ev{}
	for i := len(recent) - 1; i >= 0; i-- {
		e := recent[i]
		if e.Type == "llm" || e.Type == "tool" || e.Type == "change" {
			continue
		}
		evs = append(evs, ev{e, titles[e.TaskID]})
	}
	writeJSON(w, 200, map[string]any{
		"stages": stages, "tasks": len(tasks), "projects": len(s.Store.Projects()), "running": running,
		"done_week": doneWeek, "attention": att, "top_tasks": tops, "daily": days, "week": week, "total": total,
		"providers": provs, "recent": evs, "approvals": len(s.Store.Approvals(0)),
	})
}

// ---------- audit / records / logs ----------

func (s *Server) approvalRecords(w http.ResponseWriter, r *http.Request) {
	recs := s.Store.Approvals(s.lim().ListMaxItems)
	// newest first
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	pending := []map[string]any{}
	for _, t := range s.Store.Tasks() {
		if t.Pending != nil {
			pending = append(pending, map[string]any{"task_id": t.ID, "task_title": t.Title, "id": t.Pending.ID, "command": t.Pending.Command, "at": t.Pending.At})
		}
	}
	writeJSON(w, 200, map[string]any{"pending": pending, "records": recs})
}

func (s *Server) llmRecords(w http.ResponseWriter, r *http.Request) {
	var all []store.LLMRecord
	titles := map[string]string{}
	max := s.lim().ListMaxItems
	for _, t := range s.Store.Tasks() {
		titles[t.ID] = t.Title
		all = append(all, s.Store.LLMRecords(t.ID, max)...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].At.After(all[j].At) })
	if len(all) > max {
		all = all[:max]
	}
	type rec struct {
		store.LLMRecord
		TaskTitle string `json:"task_title"`
	}
	out := []rec{}
	for _, x := range all {
		out = append(out, rec{x, titles[x.TaskID]})
	}
	writeJSON(w, 200, out)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	if s.Logs == nil {
		writeJSON(w, 200, []any{})
		return
	}
	writeJSON(w, 200, s.Logs.Lines())
}

// ---------- agents (role prompts + chains) ----------

var roleInfo = map[string]string{
	store.RoleVision:   "Reads screenshots once (OCR + description); the transcript is cached and reused by every other agent.",
	store.RoleAnalyzer: "Reads the request, project knowledge, skills and ranked code; writes the brief and the step plan.",
	store.RoleWorker:   "Executes plan steps with workspace tools, project commands and approvals; saves memory.",
	store.RoleChat:     "Answers side questions about a task or a codebase with read-only tools.",
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Config()
	out := []map[string]any{}
	tools := map[string][]string{
		store.RoleVision:   {},
		store.RoleAnalyzer: {},
		store.RoleWorker:   {"list_dir", "read_file", "search", "write_file", "edit_file", "delete_file", "run_command", "project_command", "load_skill", "web_search", "web_fetch", "save_memory", "ask_user", "step_done"},
		store.RoleChat:     {"list_dir", "read_file", "search", "load_skill", "web_search", "web_fetch"},
	}
	for _, role := range store.AgentRoles {
		chain := cfg.Roles.Get(role)
		if role == store.RoleChat {
			chain = cfg.Roles.Chat
		}
		if chain == nil {
			chain = []string{}
		}
		var mcpServers []string
		for _, m := range cfg.MCP {
			for _, r := range m.Roles {
				if r == role && m.Enabled {
					mcpServers = append(mcpServers, m.Name)
				}
			}
		}
		if mcpServers == nil {
			mcpServers = []string{}
		}
		out = append(out, map[string]any{
			"role": role, "description": roleInfo[role], "builtin": agent.BuiltinPrompts[role],
			"prompt": cfg.Prompts[role], "chain": chain, "tools": tools[role], "mcp": mcpServers,
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) putAgent(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	ok := false
	for _, x := range store.AgentRoles {
		ok = ok || x == role
	}
	if !ok {
		writeErr(w, errors.New("unknown agent role"))
		return
	}
	var in struct {
		Prompt *string   `json:"prompt"`
		Chain  *[]string `json:"chain"`
	}
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	err := s.Store.UpdateConfig(func(c *store.Config) error {
		if in.Prompt != nil {
			c.Prompts[role] = strings.TrimSpace(*in.Prompt)
		}
		if in.Chain != nil {
			switch role {
			case store.RoleVision:
				c.Roles.Vision = *in.Chain
			case store.RoleAnalyzer:
				c.Roles.Analyzer = *in.Chain
			case store.RoleWorker:
				c.Roles.Worker = *in.Chain
			case store.RoleChat:
				c.Roles.Chat = *in.Chain
			}
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.agents(w, r)
}

// ---------- list resources (skills, rules, webhooks, templates) ----------

func (s *Server) putSkills(w http.ResponseWriter, r *http.Request) {
	var in []*store.Skill
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	for _, sk := range in {
		sk.Name = strings.TrimSpace(strings.ReplaceAll(sk.Name, " ", "-"))
		if sk.Name == "" {
			writeErr(w, errors.New("every skill needs a name"))
			return
		}
		if sk.ID == "" {
			sk.ID = store.ShortID()
		}
	}
	if err := s.Store.UpdateConfig(func(c *store.Config) error { c.Skills = in; return nil }); err != nil {
		writeErr(w, err)
		return
	}
	s.getConfig(w, r)
}

func (s *Server) putRules(w http.ResponseWriter, r *http.Request) {
	var in []*store.Rule
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	for _, ru := range in {
		switch ru.Kind {
		case "prefix", "argv", "contains":
		case "regex":
			if _, err := regexp.Compile(ru.Pattern); err != nil {
				writeErr(w, err)
				return
			}
		default:
			writeErr(w, errors.New("rule kind must be prefix|argv|regex|contains"))
			return
		}
		if ru.Action != "allow" && ru.Action != "ask" && ru.Action != "deny" {
			writeErr(w, errors.New("rule action must be allow|ask|deny"))
			return
		}
		if strings.TrimSpace(ru.Pattern) == "" {
			writeErr(w, errors.New("rule pattern is empty"))
			return
		}
		if ru.ID == "" {
			ru.ID = store.ShortID()
		}
	}
	if err := s.Store.UpdateConfig(func(c *store.Config) error { c.Rules = in; return nil }); err != nil {
		writeErr(w, err)
		return
	}
	s.getConfig(w, r)
}

func (s *Server) putWebhooks(w http.ResponseWriter, r *http.Request) {
	var in []*store.Webhook
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	for _, h := range in {
		if !strings.HasPrefix(h.URL, "https://") && !strings.HasPrefix(h.URL, "http://") {
			writeErr(w, errors.New("webhook URL must start with http(s)://"))
			return
		}
		if h.ID == "" {
			h.ID = store.ShortID()
		}
	}
	if err := s.Store.UpdateConfig(func(c *store.Config) error { c.Webhooks = in; return nil }); err != nil {
		writeErr(w, err)
		return
	}
	s.getConfig(w, r)
}

func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	var h store.Webhook
	if err := s.readJSON(r, &h); err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.lim().WebhookTimeoutSec)*time.Second)
	defer cancel()
	err := notify.Send(ctx, &h, "[TEST] AATMT notification works", notify.PublicURL(s.Store.Config().Settings.PublicURL))
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) putTemplates(w http.ResponseWriter, r *http.Request) {
	var in []*store.Template
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	for _, t := range in {
		if strings.TrimSpace(t.Name) == "" {
			writeErr(w, errors.New("every template needs a name"))
			return
		}
		if t.ID == "" {
			t.ID = store.ShortID()
		}
	}
	if err := s.Store.UpdateConfig(func(c *store.Config) error { c.Templates = in; return nil }); err != nil {
		writeErr(w, err)
		return
	}
	s.getConfig(w, r)
}

// ---------- global search (⌘K) ----------

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	type hit struct {
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Title string `json:"title"`
		Sub   string `json:"sub"`
	}
	out := []hit{}
	if q == "" {
		writeJSON(w, 200, out)
		return
	}
	for _, p := range s.Store.Projects() {
		if strings.Contains(strings.ToLower(p.Name+" "+p.Description+" "+p.Workspace), q) {
			out = append(out, hit{"project", p.ID, p.Name, p.Workspace})
		}
	}
	for _, t := range s.Store.Tasks() {
		hay := t.ID + " " + t.Title + " " + strings.Join(t.Tags, " ")
		for _, m := range t.Messages {
			hay += " " + m.Text
		}
		if t.Brief != nil {
			hay += " " + t.Brief.Summary
		}
		if strings.Contains(strings.ToLower(hay), q) {
			sub := t.Stage
			if t.Archived {
				sub += " · archived"
			}
			out = append(out, hit{"task", t.ID, t.Title, sub})
		}
		if len(out) >= s.lim().SearchMaxResults {
			break
		}
	}
	writeJSON(w, 200, out)
}
