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

	recent, err := st.RecentTransactions(ctx, uid, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].EntryReference != "ref-2" {
		t.Errorf("recent ordering wrong: %+v", recent)
	}
	count, _ := st.CountTransactions(ctx, uid)
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
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
