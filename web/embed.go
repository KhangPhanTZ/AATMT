// Package web embeds the single-page UI (plain HTML/CSS/JS, no build step).
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

func FS() fs.FS {
	sub, _ := fs.Sub(files, "static")
	return sub
}
