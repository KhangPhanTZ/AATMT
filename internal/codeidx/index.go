// Package codeidx gives the agent cheap, token-bounded context about a source
// tree: a compact file tree and keyword-ranked snippets. Instead of dumping the
// whole repository into the prompt, analysis gets only the most relevant parts
// and the worker pulls more on demand with read/search tools.
package codeidx

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var SkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, ".next": true, ".nuxt": true, "__pycache__": true, ".venv": true,
	"venv": true, ".idea": true, ".vscode": true, ".gradle": true, "bin": true, "obj": true,
	"coverage": true, ".cache": true, ".terraform": true, "Pods": true, ".dart_tool": true,
}

var binaryExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ico": true, ".bmp": true,
	".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".7z": true, ".rar": true, ".jar": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".class": true, ".o": true, ".a": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true, ".mp3": true, ".mp4": true, ".mov": true,
	".wasm": true, ".pyc": true, ".db": true, ".sqlite": true, ".lock": false,
}

type File struct {
	Path string // slash-separated, relative to root
	Size int64
}

const maxIndexedSize = 256 * 1024

// Scan lists text-like source files under root (bounded by maxFiles).
func Scan(root string, maxFiles int) ([]File, error) {
	if maxFiles <= 0 {
		maxFiles = 8000
	}
	var out []File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (SkipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && d.Name() != ".github")) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= maxFiles {
			return filepath.SkipAll
		}
		if !d.Type().IsRegular() || binaryExt[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, File{Path: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// Tree renders a compact indented tree, collapsing large directories.
func Tree(files []File, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 300
	}
	type dir struct {
		files []string
		dirs  map[string]*dir
		count int
	}
	root := &dir{dirs: map[string]*dir{}}
	for _, f := range files {
		parts := strings.Split(f.Path, "/")
		d := root
		d.count++
		for _, p := range parts[:len(parts)-1] {
			nd := d.dirs[p]
			if nd == nil {
				nd = &dir{dirs: map[string]*dir{}}
				d.dirs[p] = nd
			}
			d = nd
			d.count++
		}
		d.files = append(d.files, parts[len(parts)-1])
	}
	var b strings.Builder
	lines := 0
	var walk func(d *dir, indent string)
	walk = func(d *dir, indent string) {
		names := make([]string, 0, len(d.dirs))
		for n := range d.dirs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if lines >= maxLines {
				return
			}
			sub := d.dirs[n]
			fmt.Fprintf(&b, "%s%s/ (%d)\n", indent, n, sub.count)
			lines++
			if sub.count > 60 && len(indent) >= 4 {
				continue // collapse big deep dirs
			}
			walk(sub, indent+"  ")
		}
		const perDir = 25
		for i, f := range d.files {
			if lines >= maxLines {
				return
			}
			if i == perDir {
				fmt.Fprintf(&b, "%s… %d more files\n", indent, len(d.files)-perDir)
				lines++
				break
			}
			fmt.Fprintf(&b, "%s%s\n", indent, f)
			lines++
		}
	}
	walk(root, "")
	if lines >= maxLines {
		b.WriteString("… (tree truncated)\n")
	}
	return b.String()
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for with that this from have will your you are not but can all any
	was were been has had into than then them they there their what when where which while who why how
	please need needs should would could make made add fix bug issue task page also just like some more
	các của cho với này những được trong không một và là có để khi thì này đó cần làm thêm sửa lỗi trang
	anh chị em bạn mình giúp nhé nha ạ vui lòng hãy phải nên rồi đang sẽ đã từ theo như vào ra lên xuống`) {
		stop[w] = true
	}
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// Terms extracts search terms from free text: identifiers, camelCase and
// snake_case parts, quoted strings and file-ish tokens.
func Terms(text string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(w string) {
		w = strings.ToLower(strings.TrimSpace(w))
		if len([]rune(w)) < 3 || stop[w] || seen[w] {
			return
		}
		if _, err := fmt.Sscanf(w, "%d", new(int)); err == nil && strings.Trim(w, "0123456789") == "" {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	for _, w := range wordRe.FindAllString(text, -1) {
		add(w)
		for _, p := range splitIdent(w) {
			add(p)
		}
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func splitIdent(w string) []string {
	var parts []string
	for _, seg := range strings.Split(w, "_") {
		start := 0
		rs := []rune(seg)
		for i := 1; i < len(rs); i++ {
			if unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i-1]) {
				parts = append(parts, string(rs[start:i]))
				start = i
			}
		}
		parts = append(parts, string(rs[start:]))
	}
	if len(parts) == 1 {
		return nil
	}
	return parts
}

type Hit struct {
	Path  string
	Score float64
	Lines []int // 0-based matching line numbers
}

// Rank scores files by term matches in path (heavy) and content (light, with
// a bonus for each distinct term) and returns the best ones.
func Rank(root string, files []File, terms []string, limit int) []Hit {
	if len(terms) == 0 {
		return nil
	}
	lterms := make([][]byte, len(terms))
	for i, t := range terms {
		lterms[i] = []byte(t)
	}
	var hits []Hit
	for _, f := range files {
		lp := strings.ToLower(f.Path)
		score := 0.0
		for _, t := range terms {
			if strings.Contains(lp, t) {
				score += 6
			}
		}
		var lines []int
		if f.Size <= maxIndexedSize {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
			if err == nil && !bytes.Contains(data[:min(len(data), 512)], []byte{0}) {
				low := bytes.ToLower(data)
				distinct := 0
				for _, t := range lterms {
					c := bytes.Count(low, t)
					if c > 0 {
						distinct++
						score += float64(min(c, 8)) * 0.5
					}
				}
				score += float64(distinct * distinct)
				if score > 0 {
					for i, ln := range bytes.Split(low, []byte("\n")) {
						for _, t := range lterms {
							if bytes.Contains(ln, t) {
								lines = append(lines, i)
								break
							}
						}
						if len(lines) >= 40 {
							break
						}
					}
				}
			}
		}
		if score > 0 {
			hits = append(hits, Hit{Path: f.Path, Score: score, Lines: lines})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Snippets renders the ranked hits as line-numbered excerpts within budgetChars.
func Snippets(root string, hits []Hit, budgetChars int) string {
	var b strings.Builder
	for _, h := range hits {
		if b.Len() >= budgetChars {
			break
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(h.Path)))
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		perFile := min(budgetChars-b.Len(), max(budgetChars/4, 1500))
		var sb strings.Builder
		fmt.Fprintf(&sb, "### %s (%d lines)\n", h.Path, len(lines))
		if len(h.Lines) == 0 || len(lines) <= 80 {
			for i := 0; i < len(lines) && i < 80; i++ {
				fmt.Fprintf(&sb, "%d| %s\n", i+1, lines[i])
			}
		} else {
			last := -1
			for _, ln := range h.Lines {
				from, to := max(ln-5, last+1), min(ln+6, len(lines))
				if from > last+1 && last >= 0 {
					sb.WriteString("…\n")
				}
				for i := from; i < to; i++ {
					fmt.Fprintf(&sb, "%d| %s\n", i+1, lines[i])
				}
				last = to - 1
				if sb.Len() > perFile {
					break
				}
			}
		}
		s := sb.String()
		if len(s) > perFile {
			s = s[:perFile] + "\n…(truncated)\n"
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}
