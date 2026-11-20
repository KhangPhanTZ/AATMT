package server

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/codeidx"
	"github.com/khangphantz/aatmt/internal/store"
)

type projectSummary struct {
	*store.Project
	Tasks    int            `json:"tasks"`
	Active   int            `json:"active"`
	Done     int            `json:"done"`
	Stages   map[string]int `json:"stages"`
	ChatBusy bool           `json:"chat_busy"`
	LastTask *time.Time     `json:"last_task,omitempty"`
}

func (s *Server) projectSummary(p *store.Project) projectSummary {
	ps := projectSummary{Project: p, Stages: map[string]int{}, ChatBusy: s.Engine.ChatBusy("project", p.ID)}
	if p.Knowledge == nil {
		p.Knowledge = []store.Knowledge{}
	}
	if p.Commands == nil {
		p.Commands = []store.Command{}
	}
	if p.Chat == nil {
		p.Chat = []store.ChatMsg{}
	}
	if p.SkillIDs == nil {
		p.SkillIDs = []string{}
	}
	for _, t := range s.Store.Tasks() {
		if t.ProjectID != p.ID {
			continue
		}
		ps.Tasks++
		ps.Stages[t.Stage]++
		if t.Stage == store.StageDone {
			ps.Done++
		} else if !t.Archived {
			ps.Active++
		}
		if ps.LastTask == nil || t.UpdatedAt.After(*ps.LastTask) {
			u := t.UpdatedAt
			ps.LastTask = &u
		}
	}
	return ps
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	out := []projectSummary{}
	for _, p := range s.Store.Projects() {
		ps := s.projectSummary(p)
		ps.Chat = nil // keep the list light
		out = append(out, ps)
	}
	writeJSON(w, 200, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Project(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"project": s.projectSummary(p), "git": gitInfo(p.Workspace)})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var src store.Source
	var zipFile *multipart.FileHeader
	name, desc := "", ""
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := s.parseMultipart(w, r); err != nil {
			writeErr(w, err)
			return
		}
		f := r.MultipartForm
		name, desc = first(f.Value["name"]), first(f.Value["description"])
		src = store.Source{Kind: first(f.Value["source_kind"]), Path: first(f.Value["source_path"]),
			URL: first(f.Value["git_url"]), Ref: first(f.Value["git_ref"])}
		if z := f.File["zip"]; len(z) > 0 {
			zipFile = z[0]
		}
	} else {
		var in struct {
			Name        string       `json:"name"`
			Description string       `json:"description"`
			Source      store.Source `json:"source"`
		}
		if err := s.readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		name, desc, src = in.Name, in.Description, in.Source
	}
	if (src.Kind == "" || src.Kind == "none") && zipFile == nil {
		p := &store.Project{Name: strings.TrimSpace(name), Description: desc, Source: store.Source{Kind: "none"}}
		if p.Name == "" {
			writeErr(w, errors.New("a project needs a name or a source"))
			return
		}
		if err := s.Store.CreateProject(p); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"project": s.projectSummary(p)})
		return
	}
	id, err := s.ensureProject(r.Context(), src, zipFile, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if desc != "" {
		_, _ = s.Store.UpdateProject(id, func(p *store.Project) error { p.Description = desc; return nil })
	}
	p, _ := s.Store.Project(id)
	writeJSON(w, 200, map[string]any{"project": s.projectSummary(p)})
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        *string            `json:"name"`
		Description *string            `json:"description"`
		Commands    *[]store.Command   `json:"commands"`
		SkillIDs    *[]string          `json:"skill_ids"`
		Overrides   *store.Overrides   `json:"overrides"`
		Archived    *bool              `json:"archived"`
		Knowledge   *[]store.Knowledge `json:"knowledge"`
	}
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.Store.UpdateProject(r.PathValue("id"), func(p *store.Project) error {
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			p.Name = strings.TrimSpace(*in.Name)
		}
		if in.Description != nil {
			p.Description = *in.Description
		}
		if in.Commands != nil {
			var cmds []store.Command
			for _, c := range *in.Commands {
				c.Name = strings.TrimSpace(strings.ReplaceAll(c.Name, " ", "_"))
				if c.Name != "" && strings.TrimSpace(c.Command) != "" {
					cmds = append(cmds, c)
				}
			}
			p.Commands = cmds
		}
		if in.SkillIDs != nil {
			p.SkillIDs = *in.SkillIDs
		}
		if in.Overrides != nil {
			p.Overrides = *in.Overrides
		}
		if in.Archived != nil {
			p.Archived = *in.Archived
		}
		if in.Knowledge != nil {
			var ks []store.Knowledge
			for _, k := range *in.Knowledge {
				if strings.TrimSpace(k.Text) == "" {
					continue
				}
				if k.ID == "" {
					k.ID, k.At = store.ShortID(), time.Now().UTC()
				}
				ks = append(ks, k)
			}
			p.Knowledge = ks
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"project": s.projectSummary(p), "git": gitInfo(p.Workspace)})
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, t := range s.Store.Tasks() {
		if t.ProjectID == id && s.Engine.Running(t.ID) != "" {
			writeErr(w, errors.New("a task of this project is running"))
			return
		}
	}
	if err := s.Store.DeleteProject(id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// syncProject pulls the latest commit for git projects.
func (s *Server) syncProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Project(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if p.Workspace == "" {
		writeErr(w, errors.New("project has no workspace"))
		return
	}
	if _, err := os.Stat(filepath.Join(p.Workspace, ".git")); err != nil {
		writeErr(w, errors.New("workspace is not a git repository"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.lim().GitTimeoutMin)*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", p.Workspace, "pull", "--ff-only")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	res := map[string]any{"ok": err == nil, "output": strings.TrimSpace(string(out)), "git": gitInfo(p.Workspace)}
	writeJSON(w, 200, res)
}

func (s *Server) projectFiles(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Project(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if p.Workspace == "" {
		writeJSON(w, 200, []codeidx.File{})
		return
	}
	files, _ := codeidx.Scan(p.Workspace, s.lim().CodeScanMaxFiles)
	if files == nil {
		files = []codeidx.File{}
	}
	writeJSON(w, 200, files)
}

func (s *Server) projectFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Project(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	rel := filepath.FromSlash(r.URL.Query().Get("path"))
	full := filepath.Join(p.Workspace, rel)
	root := filepath.Clean(p.Workspace) + string(filepath.Separator)
	if p.Workspace == "" || !strings.HasPrefix(full, root) {
		writeErr(w, errors.New("invalid path"))
		return
	}
	if real, err := filepath.EvalSymlinks(full); err == nil {
		if rr, _ := filepath.EvalSymlinks(p.Workspace); rr != "" && !strings.HasPrefix(real, filepath.Clean(rr)+string(filepath.Separator)) {
			writeErr(w, errors.New("invalid path"))
			return
		}
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		writeErr(w, errors.New("file not found"))
		return
	}
	if kb := s.lim().FileViewMaxKB; st.Size() > int64(kb)<<10 {
		writeErr(w, fmt.Errorf("file larger than %d KB (Settings → Limits → file_view_max_kb)", kb))
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) projectChat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Engine.Chat("project", r.PathValue("id"), in.Text); err != nil {
		writeErr(w, err)
		return
	}
	s.getProject(w, r)
}

func (s *Server) clearProjectChat(w http.ResponseWriter, r *http.Request) {
	_, err := s.Store.UpdateProject(r.PathValue("id"), func(p *store.Project) error { p.Chat = nil; return nil })
	if err != nil {
		writeErr(w, err)
		return
	}
	s.getProject(w, r)
}
