package main

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// TestPagerAndExportQueryStrings is a regression guard: PagerBase/ExportQS
// used to be plain strings, and html/template URL-escapes pre-built queries
// in href context ("?q=lidl&p2p=0" became the single mangled param
// "q%3dlidl%26p2p%3d0") — which silently dropped every filter from the
// export and pagination links (exports returned ALL transactions regardless
// of the active filters). They are template.URL now; these tests lock the
// well-formed output.
func TestPagerAndExportQueryStrings(t *testing.T) {
	export := filterQS("", "", "", "", "lidl", "0")
	if want := "p2p=0&q=lidl"; string(export) != want {
		t.Errorf("filterQS = %q, want %q", export, want)
	}
	pager := pagerBase("/transactions", 50, "", "", "", "", "lidl", "0")
	if want := "/transactions?limit=50&p2p=0&q=lidl"; string(pager) != want {
		t.Errorf("pagerBase = %q, want %q", pager, want)
	}
	pagerInbox := pagerBase("/inbox", 50, "", "", "", "", "", "")
	if want := "/inbox?limit=50"; string(pagerInbox) != want {
		t.Errorf("pagerBase(inbox) = %q, want %q", pagerInbox, want)
	}
	// Empty filters collapse to a bare link.
	if qs := filterQS("", "", "", "", "", ""); string(qs) != "" {
		t.Errorf("filterQS(empty) = %q, want empty", qs)
	}
}

// TestHrefRenderingKeepsQuery proves the template.URL value survives
// html/template's href context with '=' and '&' intact (the observable bug:
// they were mangled into %3d/%26), while '&' is still HTML-entity escaped.
func TestHrefRenderingKeepsQuery(t *testing.T) {
	tmpl := template.Must(template.New("x").Parse(
		`<a href="/transactions/export?{{.ExportQS}}">e</a>`))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ ExportQS template.URL }{
		ExportQS: template.URL("p2p=0&q=lidl"),
	}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "%3d") || strings.Contains(out, "%26") {
		t.Errorf("query got URL-escaped in href: %s", out)
	}
	if !strings.Contains(out, "p2p=0&amp;q=lidl") {
		t.Errorf("href does not keep the filter query: %s", out)
	}
}
