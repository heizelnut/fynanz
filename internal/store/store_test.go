package store

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestMigrateAndSeeds(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	uid, err := st.EnsureDefaultUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if uid != 1 {
		t.Errorf("default user = %d, want 1", uid)
	}
	// Idempotent.
	again, err := st.EnsureDefaultUser(ctx)
	if err != nil || again != uid {
		t.Errorf("ensure default user not idempotent: %d vs %d, %v", again, uid, err)
	}

	cats, err := st.ListCategories(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 14 {
		t.Errorf("seeded %d categories, want 14 (AGENTS.md §5 list)", len(cats))
	}
}

func TestLinkLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)

	link, err := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")
	if err != nil {
		t.Fatal(err)
	}
	if link.Status != LinkPendingConsent || link.State == "" {
		t.Errorf("bad initial link: %+v", link)
	}

	got, err := st.GetLinkByState(ctx, link.State)
	if err != nil || got == nil || got.ID != link.ID {
		t.Fatalf("GetLinkByState: %v %+v", err, got)
	}

	if err := st.SetLinkAuthorizing(ctx, link.ID, "auth-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ActivateLink(ctx, link.ID, "session-1", "2025-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	active, err := st.ActiveLinks(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].SessionID != "session-1" {
		t.Fatalf("active links wrong: %+v", active)
	}

	if err := st.SetLinkStatus(ctx, link.ID, LinkExpired, "session expired"); err != nil {
		t.Fatal(err)
	}
	active, _ = st.ActiveLinks(ctx, uid)
	if len(active) != 0 {
		t.Errorf("expired link still active")
	}
}

func TestUpsertAccountsAndTxDedupe(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	link, _ := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")

	acc := AccountInput{
		UID: "11111111-1111-1111-1111-111111111111", IdentificationHash: "hash-1",
		Iban: "FI1111111111", Name: "Test Account", Currency: "EUR",
	}
	ids, err := st.UpsertAccounts(ctx, uid, link.ID, []AccountInput{acc})
	if err != nil {
		t.Fatal(err)
	}
	firstID := ids[acc.UID]
	if firstID == 0 {
		t.Fatal("no account id returned")
	}

	// Re-sync same account: same row, updated name.
	acc.Name = "Renamed"
	ids2, err := st.UpsertAccounts(ctx, uid, link.ID, []AccountInput{acc})
	if err != nil {
		t.Fatal(err)
	}
	if ids2[acc.UID] != firstID {
		t.Errorf("account duplicated: %d -> %d", firstID, ids2[acc.UID])
	}
	accs, _ := st.ListAccounts(ctx, uid)
	if len(accs) != 1 || accs[0].Name != "Renamed" {
		t.Errorf("account not updated: %+v", accs)
	}

	txs := []TransactionInput{
		{EntryReference: "ref-1", BookingDate: "2025-01-10", AmountMinor: -1000, Currency: "EUR", CreditDebit: "DBIT", Status: "BOOK", Counterparty: "Supermarket"},
		{EntryReference: "ref-2", BookingDate: "2025-01-11", AmountMinor: 250000, Currency: "EUR", CreditDebit: "CRDT", Status: "BOOK", Counterparty: "Employer"},
	}
	ins, skip, err := st.InsertTransactions(ctx, uid, firstID, txs)
	if err != nil {
		t.Fatal(err)
	}
	if ins != 2 || skip != 0 {
		t.Fatalf("first insert: %d inserted, %d skipped", ins, skip)
	}
	// Same batch again -> all skipped (dedupe).
	ins, skip, err = st.InsertTransactions(ctx, uid, firstID, txs)
	if err != nil {
		t.Fatal(err)
	}
	if ins != 0 || skip != 2 {
		t.Fatalf("re-insert: %d inserted, %d skipped, want 0/2", ins, skip)
	}

	recent, err := st.RecentTransactions(ctx, uid, TxFilter{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].EntryReference != "ref-2" {
		t.Errorf("recent ordering wrong: %+v", recent)
	}
	count, _ := st.CountTransactions(ctx, uid, TxFilter{})
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestCategoryAssignmentAndInbox(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	link, _ := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")
	ids, _ := st.UpsertAccounts(ctx, uid, link.ID, []AccountInput{{UID: "u1", IdentificationHash: "h1"}})

	// Groceries is seeded by the migration — reuse it.
	seeded, _ := st.ListCategories(ctx, uid)
	var groceries int64
	for _, c := range seeded {
		if c.Name == "Groceries" {
			groceries = c.ID
		}
	}
	if groceries == 0 {
		t.Fatal("seeded Groceries category missing")
	}
	if _, err := st.CreateCategory(ctx, uid, "Groceries"); err == nil {
		t.Error("duplicate seeded name should fail")
	}
	if _, err := st.CreateCategory(ctx, uid, "Pets"); err != nil {
		t.Errorf("new category should succeed: %v", err)
	}
	if _, err := st.CreateCategory(ctx, uid, "Pets"); err == nil {
		t.Error("duplicate custom category name should fail")
	}

	txs := []TransactionInput{
		{EntryReference: "r1", BookingDate: "2025-02-01", AmountMinor: -100, Currency: "EUR", CreditDebit: "DBIT", Counterparty: "S Market", MerchantNormalized: "s market"},
		{EntryReference: "r2", BookingDate: "2025-02-02", AmountMinor: 50, Currency: "EUR", CreditDebit: "CRDT", Counterparty: "Acme", MerchantNormalized: "acme"},
	}
	if _, _, err := st.InsertTransactions(ctx, uid, ids["u1"], txs); err != nil {
		t.Fatal(err)
	}

	if n, _ := st.UncategorizedCount(ctx, uid); n != 2 {
		t.Errorf("uncategorized = %d, want 2", n)
	}
	items, err := st.ListUncategorized(ctx, uid, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].MerchantNormalized != "acme" || items[1].MerchantNormalized != "s market" {
		t.Errorf("inbox wrong (want newest first): %+v", items)
	}

	// Categorize the first via the inbox path.
	if err := st.SetTransactionCategory(ctx, uid, items[0].ID, &groceries, CatSourceRule); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertRule(ctx, uid, groceries, "s market", "exact"); err != nil {
		t.Fatal(err)
	}

	if n, _ := st.UncategorizedCount(ctx, uid); n != 1 {
		t.Errorf("after resolve uncategorized = %d, want 1", n)
	}
	got, err := st.GetTransaction(ctx, uid, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CategoryID == nil || *got.CategoryID != groceries || got.CategorySource != CatSourceRule || got.CategoryName != "Groceries" {
		t.Errorf("category not applied: %+v", got)
	}

	// Rule survives and matches a new transaction on the next sync.
	rules, err := st.ListRules(ctx, uid)
	if err != nil || len(rules) != 1 || rules[0].Pattern != "s market" {
		t.Errorf("rules: %+v %v", rules, err)
	}
}

func TestMergeCategory(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	cats, _ := st.ListCategories(ctx, uid) // seeded v1 list
	src, dst := cats[1].ID, cats[3].ID

	link, _ := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")
	ids, _ := st.UpsertAccounts(ctx, uid, link.ID, []AccountInput{{UID: "u1", IdentificationHash: "h1"}})
	g := func(c int64) *int64 { return &c }
	ins, _, _ := st.InsertTransactions(ctx, uid, ids["u1"], []TransactionInput{
		{EntryReference: "r1", BookingDate: "2025-01-01", AmountMinor: -1, Currency: "EUR", CreditDebit: "DBIT", CategoryID: g(src), CategorySource: CatSourceRule, MerchantNormalized: "pat"},
		{EntryReference: "r2", BookingDate: "2025-01-02", AmountMinor: -2, Currency: "EUR", CreditDebit: "DBIT", CategoryID: g(dst), CategorySource: CatSourceRule, MerchantNormalized: "pat2"},
	})
	_ = ins
	_ = st.InsertRule(ctx, uid, src, "pat", "exact")

	if err := st.MergeCategory(ctx, uid, src, dst); err != nil {
		t.Fatal(err)
	}
	txs, _ := st.RecentTransactions(ctx, uid, TxFilter{}, 10, 0)
	for _, tx := range txs {
		if tx.CategoryID == nil || *tx.CategoryID != dst {
			t.Errorf("post-merge tx category %v, want %d", tx.CategoryID, dst)
		}
	}
	rules, _ := st.ListRules(ctx, uid)
	if len(rules) != 0 {
		t.Errorf("source rules not removed: %+v", rules)
	}
	if _, err := st.CreateCategory(ctx, uid, "Groceries"); err != nil {
		t.Logf("(seeded Groceries may have been merged away)")
	}
}

func TestDashboardAggregations(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	link, _ := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")
	ids, _ := st.UpsertAccounts(ctx, uid, link.ID, []AccountInput{
		{UID: "u1", IdentificationHash: "h1"},
		{UID: "u2", IdentificationHash: "h2"},
	})
	g := func(c int64) *int64 { return &c }
	cats, _ := st.ListCategories(ctx, uid)
	groceries, transport := cats[1].ID, cats[3].ID

	txs := []TransactionInput{
		// August: groceries 10 + 3, transport 2.50, income 100; a transfer;
		// one uncategorized expense.
		{EntryReference: "a1", BookingDate: "2026-08-01", AmountMinor: -1000, Currency: "EUR", CreditDebit: "DBIT", CategoryID: g(groceries), CategorySource: CatSourceRule, MerchantNormalized: "m1"},
		{EntryReference: "a2", BookingDate: "2026-08-05", AmountMinor: -300, Currency: "EUR", CreditDebit: "DBIT", CategoryID: g(groceries), CategorySource: CatSourceRule, MerchantNormalized: "m2"},
		{EntryReference: "a3", BookingDate: "2026-08-06", AmountMinor: -250, Currency: "EUR", CreditDebit: "DBIT", CategoryID: g(transport), CategorySource: CatSourceRule, MerchantNormalized: "m3"},
		{EntryReference: "a4", BookingDate: "2026-08-07", AmountMinor: 10000, Currency: "EUR", CreditDebit: "CRDT", CategoryID: g(cats[0].ID), CategorySource: CatSourceRule, MerchantNormalized: "m4"},
		{EntryReference: "a5", BookingDate: "2026-07-20", AmountMinor: -200, Currency: "EUR", CreditDebit: "DBIT", CategoryID: nil, MerchantNormalized: "m5"},
		{EntryReference: "a6", BookingDate: "2026-08-10", AmountMinor: -500, Currency: "EUR", CreditDebit: "DBIT", InternalTransfer: true, CategoryID: g(cats[10].ID), CategorySource: CatSourceTransfer, MerchantNormalized: "m6"},
		// A USD row in the same account (multi-currency isolation).
		{EntryReference: "a7", BookingDate: "2026-08-11", AmountMinor: -777, Currency: "USD", CreditDebit: "DBIT", CategoryID: g(groceries), CategorySource: CatSourceRule, MerchantNormalized: "m7"},
	}
	if _, _, err := st.InsertTransactions(ctx, uid, ids["u1"], txs); err != nil {
		t.Fatal(err)
	}

	// Spend by category: August, EUR only — transfer excluded, income
	// excluded, uncategorized from July excluded by date, USD excluded.
	spend, err := st.SpendByCategory(ctx, uid, "EUR", "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(spend) != 2 || spend[0].Name != "Groceries" || spend[0].TotalMinor != 1300 || spend[1].TotalMinor != 250 {
		t.Errorf("spend by category wrong: %+v", spend)
	}

	// Editor: if everything is in July (uncategorized), it still shows up.
	spendJul, _ := st.SpendByCategory(ctx, uid, "EUR", "2026-07-01")
	if len(spendJul) != 3 || !spendJul[2].IsUncategorized {
		t.Errorf("july spend (incl. uncategorized) wrong: %+v", spendJul)
	}

	flows, err := st.IncomeExpenseByMonth(ctx, uid, "EUR", "2026-07-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 2 || flows[0].Month != "2026-07" || flows[0].ExpenseMinor != 200 ||
		flows[1].Month != "2026-08" || flows[1].IncomeMinor != 10000 || flows[1].ExpenseMinor != 1550 {
		t.Errorf("month flows wrong: %+v", flows)
	}

	days, err := st.DailyAmountSums(ctx, uid, ids["u1"], "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	// Every movement counts here (balance is balance): 6 distinct booking
	// days on account u1 in August — transfers included, USD rows too.
	if len(days) != 6 || days[0].Date != "2026-08-01" {
		t.Errorf("daily sums wrong: %+v", days)
	}

	cur, err := st.PrimaryCurrency(ctx, uid)
	if err != nil || cur != "EUR" {
		t.Errorf("primary currency = %q, %v; want EUR", cur, err)
	}
}

func TestEmptyEntryReferenceGetsPseudoRef(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	link, _ := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")
	ids, _ := st.UpsertAccounts(ctx, uid, link.ID, []AccountInput{{UID: "u1", IdentificationHash: "h1"}})

	tx := TransactionInput{BookingDate: "2025-01-01", AmountMinor: -500, Currency: "EUR", CreditDebit: "DBIT", Counterparty: "Cafe"}
	ins, _, err := st.InsertTransactions(ctx, uid, ids["u1"], []TransactionInput{tx})
	if err != nil || ins != 1 {
		t.Fatalf("insert: %d, %v", ins, err)
	}
	ins, _, _ = st.InsertTransactions(ctx, uid, ids["u1"], []TransactionInput{tx})
	if ins != 0 {
		t.Errorf("pseudo-ref dedupe failed: re-inserted %d rows", ins)
	}
}

func TestSettingsCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)

	if v, _ := st.GetSetting(ctx, uid, "eb_app_id"); v != "" {
		t.Errorf("unset setting should be empty, got %q", v)
	}
	if err := st.SetSetting(ctx, uid, "eb_app_id", "app-1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.GetSetting(ctx, uid, "eb_app_id"); v != "app-1" {
		t.Errorf("got %q, want app-1", v)
	}
	// Upsert updates.
	if err := st.SetSetting(ctx, uid, "eb_app_id", "app-2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.GetSetting(ctx, uid, "eb_app_id"); v != "app-2" {
		t.Errorf("got %q, want app-2", v)
	}
	// Empty clears (back to env defaults).
	if err := st.SetSetting(ctx, uid, "eb_app_id", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.GetSetting(ctx, uid, "eb_app_id"); v != "" {
		t.Errorf("cleared setting should be empty, got %q", v)
	}
}

func TestTxFilters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	link, _ := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")
	ids, _ := st.UpsertAccounts(ctx, uid, link.ID, []AccountInput{
		{UID: "f1", IdentificationHash: "fh1"},
		{UID: "f2", IdentificationHash: "fh2"},
	})
	g := func(c int64) *int64 { return &c }
	cats, _ := st.ListCategories(ctx, uid)
	groceries := cats[1].ID

	txs := []TransactionInput{
		{EntryReference: "q1", BookingDate: "2026-08-01", AmountMinor: -100, Currency: "EUR", CreditDebit: "DBIT", Counterparty: "S Market", MerchantNormalized: "s market", CategoryID: g(groceries), CategorySource: CatSourceRule},
		{EntryReference: "q2", BookingDate: "2026-08-05", AmountMinor: -200, Currency: "EUR", CreditDebit: "DBIT", Counterparty: "100% Organic", MerchantNormalized: "100% organic", CategoryID: g(groceries), CategorySource: CatSourceRule},
		{EntryReference: "q3", BookingDate: "2026-08-10", AmountMinor: 500, Currency: "EUR", CreditDebit: "CRDT", Counterparty: "ACME", MerchantNormalized: "acme", CategoryID: nil},
		// Same account, different date (July) for range tests.
		{EntryReference: "q4", BookingDate: "2026-07-20", AmountMinor: -10, Currency: "EUR", CreditDebit: "DBIT", Counterparty: "Train", MerchantNormalized: "train", CategoryID: nil},
	}
	for i := range txs {
		if _, _, err := st.InsertTransactions(ctx, uid, ids["f1"], []TransactionInput{txs[i]}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.InsertTransactions(ctx, uid, ids["f2"], []TransactionInput{
		{EntryReference: "q5", BookingDate: "2026-08-03", AmountMinor: -7, Currency: "EUR", CreditDebit: "DBIT", Counterparty: "S Market", MerchantNormalized: "s market", CategoryID: g(groceries), CategorySource: CatSourceRule},
	}); err != nil {
		t.Fatal(err)
	}

	count := func(f TxFilter) int64 {
		n, err := st.CountTransactions(ctx, uid, f)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	get := func(f TxFilter) []Transaction {
		tx, err := st.RecentTransactions(ctx, uid, f, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}

	if n := count(TxFilter{}); n != 5 {
		t.Errorf("all = %d, want 5", n)
	}
	// Account filter.
	if n := count(TxFilter{AccountID: ids["f2"]}); n != 1 {
		t.Errorf("account f2 = %d, want 1", n)
	}
	// Category + uncategorized filter.
	if n := count(TxFilter{CategoryID: groceries}); n != 3 {
		t.Errorf("category = %d, want 3", n)
	}
	if n := count(TxFilter{CategoryID: UncategorizedCat}); n != 2 {
		t.Errorf("uncategorized = %d, want 2", n)
	}
	// Date range.
	if n := count(TxFilter{DateFrom: "2026-08-01", DateTo: "2026-08-05"}); n != 3 {
		t.Errorf("aug 1-5 = %d, want 3", n)
	}
	// Free-text on merchant/counterparty; literal % must not act as wildcard.
	if n := count(TxFilter{Query: "100% Or"}); n != 1 {
		t.Errorf("literal-%% query = %d, want 1", n)
	}
	if n := count(TxFilter{Query: "s market"}); n != 2 {
		t.Errorf("query 's market' = %d, want 2", n)
	}
	// Underscore is escaped (plain 'a_c' would match 'acme').
	if n := count(TxFilter{Query: "a_c"}); n != 0 {
		t.Errorf("underscore query = %d, want 0", n)
	}
	// Combined: account + query.
	if tx := get(TxFilter{AccountID: ids["f1"], Query: "market"}); len(tx) != 1 {
		t.Errorf("combined = %d, want 1", len(tx))
	}
}

func TestAllTransactions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	link, _ := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")
	ids, _ := st.UpsertAccounts(ctx, uid, link.ID, []AccountInput{{UID: "u1", IdentificationHash: "h1"}})
	g := func(c int64) *int64 { return &c }
	cats, _ := st.ListCategories(ctx, uid)
	_, _, err := st.InsertTransactions(ctx, uid, ids["u1"], []TransactionInput{
		{EntryReference: "e1", BookingDate: "2026-08-02", AmountMinor: -100, Currency: "EUR", CreditDebit: "DBIT", Counterparty: "Bakery", MerchantNormalized: "bakery", CategoryID: g(cats[1].ID), CategorySource: CatSourceRule},
		{EntryReference: "e2", BookingDate: "2026-08-01", AmountMinor: 500, Currency: "EUR", CreditDebit: "CRDT", Counterparty: "ACME", MerchantNormalized: "acme", CategoryID: nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := st.AllTransactions(ctx, uid, TxFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].EntryReference != "e1" {
		t.Errorf("export order wrong: %+v", all)
	}
	// Filtered export honors category/query like the page does.
	filtered, err := st.AllTransactions(ctx, uid, TxFilter{Query: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].EntryReference != "e2" {
		t.Errorf("filtered export wrong: %+v", filtered)
	}
}

func TestNotificationEvents(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)

	for i := 0; i < 3; i++ {
		err := st.InsertNotificationEvent(ctx, NotificationEvent{
			UserID: uid, ChannelID: 1, ChannelType: "webhook", Event: "sync_failed",
			OK: i%2 == 0, Error: errMsgIf(i), Title: "Sync failed", Message: "m",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := st.CountNotificationEvents(ctx, uid); n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
	evs, err := st.RecentNotificationEvents(ctx, uid, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || !evs[0].OK || evs[1].OK || !evs[2].OK {
		t.Errorf("order/ok wrong: %+v", evs)
	}
	page2, _ := st.RecentNotificationEvents(ctx, uid, 2, 2)
	if len(page2) != 1 {
		t.Errorf("paging wrong: %d", len(page2))
	}
}

func errMsgIf(i int) string {
	if i%2 == 0 {
		return ""
	}
	return "boom"
}
