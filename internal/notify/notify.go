// Package notify posts short messages to Slack / Discord / Teams / generic
// webhooks when a task needs attention (approval, question, review, error…).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
)

// Kind maps a task event to a notification kind ("" = not notifiable).
func Kind(ev store.Event) string {
	switch ev.Type {
	case "approval":
		if ev.Title == "approval needed" {
			return "approval"
		}
	case "question":
		return "question"
	case "error":
		if ev.Title == "error" {
			return "error"
		}
	case "stage":
		switch ev.Title {
		case store.StageReview:
			return "review"
		case store.StageDone:
			return "done"
		case "paused":
			return "paused"
		}
	}
	return ""
}

var client = &http.Client{Timeout: 10 * time.Second}

// Hook returns a store.OnEvent handler that dispatches matching events.
func Hook(st *store.Store) func(store.Event) {
	return func(ev store.Event) {
		kind := Kind(ev)
		if kind == "" {
			return
		}
		cfg := st.Config()
		var hooks []*store.Webhook
		for _, w := range cfg.Webhooks {
			if w.Enabled && contains(w.Events, kind) {
				hooks = append(hooks, w)
			}
		}
		if len(hooks) == 0 {
			return
		}
		title := ev.TaskID
		if t, err := st.Task(ev.TaskID); err == nil {
			title = t.Title
		}
		link := strings.TrimRight(cfg.Settings.PublicURL, "/") + "/#/task/" + ev.TaskID
		text := fmt.Sprintf("[%s] %s — %s", strings.ToUpper(kind), title, firstLine(ev.Detail, ev.Title))
		for _, w := range hooks {
			go func(w *store.Webhook) {
				if err := Send(context.Background(), w, text, link); err != nil {
					log.Printf("notify %s: %v", w.Name, err)
				}
			}(w)
		}
	}
}

// Send posts one message in the webhook's format.
func Send(ctx context.Context, w *store.Webhook, text, link string) error {
	var body any
	full := text
	if link != "" {
		full += "\n" + link
	}
	switch w.Format {
	case "slack":
		body = map[string]any{"text": full}
	case "discord":
		body = map[string]any{"content": full}
	case "teams":
		body = map[string]any{"@type": "MessageCard", "@context": "https://schema.org/extensions", "summary": text, "text": full}
	default:
		body = map[string]any{"text": text, "url": link, "source": "aatmt"}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func firstLine(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
