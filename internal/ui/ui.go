// Package ui provides the embedded static assets and HTML template renderer
// for the api-gateway's web interface.
//
// Why embed.FS?
// All template files and the CSS are compiled into the api-gateway binary at
// build time using Go's //go:embed directive. This means:
//   - The distroless Docker image needs no extra volume mounts for UI files.
//   - The binary is fully self-contained — `docker cp` or `kubectl cp` gives
//     you everything you need.
//   - Hot-reloading in local dev is still possible by passing -tags dev and
//     switching to os.DirFS (not implemented here to keep the code simple).
package ui

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed templates/* static/*
var files embed.FS

// StaticHandler returns an http.Handler that serves files from the embedded
// static/ directory at the /static/ URL prefix.
//
// Usage in api-gateway:
//
//	mux.Handle("/static/", ui.StaticHandler())
func StaticHandler() http.Handler {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(fmt.Sprintf("ui: sub static fs: %v", err))
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// templates is the parsed template set. All *.html files in templates/ are
// parsed together so they can reference each other with {{template "name" .}}.
// template.FuncMap provides helper functions used inside the templates.
var templates = template.Must(
	template.New("").
		Funcs(template.FuncMap{
			// divCents converts an integer price in cents to a "$X.XX" string.
			// Used in product and order tables: {{divCents .Price}} → "$19.99"
			"divCents": func(cents int) string {
				return fmt.Sprintf("%.2f", float64(cents)/100)
			},
			// toString converts any value to its string representation.
			// Used in order templates: {{toString .Status}} converts the
			// OrderStatus typed string to a plain string for comparison.
			"string": func(v any) string {
				return fmt.Sprintf("%s", v)
			},
			// slice is a safe substr helper: {{slice .ID 0 8}} → first 8 chars.
			"slice": func(s string, i, j int) string {
				if j > len(s) {
					j = len(s)
				}
				if i > j {
					i = j
				}
				return s[i:j]
			},
		}).
		ParseFS(files, "templates/*.html"),
)

// Render writes the named template to w with the given data.
// It is the single entry point for all template rendering in the api-gateway
// UI handlers, preventing the html/template boilerplate from being repeated.
//
// Usage:
//
//	if err := ui.Render(w, "customers", data); err != nil {
//	    http.Error(w, "render error", 500)
//	}
func Render(w http.ResponseWriter, name string, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return templates.ExecuteTemplate(w, name, data)
}
