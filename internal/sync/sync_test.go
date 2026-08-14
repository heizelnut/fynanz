package sync

import (
	"context"
	"path/filepath"
	"testing"

	"fynanz/internal/metrics"
	"fynanz/internal/store"
)

// newTestService wires a real store (temp DB) + fake EB server.
func newTestService(t *testing.T, f *fakeEB) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "int.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	c := testClient(t, f)
	svc := NewService(st, c, metrics.Recorder{}, "http://localhost:8080/links/callback",
		"personal", 90, 30)
	return svc, st
}

func fixtureEB() *fakeEB {
	return &fakeEB{
		authURL: "https://auth.invalid/consent",
		sessionResp: AuthorizeSessionResponse{
			SessionID: "sesh-1",
			Access:    Access{ValidUntil: "2025-08-01T00:00:00Z"},
			Accounts: []Account{{
				AccountID: AccountIdentification{Iban: "FI1234567890"}, Name: "Mock Personal",
				CashAccountType: "CACC", Product: "Mock", Currency: "EUR",
				UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", IdentificationHash: "hash-acc-1",
			}},
		},
		details: Account{
			AccountID: AccountIdentification{Iban: "FI1234567890"}, Name: "Mock Personal",
			Product: "Mock", Currency: "EUR",
		},
		balances: BalancesResponse{Balances: []BalanceResource{{
			Name: "Booked", BalanceAmount: Amount{Currency: "EUR", Amount: "999.00"}, BalanceType: "CLBD",
		}}},
		txnsPage1: TransactionsResponse{
			Transactions: []Transaction{
				{
					EntryReference: "tx-1", TransactionAmount: Amount{Currency: "EUR", Amount: "23.40"},
					CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-01-15",
					Creditor:              &PartyIdentification{Name: "S Market"},
					CreditorAccount:       &AccountIdentification{Iban: "FI1111111111"},
					RemittanceInformation: []string{"Grocery run"},
				},
				{
					EntryReference: "tx-2", TransactionAmount: Amount{Currency: "EUR", Amount: "-1500.00"},
					CreditDebitIndicator: "CRDT", Status: "BOOK", BookingDate: "2025-01-14",
					Debtor: &PartyIdentification{Name: "ACME Corp"},
				},
			},
		},
	}
}

func TestFullThreeLeggedFlow(t *testing.T) {
	svc, st := newTestService(t, fixtureEB())
	ctx := context.Background()

	// 1. Start consent.
	link, authURL, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if link.Bank != "sandbox" || link.AspspName != "Mock ASPSP" {
		t.Errorf("link aspsp wrong: %+v", link)
	}
	if authURL != "https://auth.invalid/consent" {
		t.Errorf("consent URL = %q", authURL)
	}

	// 2. Simulate the bank redirecting back with code + state.
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err != nil {
		t.Fatal(err)
	}

	// 3. Assert everything landed in the store.
	got, err := st.GetLink(ctx, link.ID, link.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.LinkActive || got.SessionID != "sesh-1" {
		t.Errorf("link not active: %+v", got)
	}
	if got.ValidUntil == "" {
		t.Error("valid_until not set")
	}

	accs, err := st.ListAccounts(ctx, link.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 1 {
		t.Fatalf("accounts = %d, want 1", len(accs))
	}
	a := accs[0]
	if a.Iban != "FI1234567890" || a.Name != "Mock Personal" || a.Currency != "EUR" {
		t.Errorf("account wrong: %+v", a)
	}
	if a.BalanceMinor == nil || *a.BalanceMinor != 99900 {
		t.Errorf("balance = %v, want 99900", a.BalanceMinor)
	}

	txs, err := st.RecentTransactions(ctx, link.UserID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 {
		t.Fatalf("stored %d transactions, want 2", len(txs))
	}
	t1, t2 := txs[0], txs[1] // booking_date desc: tx-1 (01-15) first, tx-2 (01-14) second
	if t2.Counterparty != "ACME Corp" {
		t.Errorf("tx-2 counterparty = %q", t2.Counterparty)
	}
	if t2.AmountMinor != 150000 {
		t.Errorf("tx-2 amount = %d, want 150000 (CRDT +1500.00)", t2.AmountMinor)
	}
	if t1.Counterparty != "S Market" || t1.CounterpartyAccount != "FI1111111111" {
		t.Errorf("tx-1 counterparty = %q / %q", t1.Counterparty, t1.CounterpartyAccount)
	}
	if t1.AmountMinor != -2340 {
		t.Errorf("tx-1 amount = %d, want -2340", t1.AmountMinor)
	}
	if t1.Raw == "" {
		t.Error("raw payload not stored")
	}

	// 4. Re-completing an active link fails; a re-sync dedupes everything.
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err == nil {
		t.Error("re-completing an active link should fail (already authorized)")
	}
	res, err := svc.SyncLink(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.TransactionsInserted != 0 || res.TransactionsSkipped != 2 {
		t.Errorf("re-sync counts: %d inserted / %d skipped, want 0/2", res.TransactionsInserted, res.TransactionsSkipped)
	}
}

func TestExpiredSessionMarksLinkReauth(t *testing.T) {
	f := fixtureEB()
	svc, st := newTestService(t, f)
	ctx := context.Background()

	link, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	// Initial consent + first sync succeed with a healthy session…
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err != nil {
		t.Fatal(err)
	}
	// …then the bank revokes/expires it (PSD2 refresh requirement).
	f.setSessionErr(&APIError{HTTPStatus: 400, Message: "Session has expired", Code: "EXPIRED_SESSION", Detail: "expired"})

	// Failing GetSession on sync → link moves to expired + reauth metric path.
	_, err = svc.SyncLink(ctx, link.ID)
	if err == nil {
		t.Fatal("sync succeeded despite expired session")
	}
	if !IsSessionError(err) {
		t.Errorf("err = %v, want session error", err)
	}

	got, _ := st.GetLink(ctx, link.ID, link.UserID)
	if got.Status != store.LinkExpired {
		t.Errorf("link status = %q, want %q (last_error: %s)", got.Status, store.LinkExpired, got.LastError)
	}
	active, _ := st.ActiveLinks(ctx, link.UserID)
	if len(active) != 0 {
		t.Errorf("expired link still in active list")
	}
}

func TestSyncAllSkipsNonActiveLinks(t *testing.T) {
	svc, _ := newTestService(t, fixtureEB())
	ctx := context.Background()
	if _, _, err := svc.StartLink(ctx, "sandbox"); err != nil {
		t.Fatal(err)
	}
	// Only pending → SyncAll must not crash and not return errors.
	if errs := svc.SyncAll(ctx); len(errs) != 0 {
		t.Errorf("SyncAll errored on pending links: %v", errs)
	}
}
