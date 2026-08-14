package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"

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

// loadTemplates parses all server-rendered templates from the embedded FS.
func (h *handlers) loadTemplates() error {
	fsys, err := web.Templates()
	if err != nil {
		return err
	}
	funcs := template.FuncMap{
		"fmtAmount":   fmtAmount,
		"accountName": h.accountName,
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(fsys, "*.html")
	if err != nil {
		return err
	}
	h.tmpl = tmpl
	return nil
}

func (h *handlers) render(w http.ResponseWriter, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "base.html", d); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
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

func urlEncode(s string) string { return url.QueryEscape(s) }

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func httpRedirErr(w http.ResponseWriter, r *http.Request, err error) {
	http.Redirect(w, r, "/?err="+urlEncode(err.Error()), http.StatusSeeOther)
}
