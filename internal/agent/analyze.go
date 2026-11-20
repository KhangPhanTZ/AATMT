package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/store"
)

// Analyze transcribes new screenshots and produces the brief + plan.
func (e *Engine) Analyze(id string) error {
	t, err := e.Store.Task(id)
	if err != nil {
		return err
	}
	if len(t.Messages) == 0 && len(t.Images) == 0 {
		return errors.New("nothing captured yet: add a message or a screenshot")
	}
	return e.start(id, "analyze", "", func(ctx context.Context) error {
		e.setStage(id, store.StageAnalyzing, "reading screenshots")
		if err := e.transcribe(ctx, id); err != nil {
			return err
		}
		e.setStatus(id, "analysing request and code")
		if err := e.analyze(ctx, id); err != nil {
			return err
		}
		if e.Store.Config().Settings.AutoRun {
			go func() {
				// start the run once this job has released the task
				for e.Running(id) != "" {
					sleep(100)
				}
				_ = e.Run(id)
			}()
		}
		return nil
	})
}

// TranscribeOnly re-reads screenshots without re-planning.
func (e *Engine) TranscribeOnly(id string) error {
	return e.start(id, "analyze", "", func(ctx context.Context) error {
		err := e.transcribe(ctx, id)
		e.setStatus(id, "screenshots transcribed")
		return err
	})
}

func (e *Engine) transcribe(ctx context.Context, id string) error {
	t, err := e.Store.Task(id)
	if err != nil {
		return err
	}
	for _, im := range t.Images {
		if strings.TrimSpace(im.Transcript) != "" {
			continue // transcribed once, reused forever
		}
		data, err := os.ReadFile(filepath.Join(e.Store.TaskDir(id), "images", im.File))
		if err != nil {
			return err
		}
		e.setStatus(id, "reading screenshot "+im.Name)
		resp, err := e.call(ctx, callOpts{taskID: id, projectID: t.ProjectID, role: store.RoleVision, vision: true}, llm.Request{
			System:    e.prompt(store.RoleVision, visionPrompt),
			Messages:  []llm.Message{{Role: "user", Text: "Read this image.", Images: []llm.Image{{Mime: im.Mime, Data: data}}}},
			MaxTokens: 4096,
		})
		if err != nil {
			return fmt.Errorf("vision (%s): %w — add a vision-capable profile, or type the screenshot text manually", im.Name, err)
		}
		imID, text, by := im.ID, strings.TrimSpace(resp.Text), resp.Profile.Name
		_, _ = e.Store.UpdateTask(id, func(t *store.Task) error {
			for i := range t.Images {
				if t.Images[i].ID == imID {
					t.Images[i].Transcript, t.Images[i].TranscribedBy = text, by
				}
			}
			return nil
		})
	}
	return nil
}

type analysis struct {
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Requirements  []string `json:"requirements"`
	Acceptance    []string `json:"acceptance_criteria"`
	Questions     []string `json:"open_questions"`
	RelevantFiles []string `json:"relevant_files"`
	Risks         []string `json:"risks"`
	Estimate      string   `json:"estimate"`
	Plan          []struct {
		Title  string   `json:"title"`
		Detail string   `json:"detail"`
		Files  []string `json:"files"`
	} `json:"plan"`
}

func (e *Engine) analyze(ctx context.Context, id string) error {
	t, err := e.Store.Task(id)
	if err != nil {
		return err
	}
	st := e.Store.Config().Settings
	req := llm.Request{
		System:    e.analyzerSystem(st.Language),
		Messages:  []llm.Message{{Role: "user", Text: e.AnalysisPrompt(t)}},
		MaxTokens: 8192,
	}
	e.emit(id, "info", "", "analysis context", fmt.Sprintf("≈%d tokens", llm.EstimateRequest(req)), nil)
	var a analysis
	opts := callOpts{taskID: id, projectID: t.ProjectID, role: store.RoleAnalyzer, override: e.override(t, store.RoleAnalyzer)}
	resp, err := e.call(ctx, opts, req)
	if err != nil {
		return err
	}
	if perr := parseAnalysis(resp.Text, &a); perr != nil {
		// one cheap repair round instead of failing the whole analysis
		req.Messages = append(req.Messages, resp.Assistant(), llm.Message{Role: "user",
			Text: "That was not valid JSON matching the schema (" + perr.Error() + "). Reply again with ONLY the JSON object."})
		resp, err = e.call(ctx, opts, req)
		if err != nil {
			return err
		}
		if perr = parseAnalysis(resp.Text, &a); perr != nil {
			return fmt.Errorf("analyzer returned invalid JSON: %w", perr)
		}
	}
	_, err = e.Store.UpdateTask(id, func(t *store.Task) error {
		if a.Title != "" && (t.Title == "" || strings.HasPrefix(t.Title, "Untitled")) {
			t.Title = a.Title
		}
		var memory []string
		if t.Brief != nil {
			memory = t.Brief.Memory
		}
		t.Brief = &store.Brief{
			Summary: a.Summary, Requirements: a.Requirements, Acceptance: a.Acceptance,
			Questions: a.Questions, RelevantFiles: a.RelevantFiles, Risks: a.Risks,
			Estimate: a.Estimate, Memory: memory,
		}
		// keep finished steps, replace the rest with the new plan
		var steps []store.Step
		for _, s := range t.Steps {
			if s.Status == store.StepDone {
				steps = append(steps, s)
			}
		}
		for _, p := range a.Plan {
			if strings.TrimSpace(p.Title) == "" {
				continue
			}
			steps = append(steps, store.Step{ID: store.ShortID(), Title: p.Title, Detail: p.Detail, Files: p.Files, Status: store.StepPending})
		}
		t.Steps = steps
		for i := range t.Notes {
			t.Notes[i].Consumed = true
		}
		t.Stage, t.Status, t.Error = store.StagePlanned, "plan ready — review and approve", ""
		return nil
	})
	if err == nil {
		e.emit(id, "stage", "", store.StagePlanned, fmt.Sprintf("%d steps planned", len(a.Plan)), nil)
	}
	return err
}

func parseAnalysis(text string, a *analysis) error {
	var lastErr error = errors.New("no JSON object found")
	objs := llm.ExtractJSONObjects(text)
	// prefer the largest object (the model may echo small examples)
	for i := len(objs) - 1; i >= 0; i-- {
		var cand analysis
		if err := json.Unmarshal([]byte(objs[i]), &cand); err != nil {
			lastErr = err
			continue
		}
		if cand.Summary == "" && len(cand.Plan) == 0 {
			lastErr = errors.New("missing summary and plan")
			continue
		}
		*a = cand
		return nil
	}
	return lastErr
}
