package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"fynanz/internal/metrics"
	"fynanz/internal/notify"
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
	r.Get("/settings", h.settings)
	r.Get("/terms", h.legalPage("Terms of service", "terms.html"))
	r.Get("/privacy", h.legalPage("Privacy policy", "privacy.html"))
	r.Post("/settings/notifications", h.notificationsAdd)
	r.Post("/settings/notifications/{chID}/delete", h.notificationsDelete)
	r.Post("/links", h.createLink)
	r.Get("/links/callback", h.linkCallback)
	r.Get("/links/sync", h.syncAll)
	r.Get("/links/{linkID}/sync", h.syncOne)
	r.Get("/links/{linkID}/reconnect", h.reconnectLink)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())
	r.Handle("/static/*", noCache(http.StripPrefix("/static/", http.FileServer(http.FS(webStatic)))))
	return r
}

type pageData struct {
	Title          string
	ActiveNav      string // which nav item shows the active dot ("dashboard", ...)
	Message        string
	Error          string
	Links          []store.BankLink
	Accounts       []store.Account
	Txs            []store.Transaction
	TxCount        int64
	Categories     []store.Category
	Uncategorized  []store.Transaction
	UncategorizedN int64
	Pagination     pagination
	Dashboard      dashboardData
}

// dashboardData is everything the insights panels need (milestone 7); empty
// HasData=false renders the "sync to see insights" hint.
type dashboardData struct {
	HasData  bool
	Currency string
	Since    string // balance series starts here (derived, not bank-reported)
	CatSpend []catSpendRow
	Flows    []monthFlowRow
	Balance  balanceChart
}

// pagination describes one page of a transaction listing.
// Pages holds page numbers to render (-1 marks an ellipsis gap).
type pagination struct {
	Current    int
	Prev       int // 0 when there is none
	Next       int // 0 when there is none
	TotalPages int64
	Total      int64
	Limit      int
	From       int64
	To         int64
	Pages      []int
}

// makePagination computes the pager for (page, limit) over total items,
// with a window of page numbers around the current one and ellipsis gaps.
func makePagination(page, limit int, total int64) pagination {
	p := pagination{Current: page, Limit: limit, Total: total}
	if total <= 0 {
		p.TotalPages = 1
		p.Pages = []int{1}
		return p
	}
	p.TotalPages = (total + int64(limit) - 1) / int64(limit)
	if p.Current < 1 {
		p.Current = 1
	}
	if int64(p.Current) > p.TotalPages {
		p.Current = int(p.TotalPages)
	}
	p.From = int64(p.Current-1)*int64(p.Limit) + 1
	p.To = int64(p.Current) * int64(p.Limit)
	if p.To > total {
		p.To = total
	}
	if p.Current > 1 {
		p.Prev = p.Current - 1
	}
	if int64(p.Current) < p.TotalPages {
		p.Next = p.Current + 1
	}

	// Windowed page numbers: edges always present, +/-2 around current,
	// -1 where a gap needs an ellipsis.
	set := map[int]bool{1: true, int(p.TotalPages): true}
	for n := p.Current - 2; n <= p.Current+2; n++ {
		if n >= 1 && n <= int(p.TotalPages) {
			set[n] = true
		}
	}
	var nums []int
	for n := range set {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for i, n := range nums {
		if i > 0 && n-nums[i-1] > 1 {
			p.Pages = append(p.Pages, -1)
		}
		p.Pages = append(p.Pages, n)
	}
	return p
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
	Name  string
	Count int64
}

var defaultUserID int64 = 1 // single implicit v1 user (AGENTS.md §6)

// noCache keeps dev iterations visible: the CSS/JS are embedded in the
// binary and change on every restart, so let the browser revalidate.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (h *handlers) index(w http.ResponseWriter, r *http.Request) {
	data, err := h.page(r, "Dashboard", 50, 1)
	if err != nil {
		httpError(w, err)
		return
	}
	dash, err := h.dashboard(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	data.Dashboard = dash
	data.ActiveNav = "dashboard"
	h.render(w, "index.html", data)
}

// dashboard assembles the insights panels (AGENTS.md §14 milestone 7): spend
// by category this month, 6 months of income vs expense, and a derived
// balance trend per account with balance data. v1 shows the primary currency
// only (no FX, §4).
func (h *handlers) dashboard(ctx context.Context) (dashboardData, error) {
	var d dashboardData
	cur, err := h.store.PrimaryCurrency(ctx, defaultUserID)
	if err != nil {
		return d, err
	}
	if cur == "" {
		return d, nil
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	historyFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -5, 0)

	cats, err := h.store.SpendByCategory(ctx, defaultUserID, cur, monthStart)
	if err != nil {
		return d, err
	}
	flows, err := h.store.IncomeExpenseByMonth(ctx, defaultUserID, cur, historyFrom.Format("2006-01-02"))
	if err != nil {
		return d, err
	}
	d = dashboardData{
		HasData:  true,
		Currency: cur,
		CatSpend: fillCatSpend(cats),
		Flows:    fillMonthFlows(flows, 6, now),
	}

	// Balance trend: derived per account (see deriveBalanceSeries).
	accounts, err := h.store.ListAccounts(ctx, defaultUserID)
	if err != nil {
		return d, err
	}
	since := now.AddDate(0, 0, -90).Format("2006-01-02")
	d.Since = since
	type rawSeries struct {
		label string
		pts   []balancePoint
	}
	var raw []rawSeries
	var allPts []balancePoint
	for _, a := range accounts {
		if a.BalanceMinor == nil {
			continue
		}
		days, err := h.store.DailyAmountSums(ctx, defaultUserID, a.ID, since)
		if err != nil {
			return d, err
		}
		pts := deriveBalanceSeries(*a.BalanceMinor, days, 60)
		if len(pts) == 0 {
			continue
		}
		raw = append(raw, rawSeries{label: a.DisplayName(), pts: pts})
		allPts = append(allPts, pts...)
	}
	if len(raw) > 0 {
		min, maxV := allPts[0].Minor, allPts[0].Minor
		for _, p := range allPts[1:] {
			if p.Minor < min {
				min = p.Minor
			}
			if p.Minor > maxV {
				maxV = p.Minor
			}
		}
		series := make([]lineSeries, 0, len(raw))
		for i, rs := range raw {
			series = append(series, lineSeries{
				Label:  rs.label,
				Points: svgPoints(rs.pts, min, maxV, 600, 200),
				Class:  fmt.Sprintf("bal-%d", i),
			})
		}
		d.Balance = buildBalanceChart(series, min, maxV, nil)
	}
	return d, nil
}

func (h *handlers) transactions(w http.ResponseWriter, r *http.Request) {
	limit, page := 50, 1
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	data, err := h.page(r, "Transactions", limit, page)
	if err != nil {
		httpError(w, err)
		return
	}
	data.ActiveNav = "transactions"
	h.render(w, "transactions.html", data)
}

func (h *handlers) page(r *http.Request, title string, limit, page int) (pageData, error) {
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

	count, err := h.store.CountTransactions(ctx, defaultUserID)
	if err != nil {
		return d, err
	}
	d.TxCount = count
	d.Pagination = makePagination(page, limit, count)

	offset := (d.Pagination.Current - 1) * limit
	txs, err := h.store.RecentTransactions(ctx, defaultUserID, 0, limit, offset)
	if err != nil {
		return d, err
	}
	d.Txs = txs

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
	data, err := h.page(r, "Categorize", 50, 1)
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
	d.ActiveNav = "inbox"
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
	data, err := h.page(r, "Categories", 50, 1)
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
		cc = append(cc, catCount{ID: c.ID, Name: c.Name, Count: counts[c.ID]})
	}
	data.ActiveNav = "categories"
	h.render(w, "categories.html", categoryPageData{pageData: data, CatTxCounts: cc})
}

// legalPage renders the terms/privacy documents (used by the prod-app
// registration and as in-app pages). No nav highlighting.
func (h *handlers) legalPage(title, page string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := h.page(r, title, 50, 1)
		if err != nil {
			httpError(w, err)
			return
		}
		h.render(w, page, data)
	}
}

// settings renders the notification channels panel (§7 configuration UI).
func (h *handlers) settings(w http.ResponseWriter, r *http.Request) {
	data, err := h.page(r, "Settings", 50, 1)
	if err != nil {
		httpError(w, err)
		return
	}
	ch, err := h.store.ListNotificationChannels(r.Context(), defaultUserID)
	if err != nil {
		httpError(w, err)
		return
	}
	settingsData := struct {
		pageData
		Channels []store.NotificationChannel
	}{
		pageData: data,
		Channels: ch,
	}
	settingsData.ActiveNav = "settings"
	h.render(w, "settings.html", settingsData)
}

// notificationsAdd persists a new channel from the form (ntfy or webhook).
// Events come in as checkboxes; unset = not subscribed (explicit opt-in when
// the form submitted; an empty events JSON still means "all" in the loader,
// so we always write at least one event here).
func (h *handlers) notificationsAdd(w http.ResponseWriter, r *http.Request) {
	typ := r.FormValue("type")
	if typ != "ntfy" && typ != "webhook" {
		http.Redirect(w, r, "/settings?err=unknown+channel+type", http.StatusSeeOther)
		return
	}
	// Eagerly validate the config through the backend's own factory, so a
	// mistyped/empty URL fails here with a visible error instead of silently
	// at every send attempt later.
	var raw []byte
	switch typ {
	case "ntfy":
		c := notify.NtfyConfig{URL: r.FormValue("ntfy_url"), Topic: r.FormValue("ntfy_topic"), Token: r.FormValue("ntfy_token")}
		raw, _ = json.Marshal(c)
	case "webhook":
		c := notify.WebhookConfig{URL: r.FormValue("webhook_url")}
		raw, _ = json.Marshal(c)
	}
	factory := map[string]notify.Factory{"ntfy": notify.NtfyFactory, "webhook": notify.WebhookFactory}[typ]
	if _, err := factory(raw); err != nil {
		httpRedirErr(w, r, fmt.Errorf("invalid channel: %w", err))
		return
	}
	config := string(raw)
	events := map[notify.Event]bool{}
	for _, e := range []notify.Event{notify.EventNeedsCategory, notify.EventReauthNeeded, notify.EventSyncFailed} {
		events[e] = r.FormValue(string(e)) == "on"
	}
	eventsRaw, _ := json.Marshal(events)
	if _, err := h.store.InsertNotificationChannel(r.Context(), defaultUserID, typ, config, string(eventsRaw), true); err != nil {
		httpRedirErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?msg=notification+channel+added", http.StatusSeeOther)
}

func (h *handlers) notificationsDelete(w http.ResponseWriter, r *http.Request) {
	id := strID(chi.URLParam(r, "chID"))
	if err := h.store.DeleteNotificationChannel(r.Context(), defaultUserID, id); err != nil {
		httpRedirErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?msg=channel+removed", http.StatusSeeOther)
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
