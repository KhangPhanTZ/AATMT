package store

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")

// Store keeps everything in memory and writes JSON files on every change.
// The data set of a single-user task tool is small, so this is both the
// simplest and the fastest option (no database dependency).
type Store struct {
	dir string

	mu       sync.RWMutex
	cfg      *Config
	tasks    map[string]*Task
	projects map[string]*Project

	evMu   sync.Mutex // serialises *.jsonl appends
	hub    *Hub
	recent []Event // last events across all tasks (dashboard)

	// OnEvent is called (outside any lock) for every emitted task event; used for notifications.
	OnEvent func(Event)
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, tasks: map[string]*Task{}, projects: map[string]*Project{}, hub: NewHub()}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	if err := s.loadProjects(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "tasks"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "tasks", e.Name(), "task.json"))
		if err != nil {
			continue
		}
		var t Task
		if json.Unmarshal(b, &t) == nil && t.ID != "" {
			// A crash/restart interrupts live work: surface it as paused.
			if t.Stage == StageRunning || t.Stage == StageAnalyzing {
				prev := t.Stage
				t.Stage = StagePaused
				t.Status = "interrupted by restart (" + prev + ") — press Resume"
				for i := range t.Steps {
					if t.Steps[i].Status == StepRunning {
						t.Steps[i].Status = StepPending
					}
				}
				if prev == StageAnalyzing {
					t.Stage = StageInbox
				}
			}
			t.Pending = nil
			s.tasks[t.ID] = &t
		}
	}
	s.migrateLegacyWorkspaces()
	return s, nil
}

// migrateLegacyWorkspaces groups tasks created before projects existed into
// one project per workspace, so their history shows up per codebase.
func (s *Store) migrateLegacyWorkspaces() {
	byWS := map[string]*Project{}
	for _, p := range s.projects {
		byWS[p.Workspace] = p
	}
	for _, t := range s.tasks {
		if t.ProjectID != "" || t.Workspace == "" {
			continue
		}
		p := byWS[t.Workspace]
		if p == nil {
			p = &Project{ID: NewID(), Name: filepath.Base(t.Workspace), Source: t.Source, Workspace: t.Workspace,
				CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
			if os.MkdirAll(s.ProjectDir(p.ID), 0o700) != nil {
				continue
			}
			s.projects[p.ID] = p
			byWS[t.Workspace] = p
		}
		t.ProjectID = p.ID
		p.Usage.Add(t.Usage)
		_ = s.saveProjectLocked(p)
		_ = s.saveTaskLocked(t)
	}
}

func (s *Store) Dir() string { return s.dir }
func (s *Store) Hub() *Hub   { return s.hub }

func NewID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("060102") + "-" + hex.EncodeToString(b[:])
}

func ShortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---------- config ----------

func (s *Store) loadConfig() error {
	p := filepath.Join(s.dir, "config.json")
	cfg := &Config{Settings: DefaultSettings(), Usage: map[string]Usage{}}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		cfg.Skills = DefaultSkills()
	}
	if err == nil {
		if err := json.Unmarshal(b, cfg); err != nil {
			return fmt.Errorf("config.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	fillConfigDefaults(cfg)
	s.cfg = cfg
	return s.saveConfigLocked()
}

func fillConfigDefaults(c *Config) {
	if c.Usage == nil {
		c.Usage = map[string]Usage{}
	}
	if c.Daily == nil {
		c.Daily = map[string]Usage{}
	}
	if c.Prompts == nil {
		c.Prompts = map[string]string{}
	}
	fillSettingDefaults(&c.Settings)
}

func fillSettingDefaults(st *Settings) {
	d := DefaultSettings()
	if st.MaxConcurrentTasks <= 0 {
		st.MaxConcurrentTasks = d.MaxConcurrentTasks
	}
	if st.MaxTurnsPerStep <= 0 {
		st.MaxTurnsPerStep = d.MaxTurnsPerStep
	}
	if st.CodeContextTokens <= 0 {
		st.CodeContextTokens = d.CodeContextTokens
	}
	if st.CompactAtPct <= 0 || st.CompactAtPct > 95 {
		st.CompactAtPct = d.CompactAtPct
	}
	if st.ToolOutputMaxChars <= 0 {
		st.ToolOutputMaxChars = d.ToolOutputMaxChars
	}
	if st.CommandPolicy == "" {
		st.CommandPolicy = d.CommandPolicy
	}
	if st.CommandTimeoutSec <= 0 {
		st.CommandTimeoutSec = d.CommandTimeoutSec
	}
	if st.Language == "" {
		st.Language = d.Language
	}
	if st.RetryAttempts <= 0 {
		st.RetryAttempts = d.RetryAttempts
	}
}

func (s *Store) saveConfigLocked() error {
	return writeJSON(filepath.Join(s.dir, "config.json"), s.cfg)
}

// Config returns a deep copy of the configuration.
func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var c Config
	clone(s.cfg, &c)
	return c
}

func (s *Store) UpdateConfig(fn func(c *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Config
	clone(s.cfg, &c)
	if err := fn(&c); err != nil {
		return err
	}
	fillConfigDefaults(&c)
	s.cfg = &c
	return s.saveConfigLocked()
}

func (s *Store) Profile(id string) *Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.cfg.Profiles {
		if p.ID == id {
			var c Profile
			clone(p, &c)
			return &c
		}
	}
	return nil
}

func (s *Store) AddProfileUsage(id string, u Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.cfg.Usage[id]
	cur.Add(u)
	s.cfg.Usage[id] = cur
	day := time.Now().UTC().Format("2006-01-02")
	d := s.cfg.Daily[day]
	d.Add(u)
	s.cfg.Daily[day] = d
	if len(s.cfg.Daily) > 90 {
		cut := time.Now().UTC().AddDate(0, 0, -90).Format("2006-01-02")
		for k := range s.cfg.Daily {
			if k < cut {
				delete(s.cfg.Daily, k)
			}
		}
	}
	_ = s.saveConfigLocked()
}

// ---------- tasks ----------

func (s *Store) TaskDir(id string) string { return filepath.Join(s.dir, "tasks", id) }

func (s *Store) CreateTask(t *Task) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	if t.Stage == "" {
		t.Stage = StageInbox
	}
	if t.Priority == "" {
		t.Priority = "normal"
	}
	if err := os.MkdirAll(filepath.Join(s.TaskDir(t.ID), "images"), 0o700); err != nil {
		return err
	}
	s.mu.Lock()
	s.tasks[t.ID] = t
	err := s.saveTaskLocked(t)
	s.mu.Unlock()
	s.hub.Publish(Event{TaskID: t.ID, Type: "task", Title: "created"})
	return err
}

func (s *Store) saveTaskLocked(t *Task) error {
	return writeJSON(filepath.Join(s.TaskDir(t.ID), "task.json"), t)
}

// Task returns a deep copy.
func (s *Store) Task(id string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	var c Task
	clone(t, &c)
	return &c, nil
}

func (s *Store) Tasks() []*Task {
	s.mu.RLock()
	out := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		var c Task
		clone(t, &c)
		out = append(out, &c)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// UpdateTask applies fn to the live task under the lock and persists it.
func (s *Store) UpdateTask(id string, fn func(t *Task) error) (*Task, error) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	if err := fn(t); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	t.UpdatedAt = time.Now().UTC()
	err := s.saveTaskLocked(t)
	var c Task
	clone(t, &c)
	s.mu.Unlock()
	s.hub.Publish(Event{TaskID: id, Type: "task", Title: "updated"})
	return &c, err
}

func (s *Store) DeleteTask(id string) error {
	s.mu.Lock()
	_, ok := s.tasks[id]
	delete(s.tasks, id)
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	s.hub.Publish(Event{TaskID: id, Type: "task", Title: "deleted"})
	return os.RemoveAll(s.TaskDir(id))
}

// ---------- events ----------

// Emit appends an event to the task log and broadcasts it to live subscribers.
func (s *Store) Emit(ev Event) Event {
	s.mu.Lock()
	t, ok := s.tasks[ev.TaskID]
	if ok {
		t.EventSeq++
		ev.Seq = t.EventSeq
	}
	s.mu.Unlock()
	if !ok {
		return ev
	}
	ev.At = time.Now().UTC()
	s.evMu.Lock()
	f, err := os.OpenFile(filepath.Join(s.TaskDir(ev.TaskID), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		b, _ := json.Marshal(ev)
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
	if ev.Type != "task" {
		s.recent = append(s.recent, ev)
		if len(s.recent) > 200 {
			s.recent = s.recent[len(s.recent)-200:]
		}
	}
	s.evMu.Unlock()
	s.hub.Publish(ev)
	if s.OnEvent != nil {
		s.OnEvent(ev)
	}
	return ev
}

// Recent returns the latest events across all tasks, newest last.
func (s *Store) Recent(n int) []Event {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	if n > len(s.recent) {
		n = len(s.recent)
	}
	out := make([]Event, n)
	copy(out, s.recent[len(s.recent)-n:])
	return out
}

// ---------- approvals audit ----------

func (s *Store) AddApproval(r ApprovalRecord) {
	if r.ID == "" {
		r.ID = ShortID()
	}
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	s.appendJSONL(filepath.Join(s.dir, "approvals.jsonl"), r)
}

func (s *Store) Approvals(limit int) []ApprovalRecord {
	var out []ApprovalRecord
	readJSONL(filepath.Join(s.dir, "approvals.jsonl"), func(b []byte) {
		var r ApprovalRecord
		if json.Unmarshal(b, &r) == nil {
			out = append(out, r)
		}
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	if out == nil {
		out = []ApprovalRecord{}
	}
	return out
}

// ---------- LLM records ----------

func (s *Store) AddLLMRecord(r LLMRecord) {
	s.mu.Lock()
	t, ok := s.tasks[r.TaskID]
	if ok {
		t.LLMSeq++
		r.Seq = t.LLMSeq
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	r.At = time.Now().UTC()
	s.appendJSONL(filepath.Join(s.TaskDir(r.TaskID), "llm.jsonl"), r)
}

func (s *Store) LLMRecords(taskID string, limit int) []LLMRecord {
	out := []LLMRecord{}
	readJSONL(filepath.Join(s.TaskDir(taskID), "llm.jsonl"), func(b []byte) {
		var r LLMRecord
		if json.Unmarshal(b, &r) == nil {
			out = append(out, r)
		}
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (s *Store) appendJSONL(path string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.evMu.Lock()
	defer s.evMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

func readJSONL(path string, fn func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		fn(sc.Bytes())
	}
}

func (s *Store) Events(taskID string, after int, limit int) ([]Event, error) {
	f, err := os.Open(filepath.Join(s.TaskDir(taskID), "events.jsonl"))
	if os.IsNotExist(err) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []Event{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Seq > after {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// ---------- helpers ----------

func clone(src, dst any) {
	b, _ := json.Marshal(src)
	_ = json.Unmarshal(b, dst)
}

// writeJSON writes atomically (temp file + rename) so a crash never leaves a torn file.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ResolveKey resolves "env:NAME" API key references.
func ResolveKey(k string) string {
	if strings.HasPrefix(k, "env:") {
		return os.Getenv(strings.TrimPrefix(k, "env:"))
	}
	return k
}
