package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func (s *Store) ProjectDir(id string) string { return filepath.Join(s.dir, "projects", id) }

func (s *Store) loadProjects() error {
	dir := filepath.Join(s.dir, "projects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "project.json"))
		if err != nil {
			continue
		}
		var p Project
		if json.Unmarshal(b, &p) == nil && p.ID != "" {
			s.projects[p.ID] = &p
		}
	}
	return nil
}

func (s *Store) saveProjectLocked(p *Project) error {
	return writeJSON(filepath.Join(s.ProjectDir(p.ID), "project.json"), p)
}

func (s *Store) CreateProject(p *Project) error {
	if p.ID == "" {
		p.ID = NewID()
	}
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now
	if err := os.MkdirAll(s.ProjectDir(p.ID), 0o700); err != nil {
		return err
	}
	s.mu.Lock()
	s.projects[p.ID] = p
	err := s.saveProjectLocked(p)
	s.mu.Unlock()
	s.hub.Publish(Event{Type: "project", Title: "created", Data: map[string]any{"project_id": p.ID}})
	return err
}

func (s *Store) Project(id string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, ErrNotFound
	}
	var c Project
	clone(p, &c)
	return &c, nil
}

func (s *Store) Projects() []*Project {
	s.mu.RLock()
	out := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		var c Project
		clone(p, &c)
		out = append(out, &c)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (s *Store) UpdateProject(id string, fn func(p *Project) error) (*Project, error) {
	s.mu.Lock()
	p, ok := s.projects[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	if err := fn(p); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	p.UpdatedAt = time.Now().UTC()
	err := s.saveProjectLocked(p)
	var c Project
	clone(p, &c)
	s.mu.Unlock()
	s.hub.Publish(Event{Type: "project", Title: "updated", Data: map[string]any{"project_id": id}})
	return &c, err
}

// DeleteProject removes the project record (and its cloned workspace). Tasks
// keep their history but lose the project link.
func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	_, ok := s.projects[id]
	delete(s.projects, id)
	for _, t := range s.tasks {
		if t.ProjectID == id {
			t.ProjectID = ""
			_ = s.saveTaskLocked(t)
		}
	}
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	s.hub.Publish(Event{Type: "project", Title: "deleted", Data: map[string]any{"project_id": id}})
	return os.RemoveAll(s.ProjectDir(id))
}

// AddProjectUsage books token usage on the project as well.
func (s *Store) AddProjectUsage(id string, u Usage) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[id]; ok {
		p.Usage.Add(u)
		_ = s.saveProjectLocked(p)
	}
}

// Workspace resolves the effective workspace of a task: its project's, or
// the legacy per-task one.
func (s *Store) Workspace(t *Task) string {
	if t.ProjectID != "" {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if p, ok := s.projects[t.ProjectID]; ok {
			return p.Workspace
		}
	}
	return t.Workspace
}

// DefaultSkills seeds a fresh install with a few useful playbooks.
func DefaultSkills() []*Skill {
	return []*Skill{
		{ID: ShortID(), Name: "minimal-diff", Enabled: true, Always: true,
			Description: "Keep changes small and in the existing style",
			Content:     "- Change only what the task needs; do not reformat unrelated code.\n- Match naming, error handling and formatting of the surrounding code.\n- Prefer editing existing functions/components over adding new abstractions.\n- Never commit, push or change git history unless the task explicitly asks."},
		{ID: ShortID(), Name: "bugfix", Enabled: true, Keywords: []string{"bug", "fix", "error", "crash", "lỗi", "exception", "broken"},
			Description: "Reproduce → locate root cause → fix → verify",
			Content:     "1. Find where the reported behaviour happens (search for the error text / UI label).\n2. Identify the root cause, not just the symptom; note it with save_memory.\n3. Apply the smallest fix.\n4. Verify with the project's build/test command; add a regression test when a test suite exists."},
		{ID: ShortID(), Name: "ui-change", Enabled: true, Keywords: []string{"ui", "button", "page", "screen", "css", "layout", "giao diện", "màu", "color", "style"},
			Description: "Frontend/UI change checklist",
			Content:     "- Locate the component by searching visible text from the screenshot.\n- Reuse existing components, tokens and CSS classes.\n- Check responsive behaviour and dark mode if the app has them.\n- Mention in the step summary which screen/route to check manually."},
		{ID: ShortID(), Name: "api-change", Enabled: true, Keywords: []string{"api", "endpoint", "route", "handler", "request", "response"},
			Description: "Backend endpoint change checklist",
			Content:     "- Keep backward compatibility of request/response shapes unless told otherwise.\n- Validate input, handle errors consistently with sibling handlers.\n- Update API docs/types/clients that reference the endpoint.\n- Run the backend tests."},
	}
}
