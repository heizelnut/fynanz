package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"fynanz/internal/sync"
	"fynanz/web"
)

// webStatic serves the embedded static assets at /static/.
var webStatic fs.FS = mustStatic()

func mustStatic() fs.FS {
	fsys, err := web.Static()
	if err != nil {
		log.Fatalf("static: %v", err)
	}
	return fsys
}

// pageFiles are the full-page templates. Each page is parsed as its own set
// (base + partials + page) so every page's {{define "content"}} is
// unambiguous — parsing all pages in one set would make the last definition
// win for every request.
var pageFiles = []string{"index.html", "transactions.html", "inbox.html", "categories.html", "settings.html", "terms.html", "privacy.html"}

// loadTemplates parses one template set per page from the embedded FS.
func (h *handlers) loadTemplates() error {
	fsys, err := web.Templates()
	if err != nil {
		return err
	}
	funcs := template.FuncMap{
		"fmtAmount":     fmtAmount,
		"fmtDate":       fmtDate,
		"accountName":   h.accountName,
		"eventsSummary": eventsSummary,
		"jsJSON": func(v string) template.JS {
			// Emit JSON verbatim inside <script type="application/json">
			// (html/template would otherwise escape it as a JS string).
			return template.JS(v)
		},
	}
	h.pages = make(map[string]*template.Template, len(pageFiles))
	for _, page := range pageFiles {
		tmpl, err := template.New("").Funcs(funcs).ParseFS(fsys, "base.html", "partials.html", page)
		if err != nil {
			return err
		}
		h.pages[page] = tmpl
	}
	return nil
}

// render executes the full page: base layout + that page's content block.
func (h *handlers) render(w http.ResponseWriter, page string, d any) {
	h.renderPage(w, "base.html", page, d)
}

// renderPartial executes a named partial from a page's set (HTMX responses).
func (h *handlers) renderPartial(w http.ResponseWriter, page, partial string, d any) {
	h.renderPage(w, partial, page, d)
}

func (h *handlers) renderPage(w http.ResponseWriter, name, page string, d any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl := h.pages[page]
	if tmpl == nil {
		http.Error(w, "unknown page: "+page, http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, name, d); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// categoryName resolves a category id to its name (template func + handlers).
func (h *handlers) categoryName(id int64) string {
	for _, c := range h.catCache {
		if c.ID == id {
			return c.Name
		}
	}
	return fmt.Sprintf("#%d", id)
}

// accountName resolves a stored account id to a display name (template func).
func (h *handlers) accountName(accountID int64) string {
	for _, a := range h.accCache {
		if a.ID == accountID {
			return a.DisplayName()
		}
	}
	return fmt.Sprintf("#%d", accountID)
}

// fmtAmount renders minor units as "-12.34 EUR" (template func).
func fmtAmount(minor any, currency string) string {
	var v int64
	switch n := minor.(type) {
	case int64:
		v = n
	case *int64:
		if n == nil {
			return ""
		}
		v = *n
	default:
		return ""
	}
	return sync.FormatMinor(v, currency) + " " + currency
}

// fmtDate shortens an RFC3339 timestamp ("2026-09-13T11:48:42Z") to its date
// part for dense tables; empty input stays empty.
func fmtDate(s string) string {
	if len(s) >= 10 && (s[4] == '-' && s[7] == '-') {
		return s[:10]
	}
	return s
}

// eventsSummary renders a channel's routing map as short human labels.
func eventsSummary(eventsJSON string) string {
	var m map[string]bool
	if err := json.Unmarshal([]byte(eventsJSON), &m); err != nil || len(m) == 0 {
		return "all events"
	}
	order := []struct{ key, label string }{
		{"needs_category", "new tx"},
		{"reauth_needed", "re-auth"},
		{"sync_failed", "sync failed"},
	}
	var labels []string
	for _, kv := range order {
		if m[kv.key] {
			labels = append(labels, kv.label)
		}
	}
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

func urlEncode(s string) string { return url.QueryEscape(s) }

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func httpRedirErr(w http.ResponseWriter, r *http.Request, err error) {
	http.Redirect(w, r, "/?err="+urlEncode(err.Error()), http.StatusSeeOther)
}

func strID(v string) int64 {
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}
