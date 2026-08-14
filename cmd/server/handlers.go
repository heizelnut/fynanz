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
	pages    map[string]*template.Template // one set per page (template.go)
	accCache []store.Account               // account id -> display name, refreshed per page
	catCache []store.Category              // category id -> name, refreshed per page
}

func newServer(st *store.Store, svc *sync.Service) http.Handler {
	h := &handlers{store: st, svc: svc, rec: metrics.Recorder{}}
	if err := h.loadTemplates(); err != nil {
		log.Fatalf("templates: %v", err)
	}
	r := chi.NewRouter()
	r.Get("/", h.index)
	r.Get("/transactions", h.transactions)
	r.Get("/inbox", h.inbox)
	r.Post("/inbox/{txID}", h.inboxResolve)
	r.Post("/transactions/{txID}/category", h.txOverride)
	r.Get("/categories", h.categories)
	r.Post("/categories", h.categoriesCreate)
	r.Post("/categories/{catID}/rename", h.categoriesRename)
	r.Post("/categories/{catID}/merge", h.categoriesMerge)
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
	Title          string
	Message        string
	Error          string
	Links          []store.BankLink
	Accounts       []store.Account
	Txs            []store.Transaction
	TxCount        int64
	Categories     []store.Category
	Uncategorized  []store.Transaction
	UncategorizedN int64
}

type inboxPageData struct {
	pageData
	ResolvedCategory string
}

type categoryPageData struct {
	pageData
	CatTxCounts []catCount
}

type catCount struct {
	ID    int64
	Count int64
}

var defaultUserID int64 = 1 // single implicit v1 user (AGENTS.md §6)

func (h *handlers) index(w http.ResponseWriter, r *http.Request) {
	data, err := h.page(r, "Dashboard")
	if err != nil {
		httpError(w, err)
		return
	}
	h.render(w, "index.html", data)
}

func (h *handlers) transactions(w http.ResponseWriter, r *http.Request) {
	data, err := h.page(r, "Transactions")
	if err != nil {
		httpError(w, err)
		return
	}
	h.render(w, "transactions.html", data)
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

	cats, err := h.store.ListCategories(ctx, defaultUserID)
	if err != nil {
		return d, err
	}
	d.Categories = cats
	h.catCache = cats

	uncat, err := h.store.UncategorizedCount(ctx, defaultUserID)
	if err != nil {
		return d, err
	}
	d.UncategorizedN = uncat

	// Dashboard surfacing: first few items needing a category (§5).
	first, err := h.store.ListUncategorized(ctx, defaultUserID, 5)
	if err != nil {
		return d, err
	}
	d.Uncategorized = first
	return d, nil
}

// inbox renders the real-time resolve view (AGENTS.md §4/§10).
func (h *handlers) inbox(w http.ResponseWriter, r *http.Request) {
	data, err := h.page(r, "Categorize")
	if err != nil {
		httpError(w, err)
		return
	}
	items, err := h.store.ListUncategorized(r.Context(), defaultUserID, 100)
	if err != nil {
		httpError(w, err)
		return
	}
	d := inboxPageData{pageData: data, ResolvedCategory: r.URL.Query().Get("done")}
	d.Uncategorized = items
	h.render(w, "inbox.html", d)
}

// inboxResolve is the HTMX action: categorize + grow a rule. Responds with
// the row replaced and the nav badge updated out-of-band.
func (h *handlers) inboxResolve(w http.ResponseWriter, r *http.Request) {
	txID := strID(chi.URLParam(r, "txID"))
	catID, err := strconv.ParseInt(r.FormValue("category_id"), 10, 64)
	if err != nil || txID == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pattern, err := h.svc.ResolveInbox(r.Context(), txID, catID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Render only the row (hx-target) + badge (hx-swap-oob).
	if len(h.catCache) == 0 {
		if cats, err := h.store.ListCategories(r.Context(), defaultUserID); err == nil {
			h.catCache = cats
		}
	}
	left, _ := h.store.UncategorizedCount(r.Context(), defaultUserID)
	h.renderPartial(w, "inbox.html", "inbox_done_row", map[string]any{
		"TxID": txID, "CategoryName": h.categoryName(catID), "Pattern": pattern, "UncategorizedN": left,
	})
}

// txOverride is the manual category override from the transactions list
// (no rule is written; §4 "override at any time").
func (h *handlers) txOverride(w http.ResponseWriter, r *http.Request) {
	txID := strID(chi.URLParam(r, "txID"))
	catID, err := strconv.ParseInt(r.FormValue("category_id"), 10, 64)
	if err != nil || txID == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.svc.OverrideCategory(r.Context(), txID, catID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.renderPartial(w, "transactions.html", "tx_category_cell", map[string]any{
		"TxID": txID, "CategoryName": h.categoryName(catID), "Categories": h.catCache,
	})
}

// createLink starts a consent flow for the chosen bank and bounces the user
// to the bank's authorization page.
func (h *handlers) createLink(w http.ResponseWriter, r *http.Request) {
	bank := r.FormValue("bank")
	if bank == "" {
		http.Redirect(w, r, "/?err=no+bank+chosen", http.StatusSeeOther)
		return
	}
	// Reuse an existing link row for this bank (re-authorization), otherwise
	// create the first one: bank_links has UNIQUE(user_id, bank).
	existing, err := h.store.GetLinkByBank(r.Context(), defaultUserID, bank)
	if err != nil {
		httpError(w, err)
		return
	}
	if existing != nil {
		authURL, err := h.svc.Reconnect(r.Context(), existing.ID)
		if err != nil {
			httpRedirErr(w, r, err)
			return
		}
		log.Printf("link %d (%s) -> re-consent %s", existing.ID, bank, authURL)
		http.Redirect(w, r, authURL, http.StatusSeeOther)
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

// categories renders category management (add/rename/merge, AGENTS.md §4).
func (h *handlers) categories(w http.ResponseWriter, r *http.Request) {
	data, err := h.page(r, "Categories")
	if err != nil {
		httpError(w, err)
		return
	}
	counts := make(map[int64]int64)
	rows, err := h.store.DB().QueryContext(r.Context(),
		`SELECT category_id, COUNT(*) FROM transactions WHERE user_id = ? AND category_id IS NOT NULL GROUP BY category_id`,
		defaultUserID)
	if err != nil {
		httpError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err == nil {
			counts[id] = n
		}
	}
	_ = rows.Err()
	var cc []catCount
	for _, c := range data.Categories {
		cc = append(cc, catCount{ID: c.ID, Count: counts[c.ID]})
	}
	h.render(w, "categories.html", categoryPageData{pageData: data, CatTxCounts: cc})
}

func (h *handlers) categoriesCreate(w http.ResponseWriter, r *http.Request) {
	if _, err := h.store.CreateCategory(r.Context(), defaultUserID, r.FormValue("name")); err != nil {
		httpRedirErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/categories?msg=category+added", http.StatusSeeOther)
}

func (h *handlers) categoriesRename(w http.ResponseWriter, r *http.Request) {
	id := strID(chi.URLParam(r, "catID"))
	if err := h.store.RenameCategory(r.Context(), defaultUserID, id, r.FormValue("name")); err != nil {
		httpRedirErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/categories?msg=renamed", http.StatusSeeOther)
}

func (h *handlers) categoriesMerge(w http.ResponseWriter, r *http.Request) {
	id := strID(chi.URLParam(r, "catID"))
	into, err := strconv.ParseInt(r.FormValue("into"), 10, 64)
	if err != nil {
		httpRedirErr(w, r, err)
		return
	}
	if err := h.store.MergeCategory(r.Context(), defaultUserID, id, into); err != nil {
		httpRedirErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/categories?msg=merged", http.StatusSeeOther)
}

// reconnectLink restarts the consent flow for a link (row reused, see
// sync.Service.Reconnect).
func (h *handlers) reconnectLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "linkID"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/?err=bad+link+id", http.StatusSeeOther)
		return
	}
	authURL, err := h.svc.Reconnect(r.Context(), id)
	if err != nil {
		httpRedirErr(w, r, err)
		return
	}
	log.Printf("reconnecting link %d -> consent", id)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}
