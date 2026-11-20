// Package logbuf keeps the last N server log lines in memory for the Logs page.
package logbuf

import (
	"strings"
	"sync"
	"time"
)

type Line struct {
	At    time.Time `json:"at"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

type Buffer struct {
	mu    sync.Mutex
	lines []Line
	max   int
}

func New(max int) *Buffer { return &Buffer{max: max} }

// Write implements io.Writer so it can be plugged into the standard logger.
func (b *Buffer) Write(p []byte) (int, error) {
	for _, ln := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if ln == "" {
			continue
		}
		level := "info"
		low := strings.ToLower(ln)
		switch {
		case strings.Contains(low, "error") || strings.Contains(low, "fail") || strings.Contains(low, "panic"):
			level = "error"
		case strings.Contains(low, "warn"):
			level = "warn"
		}
		b.mu.Lock()
		b.lines = append(b.lines, Line{At: time.Now().UTC(), Level: level, Text: ln})
		if len(b.lines) > b.max {
			b.lines = b.lines[len(b.lines)-b.max:]
		}
		b.mu.Unlock()
	}
	return len(p), nil
}

func (b *Buffer) Lines() []Line {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Line, len(b.lines))
	copy(out, b.lines)
	return out
}

// SetMax changes how many lines are kept (Settings → Limits → log_lines).
func (b *Buffer) SetMax(n int) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.max = n
	if len(b.lines) > n {
		b.lines = b.lines[len(b.lines)-n:]
	}
}
