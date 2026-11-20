package server

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
)

const (
	maxImageBytes = 20 << 20
	maxZipBytes   = 200 << 20
	maxUnzipBytes = 500 << 20
	maxUnzipFiles = 20000
)

// prepareSource materialises a source into a workspace. Git and zip sources
// are placed under dir/workspace; local folders are used in place.
func prepareSource(ctx context.Context, dir string, src store.Source, zipFile *multipart.FileHeader) (store.Source, string, error) {
	if zipFile != nil {
		src.Kind = "upload"
	}
	ws := filepath.Join(dir, "workspace")
	switch src.Kind {
	case "", "none":
		return store.Source{Kind: "none"}, "", nil
	case "path":
		p := strings.TrimSpace(src.Path)
		if strings.HasPrefix(p, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, strings.TrimPrefix(p, "~"))
			}
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return src, "", err
		}
		st, err := os.Stat(abs)
		if err != nil || !st.IsDir() {
			return src, "", fmt.Errorf("source folder not found: %s", abs)
		}
		src.Path = abs
		return src, abs, nil
	case "git":
		u := strings.TrimSpace(src.URL)
		if u == "" || strings.HasPrefix(u, "-") {
			return src, "", errors.New("invalid git URL")
		}
		if _, err := exec.LookPath("git"); err != nil {
			return src, "", errors.New("git is not installed on this machine")
		}
		_ = os.RemoveAll(ws)
		args := []string{"clone", "--depth", "1"}
		if ref := strings.TrimSpace(src.Ref); ref != "" {
			if strings.HasPrefix(ref, "-") {
				return src, "", errors.New("invalid git ref")
			}
			args = append(args, "--branch", ref)
		}
		args = append(args, "--", u, ws)
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return src, "", fmt.Errorf("git clone failed: %s", strings.TrimSpace(string(out)))
		}
		src.URL = u
		return src, ws, nil
	case "upload":
		if zipFile == nil {
			return src, "", errors.New("no zip file uploaded")
		}
		_ = os.RemoveAll(ws)
		if err := unzip(zipFile, ws); err != nil {
			return src, "", err
		}
		root := ws
		// a zip with a single top folder: use that folder as the root
		if ents, err := os.ReadDir(ws); err == nil && len(ents) == 1 && ents[0].IsDir() {
			root = filepath.Join(ws, ents[0].Name())
		}
		src.Path = zipFile.Filename
		return src, root, nil
	}
	return src, "", fmt.Errorf("unknown source kind %q", src.Kind)
}

// defaultProjectName derives a readable name from a source.
func defaultProjectName(src store.Source, ws string) string {
	switch src.Kind {
	case "git":
		n := strings.TrimSuffix(filepath.Base(strings.TrimRight(src.URL, "/")), ".git")
		if n != "" && n != "." {
			return n
		}
	case "upload":
		return strings.TrimSuffix(filepath.Base(src.Path), ".zip")
	}
	if ws != "" {
		return filepath.Base(ws)
	}
	return "Untitled project"
}

func gitInfo(ws string) map[string]any {
	if ws == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		return nil
	}
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", ws}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	status := run("status", "--porcelain")
	dirty := 0
	if status != "" {
		dirty = len(strings.Split(status, "\n"))
	}
	return map[string]any{
		"branch": run("rev-parse", "--abbrev-ref", "HEAD"),
		"head":   run("log", "-1", "--format=%h %s"),
		"dirty":  dirty,
	}
}

func unzip(fh *multipart.FileHeader, dst string) error {
	if fh.Size > maxZipBytes {
		return errors.New("zip larger than 200 MB")
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	ra, ok := f.(io.ReaderAt)
	if !ok {
		return errors.New("cannot read zip")
	}
	zr, err := zip.NewReader(ra, fh.Size)
	if err != nil {
		return fmt.Errorf("invalid zip: %w", err)
	}
	if len(zr.File) > maxUnzipFiles {
		return errors.New("zip has too many files")
	}
	var total int64
	root := filepath.Clean(dst) + string(filepath.Separator)
	for _, zf := range zr.File {
		p := filepath.Join(dst, filepath.FromSlash(zf.Name))
		if !strings.HasPrefix(p, root) {
			return fmt.Errorf("unsafe path in zip: %s", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if !zf.Mode().IsRegular() {
			continue // skip symlinks and devices
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		n, err := io.Copy(out, io.LimitReader(rc, maxUnzipBytes-total+1))
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
		total += n
		if total > maxUnzipBytes {
			return errors.New("zip expands beyond 500 MB")
		}
	}
	return nil
}
