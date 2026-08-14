package sync

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"fynanz/internal/metrics"
	"fynanz/internal/notify"
	"fynanz/internal/store"
)

// newTestService wires a real store (temp DB) + fake EB server.
func newTestService(t *testing.T, f *fakeEB) (*Service, *store.Store) {
	return newTestServiceN(t, f, nil)
}

func newTestServiceN(t *testing.T, f *fakeEB, n notify.Notifier) (*Service, *store.Store) {
	t.Helper()
	if n == nil {
		n = notify.Nop{}
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "int.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	c := testClient(t, f)
	svc := NewService(st, c, metrics.Recorder{}, n, "http://localhost:8080",
		"http://localhost:8080/links/callback", "personal", 90, 30)
	return svc, st
}

// fakeNotifier records payloads for assertions.
type fakeNotifier struct {
	mu   sync.Mutex
	sent []notify.Payload
}

func (f *fakeNotifier) Notify(_ context.Context, p notify.Payload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, p)
	return nil
}

func (f *fakeNotifier) events() []notify.Payload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notify.Payload(nil), f.sent...)
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

	txs, err := st.RecentTransactions(ctx, link.UserID, 0, 10, 0)
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
}

// TestReauthAndInboxNotifications asserts the Milestone-5 events fire with
// the right deep links (an expired session and a new uncategorized tx).
func TestNotificationsFireOnSyncEvents(t *testing.T) {
	f := fixtureEB()
	f.txnsPage1 = TransactionsResponse{Transactions: []Transaction{
		{EntryReference: "n-1", TransactionAmount: Amount{Currency: "EUR", Amount: "12.00"},
			CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-03-01",
			Creditor: &PartyIdentification{Name: "Odd Shop"}},
	}}
	n := &fakeNotifier{}
	svc, _ := newTestServiceN(t, f, n)
	ctx := context.Background()
	link, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err != nil {
		t.Fatal(err)
	}

	// needs_category fired after the first sync (new tx, no matching rule).
	events := n.events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event after first sync, got %d: %+v", len(events), events)
	}
	if events[0].Event != notify.EventNeedsCategory {
		t.Errorf("event = %s, want needs_category", events[0].Event)
	}
	if !strings.HasSuffix(events[0].Link, "/inbox") || events[0].Merchant != "odd shop" || events[0].AmountMinor != -1200 {
		t.Errorf("needs_category payload wrong: %+v", events[0])
	}

	// Now expire the session mid-flight: reauth_needed with a reconnect link.
	f.setSessionErr(&APIError{HTTPStatus: 400, Message: "expired", Code: "EXPIRED_SESSION"})
	if _, err := svc.SyncLink(ctx, link.ID); err == nil {
		t.Fatal("sync succeeded despite expired session")
	}
	events = n.events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	r := events[1]
	if r.Event != notify.EventReauthNeeded {
		t.Errorf("second event = %s, want reauth_needed", r.Event)
	}
	if !strings.Contains(r.Link, "/links/"+strconv.FormatInt(link.ID, 10)+"/reconnect") {
		t.Errorf("reauth link wrong: %s", r.Link)
	}
}

// TestRuleAndTransferClassification: a sync auto-categorizes by existing rule
// and flags internal transfers; resolving the inbox grows the rules so the
// NEXT sync categorizes new entries automatically.
func TestRuleAndTransferClassification(t *testing.T) {
	f := fixtureEB()
	f.details = Account{
		AccountID: AccountIdentification{Iban: "FI1234567890"}, Name: "Mock Personal",
		Product: "Mock", Currency: "EUR",
	}
	f.txnsPage1 = TransactionsResponse{Transactions: []Transaction{
		{EntryReference: "t-rule", TransactionAmount: Amount{Currency: "EUR", Amount: "23.40"},
			CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-02-01",
			Creditor: &PartyIdentification{Name: "S Market"}},
		{EntryReference: "t-xfer", TransactionAmount: Amount{Currency: "EUR", Amount: "150.00"},
			CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-02-02",
			Creditor: &PartyIdentification{Name: "Mock Personal"}},
		{EntryReference: "t-unknown", TransactionAmount: Amount{Currency: "EUR", Amount: "9.99"},
			CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-02-03",
			Creditor: &PartyIdentification{Name: "Mystery Store"}},
	}}
	svc, st := newTestService(t, f)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)

	// Pre-seed a rule (as if learned from a previous resolve).
	cats, _ := st.ListCategories(ctx, uid)
	groceries := cats[1].ID // Groceries
	if err := st.InsertRule(ctx, uid, groceries, "s market", "exact"); err != nil {
		t.Fatal(err)
	}

	link, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err != nil {
		t.Fatal(err)
	}

	txs, _ := st.RecentTransactions(ctx, uid, 0, 10, 0)
	byRef := map[string]store.Transaction{}
	for _, t := range txs {
		byRef[t.EntryReference] = t
	}

	ruleTx := byRef["t-rule"]
	if ruleTx.CategoryID == nil || *ruleTx.CategoryID != groceries || ruleTx.CategorySource != store.CatSourceRule {
		t.Errorf("rule tx not classified: %+v", ruleTx)
	}
	if ruleTx.MerchantNormalized != "s market" {
		t.Errorf("merchant_normalized = %q", ruleTx.MerchantNormalized)
	}

	xfer := byRef["t-xfer"]
	if !xfer.InternalTransfer {
		t.Error("own-account transaction not flagged as internal transfer")
	}
	if xfer.CategoryID == nil { // Transfers (internal) category exists in seeds
		t.Error("transfer not categorized")
	}

	unknown := byRef["t-unknown"]
	if unknown.CategoryID != nil {
		t.Errorf("unknown tx prematurely categorized: %+v", unknown)
	}

	// Resolve the unknown one through the inbox flow.
	pattern, err := svc.ResolveInbox(ctx, unknown.ID, groceries)
	if err != nil {
		t.Fatal(err)
	}
	if pattern != "mystery store" {
		t.Errorf("resolve pattern = %q", pattern)
	}

	// Next sync delivers a new transaction from the same merchant → the rule
	// (grown from the answer) categorizes it automatically.
	f.txnsAfter = []Transaction{
		{EntryReference: "t-mystery2", TransactionAmount: Amount{Currency: "EUR", Amount: "3.50"},
			CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-02-04",
			Creditor: &PartyIdentification{Name: "Mystery Store"}},
	}
	res, err := svc.SyncLink(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.TransactionsInserted != 1 {
		t.Errorf("second sync inserted %d, want 1", res.TransactionsInserted)
	}
	txs, _ = st.RecentTransactions(ctx, uid, 0, 10, 0)
	for _, tx := range txs {
		if tx.EntryReference == "t-mystery2" {
			if tx.CategoryID == nil || *tx.CategoryID != groceries {
				t.Errorf("rule-grown tx not categorized: %+v", tx)
			}
			break
		}
	}
	if n, _ := st.UncategorizedCount(ctx, uid); n != 0 {
		t.Errorf("uncategorized after resolution = %d, want 0", n)
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
