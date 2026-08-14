package main

import (
	"bytes"
	"strings"
	"testing"

	"fynanz/internal/store"
)

func TestInboxTemplates(t *testing.T) {
	h := &handlers{}
	if err := h.loadTemplates(); err != nil {
		t.Fatal(err)
	}
	cats := []store.Category{{ID: 2, Name: "Groceries"}, {ID: 4, Name: "Transport"}}
	d := pageData{
		Uncategorized: []store.Transaction{
			{TransactionInput: store.TransactionInput{EntryReference: "x", BookingDate: "2026-08-01", AmountMinor: -1234, Currency: "EUR", MerchantNormalized: "ella"}, ID: 41},
			{TransactionInput: store.TransactionInput{EntryReference: "y", BookingDate: "2026-08-02", AmountMinor: 50, Currency: "EUR", MerchantNormalized: "acme"}, ID: 42},
		},
		Categories:     cats,
		UncategorizedN: 2,
		Pagination:     makePagination(1, 50, 2),
		PagerBase:      "/inbox?limit=50",
	}

	var buf bytes.Buffer
	if err := h.pages["inbox.html"].ExecuteTemplate(&buf, "inbox_refresh", d); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		`id="inbox-body"`, `hx-swap-oob="true"`, `id="tx-41"`, `id="tx-42"`,
		`id="inbox-count"`, `hx-post="/inbox/41"`, `hx-swap="none"`,
		`name="limit" value="50"`, `name="page" value="1"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("refresh output missing %q", want)
		}
	}
	if strings.Contains(html, "inbox_done_row") || strings.Contains(html, "✓") {
		t.Error("stale done-row fragment still rendered")
	}

	// Full page renders the same shared rows partial.
	var page bytes.Buffer
	if err := h.pages["inbox.html"].ExecuteTemplate(&page, "base.html", d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.String(), `id="inbox-body"`) {
		t.Error("page does not include inbox rows")
	}
	// The rows must live in a real table with column headers: a bare tbody
	// loses table styling and the header row (regression guard).
	if !strings.Contains(page.String(), "<thead>") || !strings.Contains(page.String(), "<th>Merchant</th>") {
		t.Error("page table header missing")
	}
}
