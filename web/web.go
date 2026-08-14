// Package web embeds the server-rendered templates and static assets
// (AGENTS.md §9: no separate frontend build step).
package web

import (
	"embed"
	"io/fs"
)

//go:embed templates/*.html static/*
var content embed.FS

// Templates returns the html/template sources (templates/*.html).
func Templates() (fs.FS, error) {
	return fs.Sub(content, "templates")
}

// Static returns the static assets (CSS/JS, served at /static/).
func Static() (fs.FS, error) {
	return fs.Sub(content, "static")
}
