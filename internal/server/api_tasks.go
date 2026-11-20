package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/imgutil"
	"github.com/khangphantz/aatmt/internal/store"
)

type taskSummary struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Stage       string      `json:"stage"`
	Priority    string      `json:"priority"`
	Due         string      `json:"due,omitempty"`
	Tags        []string    `json:"tags"`
	Status      string      `json:"status"`
	Error       string      `json:"error,omitempty"`
	Done        int         `json:"done"`
	Total       int         `json:"total"`
	Usage       store.Usage `json:"usage"`
	Turns       int         `json:"turns"`
	Images      int         `json:"images"`
	Messages    int         `json:"messages"`
	Snippet     string      `json:"snippet"`
	ProjectID   string      `json:"project_id"`
	ProjectName string      `json:"project_name"`
	Archived    bool        `json:"archived"`
	Pinned      bool        `json:"pinned"`
	Rotate      bool        `json:"rotate"`
	Pending     bool        `json:"pending"`
	Running     string      `json:"running"`
	ChatBusy    bool        `json:"chat_busy"`
	Changes     int         `json:"changes"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	DoneAt      *time.Time  `json:"done_at,omitempty"`
}

func (s *Server) summary(t *store.Task) taskSummary {
	d, n := t.Progress()
	sum := taskSummary{ID: t.ID, Title: t.Title, Stage: t.Stage, Priority: t.Priority, Due: t.Due, Tags: t.Tags,
		Status: t.Status, Error: t.Error, Done: d, Total: n, Usage: t.Usage, Turns: t.Turns, Images: len(t.Images),
		Messages: len(t.Messages), ProjectID: t.ProjectID, Archived: t.Archived, Pinned: t.Pinned, Rotate: t.Rotate,
		Pending: t.Pending != nil, Running: s.Engine.Running(t.ID), ChatBusy: s.Engine.ChatBusy("task", t.ID),
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, DoneAt: t.DoneAt}
	if sum.Tags == nil {
		sum.Tags = []string{}
	}
	if t.ProjectID != "" {
		if p, err := s.Store.Project(t.ProjectID); err == nil {
			sum.ProjectName = p.Name
		}
	}
	for _, c := range t.Changes {
		if !c.Reverted {
			sum.Changes++
		}
	}
	if t.Brief != nil && t.Brief.Summary != "" {
		sum.Snippet = t.Brief.Summary
	} else if len(t.Messages) > 0 {
		sum.Snippet = t.Messages[0].Text
	} else if len(t.Images) > 0 {
		sum.Snippet = t.Images[0].Transcript
	}
	if n := s.lim().SummaryChars; len([]rune(sum.Snippet)) > n {
		sum.Snippet = string([]rune(sum.Snippet)[:n]) + "…"
	}
	return sum
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	arch, proj := q.Get("archived"), q.Get("project")
	out := []taskSummary{}
	for _, t := range s.Store.Tasks() {
		if proj != "" && t.ProjectID != proj {
			continue
		}
		if (arch == "" || arch == "0") && t.Archived || arch == "1" && !t.Archived {
			continue
		}
		out = append(out, s.summary(t))
	}
	writeJSON(w, 200, out)
}

func (s *Server) taskOut(w http.ResponseWriter, id string) {
	t, err := s.Store.Task(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	// the UI expects arrays, never null
	if t.Messages == nil {
		t.Messages = []store.Message{}
	}
	if t.Images == nil {
		t.Images = []store.Image{}
	}
	if t.Steps == nil {
		t.Steps = []store.Step{}
	}
	if t.Notes == nil {
		t.Notes = []store.Note{}
	}
	if t.Changes == nil {
		t.Changes = []store.Change{}
	}
	if t.Chat == nil {
		t.Chat = []store.ChatMsg{}
	}
	out := map[string]any{"task": t, "summary": s.summary(t), "workspace": s.Store.Workspace(t)}
	if t.ProjectID != "" {
		if p, err := s.Store.Project(t.ProjectID); err == nil {
			out["project"] = map[string]any{"id": p.ID, "name": p.Name, "source": p.Source, "commands": len(p.Commands), "knowledge": len(p.Knowledge)}
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) { s.taskOut(w, r.PathValue("id")) }

// ---------- create ----------

type msgIn struct {
	From string `json:"from"`
	Text string `json:"text"`
}

type imgIn struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data string `json:"data_base64"`
}

type createIn struct {
	Title       string          `json:"title"`
	Priority    string          `json:"priority"`
	Due         string          `json:"due"`
	Tags        []string        `json:"tags"`
	Messages    []msgIn         `json:"messages"`
	Images      []imgIn         `json:"images"`
	ProjectID   string          `json:"project_id"`
	Source      store.Source    `json:"source"`
	ProjectName string          `json:"project_name"`
	TemplateID  string          `json:"template_id"`
	Analyze     *bool           `json:"analyze"`
	Rotate      bool            `json:"rotate"`
	Overrides   store.Overrides `json:"overrides"`
}

func splitTags(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var in createIn
	var files []*multipart.FileHeader
	var zipFile *multipart.FileHeader
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := s.parseMultipart(w, r); err != nil {
			writeErr(w, err)
			return
		}
		f := r.MultipartForm
		in.Title = first(f.Value["title"])
		in.Priority = first(f.Value["priority"])
		in.Due = first(f.Value["due"])
		in.Tags = splitTags(first(f.Value["tags"]))
		if js := first(f.Value["messages"]); js != "" {
			_ = json.Unmarshal([]byte(js), &in.Messages)
		}
		if txt := first(f.Value["message"]); strings.TrimSpace(txt) != "" {
			in.Messages = append(in.Messages, msgIn{From: first(f.Value["from"]), Text: txt})
		}
		in.ProjectID = first(f.Value["project_id"])
		in.ProjectName = first(f.Value["project_name"])
		in.TemplateID = first(f.Value["template_id"])
		in.Source = store.Source{Kind: first(f.Value["source_kind"]), Path: first(f.Value["source_path"]),
			URL: first(f.Value["git_url"]), Ref: first(f.Value["git_ref"])}
		if a := first(f.Value["analyze"]); a != "" {
			b := a == "1" || a == "true"
			in.Analyze = &b
		}
		in.Rotate = first(f.Value["rotate"]) == "1"
		in.Overrides = store.Overrides{Analyzer: first(f.Value["analyzer"]), Worker: first(f.Value["worker"])}
		files = f.File["images"]
		if z := f.File["zip"]; len(z) > 0 {
			zipFile = z[0]
		}
	} else if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}

	cfg := s.Store.Config()
	var tpl *store.Template
	if in.TemplateID != "" {
		for _, x := range cfg.Templates {
			if x.ID == in.TemplateID {
				tpl = x
			}
		}
	}
	t := &store.Task{Title: strings.TrimSpace(in.Title), Priority: in.Priority, Due: in.Due, Tags: in.Tags,
		Overrides: in.Overrides, Rotate: in.Rotate, ProjectID: in.ProjectID}
	now := time.Now().UTC()
	if tpl != nil {
		if t.Title == "" {
			t.Title = tpl.Title
		}
		if t.Priority == "" || t.Priority == "normal" {
			if tpl.Priority != "" {
				t.Priority = tpl.Priority
			}
		}
		if len(t.Tags) == 0 {
			t.Tags = tpl.Tags
		}
		if t.ProjectID == "" {
			t.ProjectID = tpl.ProjectID
		}
		for _, st := range tpl.Steps {
			t.Steps = append(t.Steps, store.Step{ID: store.ShortID(), Title: st, Status: store.StepPending})
		}
	}
	if t.Title == "" {
		t.Title = "Untitled task"
	}
	for _, m := range in.Messages {
		if strings.TrimSpace(m.Text) != "" {
			t.Messages = append(t.Messages, store.Message{ID: store.ShortID(), From: m.From, Text: m.Text, At: now})
		}
	}
	if tpl != nil && tpl.Message != "" && len(t.Messages) == 0 {
		t.Messages = append(t.Messages, store.Message{ID: store.ShortID(), From: "template", Text: tpl.Message, At: now})
	}
	if t.ProjectID != "" {
		if _, err := s.Store.Project(t.ProjectID); err != nil {
			writeErr(w, fmt.Errorf("project: %w", err))
			return
		}
	} else if in.Source.Kind != "" && in.Source.Kind != "none" || zipFile != nil {
		pid, err := s.ensureProject(r.Context(), in.Source, zipFile, in.ProjectName)
		if err != nil {
			writeErr(w, err)
			return
		}
		t.ProjectID = pid
	}
	if len(t.Steps) > 0 {
		t.Stage = store.StagePlanned
	}
	if err := s.Store.CreateTask(t); err != nil {
		writeErr(w, err)
		return
	}
	fail := func(err error) {
		_ = s.Store.DeleteTask(t.ID)
		writeErr(w, err)
	}
	for _, fh := range files {
		if _, err := s.saveUploadedImage(t.ID, fh); err != nil {
			fail(err)
			return
		}
	}
	for _, im := range in.Images {
		data, err := base64.StdEncoding.DecodeString(im.Data)
		if err != nil {
			fail(fmt.Errorf("image %s: %w", im.Name, err))
			return
		}
		if _, err := s.saveImage(t.ID, im.Name, im.Mime, data); err != nil {
			fail(err)
			return
		}
	}
	s.Store.Emit(store.Event{TaskID: t.ID, Type: "stage", Title: store.StageInbox, Detail: "task captured"})
	log.Printf("task %s captured: %s", t.ID, t.Title)
	analyze := cfg.Settings.AutoAnalyze
	if in.Analyze != nil {
		analyze = *in.Analyze
	}
	if analyze && len(cfg.Profiles) > 0 {
		_ = s.Engine.Analyze(t.ID)
	}
	s.taskOut(w, t.ID)
}

// ensureProject reuses the project that already owns a local folder, or
// creates a new project for the source.
func (s *Server) ensureProject(ctx context.Context, src store.Source, zipFile *multipart.FileHeader, name string) (string, error) {
	if src.Kind == "path" && zipFile == nil {
		abs, _ := filepath.Abs(strings.TrimSpace(src.Path))
		for _, p := range s.Store.Projects() {
			if p.Workspace == abs {
				return p.ID, nil
			}
		}
	}
	if src.Kind == "git" {
		for _, p := range s.Store.Projects() {
			if p.Source.Kind == "git" && p.Source.URL == strings.TrimSpace(src.URL) && p.Source.Ref == src.Ref {
				return p.ID, nil
			}
		}
	}
	p := &store.Project{ID: store.NewID()}
	dir := s.Store.ProjectDir(p.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	got, ws, err := prepareSource(ctx, dir, src, zipFile, s.lim())
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	p.Source, p.Workspace = got, ws
	p.Name = strings.TrimSpace(name)
	if p.Name == "" {
		p.Name = defaultProjectName(got, ws)
	}
	if err := s.Store.CreateProject(p); err != nil {
		return "", err
	}
	log.Printf("project %s created (%s: %s)", p.Name, got.Kind, ws)
	return p.ID, nil
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// ---------- images ----------

func (s *Server) saveUploadedImage(id string, fh *multipart.FileHeader) (*store.Image, error) {
	l := s.lim()
	if fh.Size > int64(l.MaxImageMB)<<20 {
		return nil, fmt.Errorf("image %s is larger than %d MB (Settings → Limits → max_image_mb)", fh.Filename, l.MaxImageMB)
	}
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(l.MaxImageMB)<<20+1))
	if err != nil {
		return nil, err
	}
	return s.saveImage(id, fh.Filename, fh.Header.Get("Content-Type"), data)
}

func (s *Server) saveImage(id, name, mime string, data []byte) (*store.Image, error) {
	l := s.lim()
	if len(data) > l.MaxImageMB<<20 {
		return nil, fmt.Errorf("image larger than %d MB (Settings → Limits → max_image_mb)", l.MaxImageMB)
	}
	if mime == "" || mime == "application/octet-stream" {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		return nil, fmt.Errorf("%s is not an image (%s)", name, mime)
	}
	data, mime, wd, ht := imgutil.Normalize(data, mime, l.ImageMaxEdge, l.ImageJPEGQuality)
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[mime]
	im := store.Image{ID: store.ShortID(), Name: name, Mime: mime, Size: int64(len(data)), Width: wd, Height: ht}
	if im.Name == "" {
		im.Name = "screenshot" + ext
	}
	im.File = im.ID + ext
	dir := filepath.Join(s.Store.TaskDir(id), "images")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, im.File), data, 0o600); err != nil {
		return nil, err
	}
	_, err := s.Store.UpdateTask(id, func(t *store.Task) error { t.Images = append(t.Images, im); return nil })
	return &im, err
}

func (s *Server) addImages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.parseMultipart(w, r); err != nil {
		writeErr(w, err)
		return
	}
	for _, fh := range r.MultipartForm.File["images"] {
		if _, err := s.saveUploadedImage(id, fh); err != nil {
			writeErr(w, err)
			return
		}
	}
	s.Store.Emit(store.Event{TaskID: id, Type: "info", Title: "screenshot added"})
	s.taskOut(w, id)
}

func (s *Server) findImage(id, img string) (*store.Image, error) {
	t, err := s.Store.Task(id)
	if err != nil {
		return nil, err
	}
	for _, im := range t.Images {
		if im.ID == img {
			return &im, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	im, err := s.findImage(r.PathValue("id"), r.PathValue("img"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", im.Mime)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, filepath.Join(s.Store.TaskDir(r.PathValue("id")), "images", im.File))
}

func (s *Server) putImage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Transcript string `json:"transcript"`
	}
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	id, img := r.PathValue("id"), r.PathValue("img")
	_, err := s.Store.UpdateTask(id, func(t *store.Task) error {
		for i := range t.Images {
			if t.Images[i].ID == img {
				t.Images[i].Transcript = in.Transcript
				t.Images[i].TranscribedBy = "edited by user"
				return nil
			}
		}
		return store.ErrNotFound
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.taskOut(w, id)
}

func (s *Server) deleteImage(w http.ResponseWriter, r *http.Request) {
	id, img := r.PathValue("id"), r.PathValue("img")
	var file string
	_, err := s.Store.UpdateTask(id, func(t *store.Task) error {
		for i, im := range t.Images {
			if im.ID == img {
				file = im.File
				t.Images = append(t.Images[:i], t.Images[i+1:]...)
				return nil
			}
		}
		return store.ErrNotFound
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	_ = os.Remove(filepath.Join(s.Store.TaskDir(id), "images", file))
	s.taskOut(w, id)
}

// ---------- messages ----------

func (s *Server) addMessage(w http.ResponseWriter, r *http.Request) {
	var in msgIn
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		writeErr(w, errors.New("empty message"))
		return
	}
	id := r.PathValue("id")
	_, err := s.Store.UpdateTask(id, func(t *store.Task) error {
		t.Messages = append(t.Messages, store.Message{ID: store.ShortID(), From: in.From, Text: in.Text, At: time.Now().UTC()})
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Emit(store.Event{TaskID: id, Type: "info", Title: "message added", Detail: in.Text})
	s.taskOut(w, id)
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	id, mid := r.PathValue("id"), r.PathValue("mid")
	_, err := s.Store.UpdateTask(id, func(t *store.Task) error {
		for i, m := range t.Messages {
			if m.ID == mid {
				t.Messages = append(t.Messages[:i], t.Messages[i+1:]...)
				return nil
			}
		}
		return store.ErrNotFound
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.taskOut(w, id)
}

// setSource attaches a task to an existing project or to a new source.
func (s *Server) setSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.Engine.Running(id) != "" {
		writeErr(w, errors.New("pause the task before changing its project"))
		return
	}
	var src store.Source
	var zipFile *multipart.FileHeader
	projectID, name := "", ""
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := s.parseMultipart(w, r); err != nil {
			writeErr(w, err)
			return
		}
		f := r.MultipartForm
		projectID, name = first(f.Value["project_id"]), first(f.Value["project_name"])
		src = store.Source{Kind: first(f.Value["source_kind"]), Path: first(f.Value["source_path"]),
			URL: first(f.Value["git_url"]), Ref: first(f.Value["git_ref"])}
		if z := f.File["zip"]; len(z) > 0 {
			zipFile = z[0]
		}
	} else {
		var in struct {
			ProjectID string       `json:"project_id"`
			Source    store.Source `json:"source"`
		}
		if err := s.readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		projectID, src = in.ProjectID, in.Source
	}
	if projectID == "" && (src.Kind != "" && src.Kind != "none" || zipFile != nil) {
		pid, err := s.ensureProject(r.Context(), src, zipFile, name)
		if err != nil {
			writeErr(w, err)
			return
		}
		projectID = pid
	}
	if projectID != "" {
		if _, err := s.Store.Project(projectID); err != nil {
			writeErr(w, err)
			return
		}
	}
	_, err := s.Store.UpdateTask(id, func(t *store.Task) error {
		t.ProjectID, t.Workspace, t.Source = projectID, "", store.Source{Kind: "none"}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Emit(store.Event{TaskID: id, Type: "info", Title: "project changed", Detail: projectID})
	s.taskOut(w, id)
}

// ---------- edit ----------

type stepIn struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Files  []string `json:"files"`
	Status string   `json:"status"`
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Title     *string          `json:"title"`
		Priority  *string          `json:"priority"`
		Due       *string          `json:"due"`
		Tags      *[]string        `json:"tags"`
		Stage     *string          `json:"stage"`
		Archived  *bool            `json:"archived"`
		Pinned    *bool            `json:"pinned"`
		Rotate    *bool            `json:"rotate"`
		Brief     *store.Brief     `json:"brief"`
		Steps     *[]stepIn        `json:"steps"`
		Overrides *store.Overrides `json:"overrides"`
	}
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	running := s.Engine.Running(id)
	if in.Stage != nil && *in.Stage == store.StageDone {
		if err := s.Engine.Complete(id); err != nil {
			writeErr(w, err)
			return
		}
		in.Stage = nil
	}
	if in.Archived != nil && *in.Archived && running != "" {
		writeErr(w, errors.New("pause the task before archiving it"))
		return
	}
	_, err := s.Store.UpdateTask(id, func(t *store.Task) error {
		if in.Title != nil {
			t.Title = strings.TrimSpace(*in.Title)
		}
		if in.Priority != nil {
			t.Priority = *in.Priority
		}
		if in.Due != nil {
			t.Due = *in.Due
		}
		if in.Tags != nil {
			t.Tags = *in.Tags
		}
		if in.Archived != nil {
			t.Archived = *in.Archived
		}
		if in.Pinned != nil {
			t.Pinned = *in.Pinned
		}
		if in.Rotate != nil {
			t.Rotate = *in.Rotate
		}
		if in.Overrides != nil {
			t.Overrides = *in.Overrides
		}
		if in.Brief != nil {
			mem := []string(nil)
			if t.Brief != nil {
				mem = t.Brief.Memory
			}
			b := *in.Brief
			if b.Memory == nil {
				b.Memory = mem
			}
			t.Brief = &b
		}
		if in.Steps != nil {
			if running == "run" {
				return errors.New("pause the task before editing the plan")
			}
			old := map[string]store.Step{}
			for _, st := range t.Steps {
				old[st.ID] = st
			}
			var steps []store.Step
			for _, si := range *in.Steps {
				if strings.TrimSpace(si.Title) == "" {
					continue
				}
				st, ok := old[si.ID]
				if !ok {
					st = store.Step{ID: store.ShortID(), Status: store.StepPending}
				}
				st.Title, st.Detail, st.Files = si.Title, si.Detail, si.Files
				switch si.Status {
				case store.StepPending, store.StepDone, store.StepSkipped:
					st.Status = si.Status
				}
				steps = append(steps, st)
			}
			t.Steps = steps
			if t.Stage == store.StageInbox && len(steps) > 0 {
				t.Stage = store.StagePlanned
			}
		}
		if in.Stage != nil && *in.Stage != t.Stage {
			if running != "" {
				return errors.New("pause the task before moving it")
			}
			ok := false
			for _, st := range store.Stages {
				if st == *in.Stage && st != store.StageRunning && st != store.StageAnalyzing {
					ok = true
				}
			}
			if !ok {
				return errors.New("cannot move a task to that stage manually")
			}
			t.Stage, t.Status = *in.Stage, "moved manually"
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if in.Stage != nil {
		s.Store.Emit(store.Event{TaskID: id, Type: "stage", Title: *in.Stage, Detail: "moved manually"})
	}
	if in.Archived != nil {
		s.Store.Emit(store.Event{TaskID: id, Type: "info", Title: map[bool]string{true: "archived", false: "restored from archive"}[*in.Archived]})
	}
	s.taskOut(w, id)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.Engine.Pause(id)
	if err := s.Store.DeleteTask(id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// duplicate copies the captured input (messages, screenshots + transcripts,
// project, tags) into a fresh task — handy for recurring requests.
func (s *Server) duplicate(w http.ResponseWriter, r *http.Request) {
	src, err := s.Store.Task(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	t := &store.Task{Title: src.Title + " (copy)", Priority: src.Priority, Tags: src.Tags, ProjectID: src.ProjectID,
		Messages: src.Messages, Overrides: src.Overrides, Rotate: src.Rotate, Workspace: src.Workspace, Source: src.Source}
	if err := s.Store.CreateTask(t); err != nil {
		writeErr(w, err)
		return
	}
	for _, im := range src.Images {
		data, err := os.ReadFile(filepath.Join(s.Store.TaskDir(src.ID), "images", im.File))
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(s.Store.TaskDir(t.ID), "images", im.File), data, 0o600)
	}
	_, _ = s.Store.UpdateTask(t.ID, func(x *store.Task) error { x.Images = src.Images; return nil })
	s.Store.Emit(store.Event{TaskID: t.ID, Type: "stage", Title: store.StageInbox, Detail: "duplicated from " + src.ID})
	s.taskOut(w, t.ID)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Task(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	md := s.Engine.Report(t)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="task-%s.md"`, t.ID))
	}
	_, _ = io.WriteString(w, md)
}

func (s *Server) saveTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Task(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	_ = s.readJSON(r, &in)
	tpl := &store.Template{ID: store.ShortID(), Name: strings.TrimSpace(in.Name), Title: t.Title, Priority: t.Priority,
		Tags: t.Tags, ProjectID: t.ProjectID}
	if tpl.Name == "" {
		tpl.Name = t.Title
	}
	if len(t.Messages) > 0 {
		tpl.Message = t.Messages[0].Text
	}
	for _, st := range t.Steps {
		tpl.Steps = append(tpl.Steps, st.Title)
	}
	err = s.Store.UpdateConfig(func(c *store.Config) error { c.Templates = append(c.Templates, tpl); return nil })
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, tpl)
}

// ---------- actions ----------

func (s *Server) estimate(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Task(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, s.Engine.EstimateAnalysis(t))
}

func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.Analyze(id); err != nil {
		writeErr(w, err)
		return
	}
	s.taskOut(w, id)
}

func (s *Server) transcribe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.TranscribeOnly(id); err != nil {
		writeErr(w, err)
		return
	}
	s.taskOut(w, id)
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.Store.Task(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	pending := false
	for _, st := range t.Steps {
		if st.Status == store.StepPending || st.Status == store.StepRunning {
			pending = true
		}
	}
	if !pending {
		writeErr(w, errors.New("no pending steps: add a step or request changes"))
		return
	}
	if err := s.Engine.Run(id); err != nil {
		writeErr(w, err)
		return
	}
	s.taskOut(w, id)
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.Engine.Pause(id)
	s.taskOut(w, id)
}

// note adds operator feedback. rework=true appends a new "address feedback"
// step (for tasks in review/done); run=true resumes execution.
func (s *Server) note(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Text   string `json:"text"`
		Rework bool   `json:"rework"`
		Run    bool   `json:"run"`
	}
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" {
		writeErr(w, errors.New("empty note"))
		return
	}
	_, err := s.Store.UpdateTask(id, func(t *store.Task) error {
		t.Notes = append(t.Notes, store.Note{ID: store.ShortID(), Text: in.Text, At: time.Now().UTC()})
		if in.Rework {
			title := strings.TrimSpace(strings.SplitN(in.Text, "\n", 2)[0])
			if n := s.lim().SummaryChars; len([]rune(title)) > n {
				title = string([]rune(title)[:n]) + "…"
			}
			t.Steps = append(t.Steps, store.Step{ID: store.ShortID(), Title: "Address feedback: " + title,
				Detail: "Reviewer feedback:\n" + in.Text, Status: store.StepPending})
			if t.Stage == store.StageDone {
				t.Stage = store.StageReview
			}
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Emit(store.Event{TaskID: id, Type: "note", Title: "note from you", Detail: in.Text})
	if in.Run && s.Engine.Running(id) == "" {
		if err := s.Engine.Run(id); err != nil {
			writeErr(w, err)
			return
		}
	}
	s.taskOut(w, id)
}

func (s *Server) approval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		ID      string `json:"id"`
		Approve bool   `json:"approve"`
		Always  bool   `json:"always"`
	}
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Engine.Decide(id, in.ID, in.Approve, in.Always); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) taskChat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := s.readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Engine.Chat("task", r.PathValue("id"), in.Text); err != nil {
		writeErr(w, err)
		return
	}
	s.taskOut(w, r.PathValue("id"))
}

func (s *Server) promote(w http.ResponseWriter, r *http.Request) {
	n, err := s.Engine.PromoteMemory(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"promoted": n})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if max := s.lim().ListMaxItems; limit <= 0 || limit > max {
		limit = max
	}
	evs, err := s.Store.Events(r.PathValue("id"), after, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, evs)
}

func (s *Server) taskLLM(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Store.LLMRecords(r.PathValue("id"), s.lim().ListMaxItems))
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Task(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	type fileDiff struct {
		Path string `json:"path"`
		Op   string `json:"op"`
		Step string `json:"step"`
		Diff string `json:"diff"`
	}
	only := r.URL.Query().Get("path")
	out := []fileDiff{}
	for _, c := range t.Changes {
		if c.Reverted || (only != "" && c.Path != only) {
			continue
		}
		d, err := s.Engine.FileDiff(t, c.Path)
		if err != nil {
			d = "error: " + err.Error()
		}
		if n := s.lim().DiffMaxChars; len(d) > n {
			d = d[:n] + "\n…(diff truncated — Settings → Limits → diff_max_chars)"
		}
		out = append(out, fileDiff{Path: c.Path, Op: c.Op, Step: c.Step, Diff: d})
	}
	writeJSON(w, 200, out)
}

func (s *Server) revert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Path string `json:"path"`
	}
	_ = s.readJSON(r, &in)
	n, err := s.Engine.Revert(id, in.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"reverted": n})
}
