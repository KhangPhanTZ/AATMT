package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khangphantz/aatmt/internal/agent"
	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool := llm.NewPool()
	s := &Server{Store: st, Engine: agent.New(st, pool), Pool: pool, Local: true}
	return httptest.NewServer(s.Handler()), st
}

func call(t *testing.T, srv *httptest.Server, method, path string, body any, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AATMT", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func TestGuards(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	res, _ := http.Post(srv.URL+"/api/tasks", "application/json", strings.NewReader(`{}`))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("write without X-AATMT header: %d", res.StatusCode)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/tasks", strings.NewReader(`{}`))
	req.Header.Set("X-AATMT", "1")
	req.Header.Set("Origin", "http://evil.example")
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin write: %d", res.StatusCode)
	}
}

func TestProjectsTasksHistoryFlow(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644)

	var pr struct {
		Project struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"project"`
	}
	if code := call(t, srv, "POST", "/api/projects", map[string]any{"source": map[string]any{"kind": "path", "path": ws}}, &pr); code != 200 || pr.Project.ID == "" {
		t.Fatalf("create project: %d %+v", code, pr)
	}
	// two tasks on the same codebase; a second "path" source reuses the project
	for i := 0; i < 2; i++ {
		var tr struct {
			Task store.Task `json:"task"`
		}
		body := map[string]any{"messages": []map[string]string{{"text": "do something"}}, "source": map[string]any{"kind": "path", "path": ws}, "analyze": false}
		if code := call(t, srv, "POST", "/api/tasks", body, &tr); code != 200 || tr.Task.ProjectID != pr.Project.ID {
			t.Fatalf("task %d: %d project=%q want %q", i, code, tr.Task.ProjectID, pr.Project.ID)
		}
		if i == 0 {
			call(t, srv, "PATCH", "/api/tasks/"+tr.Task.ID, map[string]any{"archived": true}, nil)
		}
	}
	var cur, all []map[string]any
	call(t, srv, "GET", "/api/tasks", nil, &cur)
	call(t, srv, "GET", "/api/tasks?archived=all&project="+pr.Project.ID, nil, &all)
	if len(cur) != 1 || len(all) != 2 {
		t.Fatalf("current=%d all=%d", len(cur), len(all))
	}
	var proj struct {
		Project struct {
			Tasks int `json:"tasks"`
		} `json:"project"`
	}
	call(t, srv, "GET", "/api/projects/"+pr.Project.ID, nil, &proj)
	if proj.Project.Tasks != 2 {
		t.Fatalf("project task count %d", proj.Project.Tasks)
	}
	var files []map[string]any
	call(t, srv, "GET", "/api/projects/"+pr.Project.ID+"/files", nil, &files)
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	if code := call(t, srv, "GET", "/api/projects/"+pr.Project.ID+"/file?path=../../etc/passwd", nil, nil); code == 200 {
		t.Fatal("path traversal must be refused")
	}
	// rules validation + dashboard + search
	if code := call(t, srv, "PUT", "/api/rules", []map[string]any{{"kind": "regex", "pattern": "(", "action": "deny"}}, nil); code == 200 {
		t.Fatal("invalid regex accepted")
	}
	var dash map[string]any
	if code := call(t, srv, "GET", "/api/dashboard", nil, &dash); code != 200 || dash["projects"].(float64) != 1 {
		t.Fatalf("dashboard %d %v", code, dash["projects"])
	}
	var hits []map[string]any
	call(t, srv, "GET", "/api/search?q=something", nil, &hits)
	if len(hits) != 2 {
		t.Fatalf("search hits %d", len(hits))
	}
	_ = st
}
