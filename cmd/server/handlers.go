package main

import (
	"html/template"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"fynanz/internal/metrics"
	"fynanz/internal/store"
	"fynanz/internal/sync"
)

type handlers struct {
	store    *store.Store
	svc      *sync.Service
	rec      metrics.Recorder
	tmpl     *template.Template
	accCache []store.Account // account id -> name for templates, refreshed per page
}

func newServer(st *store.Store, svc *sync.Service) http.Handler {
	h := &handlers{store: st, svc: svc, rec: metrics.Recorder{}}
	if err := h.loadTemplates(); err != nil {
		log.Fatalf("templates: %v", err)
	}
	r := chi.NewRouter()
	r.Get("/", h.index)
	r.Get("/transactions", h.transactions)
	r.Post("/links", h.createLink)
	r.Get("/links/callback", h.linkCallback)
	r.Get("/links/sync", h.syncAll)
	r.Get("/links/{linkID}/sync", h.syncOne)
	r.Get("/links/{linkID}/reconnect", h.reconnectLink)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(webStatic))))
	return r
}

type pageData struct {
	Title    string
	Message  string
	Error    string
	Links    []store.BankLink
	Accounts []store.Account
	Txs      []store.Transaction
	TxCount  int64
}

var defaultUserID int64 = 1 // single implicit v1 user (AGENTS.md §6)

func (h *handlers) index(w http.ResponseWriter, r *http.Request) {
	data, err := h.page(r, "Dashboard")
	if err != nil {
		httpError(w, err)
		return
	}
	h.render(w, data)
}

func (h *handlers) transactions(w http.ResponseWriter, r *http.Request) {
	data, err := h.page(r, "Transactions")
	if err != nil {
		httpError(w, err)
		return
	}
	h.render(w, data)
}

func (h *handlers) page(r *http.Request, title string) (pageData, error) {
	var d pageData
	d.Title = title
	d.Message = r.URL.Query().Get("msg")
	d.Error = r.URL.Query().Get("err")
	ctx := r.Context()

	links, err := h.store.ListLinks(ctx, defaultUserID)
	if err != nil {
		return d, err
	}
	d.Links = links

	accounts, err := h.store.ListAccounts(ctx, defaultUserID)
	if err != nil {
		return d, err
	}
	d.Accounts = accounts
	h.accCache = accounts

	txs, err := h.store.RecentTransactions(ctx, defaultUserID, 0, 50)
	if err != nil {
		return d, err
	}
	d.Txs = txs

	count, err := h.store.CountTransactions(ctx, defaultUserID)
	if err != nil {
		return d, err
	}
	d.TxCount = count
	return d, nil
}

// createLink starts a consent flow for the chosen bank and bounces the user
// to the bank's authorization page.
func (h *handlers) createLink(w http.ResponseWriter, r *http.Request) {
	bank := r.FormValue("bank")
	if bank == "" {
		http.Redirect(w, r, "/?err=no+bank+chosen", http.StatusSeeOther)
		return
	}
	link, authURL, err := h.svc.StartLink(r.Context(), bank)
	if err != nil {
		httpRedirErr(w, r, err)
		return
	}
	log.Printf("link %d (%s) -> consent %s", link.ID, bank, authURL)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// linkCallback is the app's redirect_url target after bank consent: ?code&state.
func (h *handlers) linkCallback(w http.ResponseWriter, r *http.Request) {
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Redirect(w, r, "/?err=callback+missing+code+or+state", http.StatusSeeOther)
		return
	}
	if err := h.svc.CompleteLink(r.Context(), state, code); err != nil {
		httpRedirErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/?msg=bank+linked+and+synced", http.StatusSeeOther)
}

func (h *handlers) syncAll(w http.ResponseWriter, r *http.Request) {
	errs := h.svc.SyncAll(r.Context())
	if len(errs) > 0 {
		http.Redirect(w, r, "/?err=some+syncs+failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/?msg=sync+complete", http.StatusSeeOther)
}

func (h *handlers) syncOne(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "linkID"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/?err=bad+link+id", http.StatusSeeOther)
		return
	}
	res, err := h.svc.SyncLink(r.Context(), id)
	if err != nil {
		httpRedirErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/?msg=synced+"+strconv.FormatInt(res.TransactionsInserted, 10)+"+new+transactions",
		http.StatusSeeOther)
}

// reconnectLink closes the old link row and starts a fresh consent flow for
// the same bank (UNIQUE(user_id, bank) allows one row per bank).
func (h *handlers) reconnectLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "linkID"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/?err=bad+link+id", http.StatusSeeOther)
		return
	}
	link, err := h.store.GetLink(r.Context(), id, defaultUserID)
	if err != nil {
		httpError(w, err)
		return
	}
	if link == nil {
		http.Redirect(w, r, "/?err=no+such+link", http.StatusSeeOther)
		return
	}
	if err := h.store.SetLinkStatus(r.Context(), link.ID, store.LinkClosed, "reconnecting"); err != nil {
		httpError(w, err)
		return
	}
	newLink, authURL, err := h.svc.StartLink(r.Context(), link.Bank)
	if err != nil {
		httpRedirErr(w, r, err)
		return
	}
	log.Printf("reconnecting %s: link %d -> %d", link.Bank, link.ID, newLink.ID)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}
