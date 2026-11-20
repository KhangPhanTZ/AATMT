package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/khangphantz/aatmt/internal/store"
)

// FileDiff returns a unified diff between the original (backup) and the
// current workspace version of a changed file.
func (e *Engine) FileDiff(t *store.Task, path string) (string, error) {
	for _, c := range t.Changes {
		if c.Path != path || c.Reverted {
			continue
		}
		var before, after string
		if c.Backup != "" {
			b, err := os.ReadFile(filepath.Join(e.Store.TaskDir(t.ID), "backups", c.Backup))
			if err != nil {
				return "", err
			}
			before = string(b)
		}
		if b, err := os.ReadFile(filepath.Join(e.Store.Workspace(t), filepath.FromSlash(c.Path))); err == nil {
			after = string(b)
		}
		return UnifiedDiff(before, after, c.Path, diffContext, e.lim().DiffMaxCells), nil
	}
	return "", errors.New("no change recorded for this path")
}

// Revert restores one path (or every path when path == "") to its original.
func (e *Engine) Revert(id, path string) (int, error) {
	if e.Running(id) != "" {
		return 0, errors.New("pause the task before reverting")
	}
	n := 0
	cur, err := e.Store.Task(id)
	if err != nil {
		return 0, err
	}
	ws := e.Store.Workspace(cur)
	if ws == "" {
		return 0, errors.New("task has no workspace")
	}
	_, err = e.Store.UpdateTask(id, func(t *store.Task) error {
		for i, c := range t.Changes {
			if c.Reverted || (path != "" && c.Path != path) {
				continue
			}
			full := filepath.Join(ws, filepath.FromSlash(c.Path))
			if !strings.HasPrefix(full, filepath.Clean(ws)+string(filepath.Separator)) {
				continue
			}
			if c.Backup == "" {
				_ = os.Remove(full)
			} else {
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return err
				}
				if err := copyFile(filepath.Join(e.Store.TaskDir(id), "backups", c.Backup), full); err != nil {
					return fmt.Errorf("restore %s: %w", c.Path, err)
				}
			}
			t.Changes[i].Reverted = true
			n++
		}
		return nil
	})
	if err == nil && n > 0 {
		e.emit(id, "change", "", fmt.Sprintf("reverted %d file(s)", n), path, nil)
	}
	return n, err
}

// UnifiedDiff produces a compact unified diff (LCS on lines, common
// prefix/suffix trimmed first so large files with small edits stay cheap).
// diffContext is the unified-diff convention (same as `diff -u` / git).
const diffContext = 3

func UnifiedDiff(a, b, name string, ctx, maxCells int) string {
	if a == b {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	pre := 0
	for pre < len(al) && pre < len(bl) && al[pre] == bl[pre] {
		pre++
	}
	suf := 0
	for suf < len(al)-pre && suf < len(bl)-pre && al[len(al)-1-suf] == bl[len(bl)-1-suf] {
		suf++
	}
	am, bm := al[pre:len(al)-suf], bl[pre:len(bl)-suf]

	type op struct {
		kind byte // ' ', '-', '+'
		text string
		ai   int
		bi   int
	}
	var ops []op
	for i := 0; i < pre; i++ {
		ops = append(ops, op{' ', al[i], i, i})
	}
	if len(am)*len(bm) > maxCells {
		for i, l := range am {
			ops = append(ops, op{'-', l, pre + i, -1})
		}
		for i, l := range bm {
			ops = append(ops, op{'+', l, -1, pre + i})
		}
	} else {
		n, m := len(am), len(bm)
		lcs := make([][]int32, n+1)
		for i := range lcs {
			lcs[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if am[i] == bm[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else if lcs[i+1][j] >= lcs[i][j+1] {
					lcs[i][j] = lcs[i+1][j]
				} else {
					lcs[i][j] = lcs[i][j+1]
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && am[i] == bm[j]:
				ops = append(ops, op{' ', am[i], pre + i, pre + j})
				i++
				j++
			case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
				ops = append(ops, op{'-', am[i], pre + i, -1}) // deletions before insertions
				i++
			default:
				ops = append(ops, op{'+', bm[j], -1, pre + j})
				j++
			}
		}
	}
	for k := 0; k < suf; k++ {
		ops = append(ops, op{' ', al[len(al)-suf+k], len(al) - suf + k, len(bl) - suf + k})
	}

	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", name, name)
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(k-ctx, 0)
		end := k
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run-end > 2*ctx || run == len(ops) {
				end = min(end+ctx, len(ops))
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := -1, -1, 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				if aStart < 0 {
					aStart = o.ai
				}
				aLen++
			}
			if o.kind != '-' {
				if bStart < 0 {
					bStart = o.bi
				}
				bLen++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart+1, aLen, bStart+1, bLen)
		for _, o := range ops[start:end] {
			out.WriteByte(o.kind)
			out.WriteString(o.text)
			out.WriteByte('\n')
		}
		k = end
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	return strings.Split(s, "\n")
}
