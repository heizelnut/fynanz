package sync

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	return newTestServiceH(t, f, n, 90)
}

// newTestServiceH is newTestServiceN with an explicit SYNC_HISTORY_DAYS
// equivalent (the window the sync requests on every run).
func newTestServiceH(t *testing.T, f *fakeEB, n notify.Notifier, historyDays int) (*Service, *store.Store) {
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
		"http://localhost:8080/links/callback", "personal", historyDays, 30)
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

	txs, err := st.RecentTransactions(ctx, link.UserID, store.TxFilter{}, 10, 0)
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
	if err := st.InsertRule(ctx, uid, groceries, "s market", "exact", "merchant"); err != nil {
		t.Fatal(err)
	}

	link, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err != nil {
		t.Fatal(err)
	}

	txs, _ := st.RecentTransactions(ctx, uid, store.TxFilter{}, 10, 0)
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
	txs, _ = st.RecentTransactions(ctx, uid, store.TxFilter{}, 10, 0)
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

// TestP2PAlwaysInbox: person-to-person transfers are never auto-categorized,
// even when a merchant rule would match the friend's name (the point of P2P
// detection: every such transaction lands in the inbox). Resolving one writes
// a fuzzy DESCRIPTION rule, which then auto-categorizes non-P2P transactions
// carrying the same reference text.
func TestP2PAlwaysInbox(t *testing.T) {
	f := fixtureEB()
	f.details = Account{
		AccountID: AccountIdentification{Iban: "FI1234567890"}, Name: "Mock Personal",
		Product: "Mock", Currency: "EUR",
	}
	f.txnsPage1 = TransactionsResponse{Transactions: []Transaction{
		// A friend sending money: no MCC, "reference | From <Name>" narration
		// (the real-feed P2P fingerprint).
		{EntryReference: "t-p2p", TransactionAmount: Amount{Currency: "EUR", Amount: "75.00"},
			CreditDebitIndicator: "CRDT", Status: "BOOK", BookingDate: "2025-02-10",
			Debtor:                &PartyIdentification{Name: "Mario Rossi"},
			RemittanceInformation: []string{"affitto | From Mario Rossi"}},
		// A merchant with the same reference text: no From/To, no SEPA order
		// markers, so it is NOT p2p and can be description-rule matched.
		{EntryReference: "t-rent", TransactionAmount: Amount{Currency: "EUR", Amount: "600.00"},
			CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-02-09",
			Creditor:              &PartyIdentification{Name: "Immobiliare Vera"},
			RemittanceInformation: []string{"affitto luglio 2025"}},
	}}
	svc, st := newTestService(t, f)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	cats, _ := st.ListCategories(ctx, uid)
	income := cats[0].ID // Income

	// A merchant rule on the friend's name exists and must NOT win.
	if err := st.InsertRule(ctx, uid, income, "mario rossi", "exact", "merchant"); err != nil {
		t.Fatal(err)
	}

	link, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err != nil {
		t.Fatal(err)
	}

	txs, _ := st.RecentTransactions(ctx, uid, store.TxFilter{}, 10, 0)
	byRef := map[string]store.Transaction{}
	for _, tx := range txs {
		byRef[tx.EntryReference] = tx
	}
	p2p := byRef["t-p2p"]
	if !p2p.IsP2P {
		t.Error("friend transfer not flagged P2P")
	}
	if p2p.CategoryID != nil || p2p.CategorySource != "" {
		t.Errorf("P2P tx auto-categorized despite merchant rule: %+v", p2p)
	}
	rent := byRef["t-rent"]
	if rent.IsP2P {
		t.Error("merchant without transfer signal flagged P2P")
	}
	if rent.CategoryID != nil {
		t.Errorf("merchant row prematurely categorized: %+v", rent)
	}

	// Resolve the friend transfer: category = manual, and a fuzzy description
	// rule is grown — not a merchant rule on the friend's name.
	pattern, err := svc.ResolveInbox(ctx, p2p.ID, income)
	if err != nil {
		t.Fatal(err)
	}
	if pattern != "affitto" {
		t.Errorf("P2P resolve pattern = %q, want the reference text", pattern)
	}
	got, _ := st.GetTransaction(ctx, uid, p2p.ID)
	if got.CategorySource != store.CatSourceManual {
		t.Errorf("P2P answer source = %q, want manual (so reclassify keeps it)", got.CategorySource)
	}
	rules, _ := st.ListRules(ctx, uid)
	var descRule *store.Rule
	for i := range rules {
		if rules[i].Field == "description" && rules[i].Pattern == "affitto" {
			descRule = &rules[i]
		}
	}
	if descRule == nil || descRule.MatchType != "fuzzy" {
		t.Errorf("expected fuzzy description rule, got %+v", rules)
	}

	// Next sync: another merchant carrying "affitto" in its reference is
	// auto-categorized by the description rule; the P2P flow never applies.
	f.txnsAfter = []Transaction{
		{EntryReference: "t-rent2", TransactionAmount: Amount{Currency: "EUR", Amount: "600.00"},
			CreditDebitIndicator: "DBIT", Status: "BOOK", BookingDate: "2025-02-11",
			Creditor:              &PartyIdentification{Name: "Immobiliare Vera"},
			RemittanceInformation: []string{"affitto settembre"}},
	}
	if _, err := svc.SyncLink(ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	txs, _ = st.RecentTransactions(ctx, uid, store.TxFilter{}, 10, 0)
	var rent2 *store.Transaction
	for i := range txs {
		if txs[i].EntryReference == "t-rent2" {
			rent2 = &txs[i]
		}
	}
	if rent2 == nil {
		t.Fatal("t-rent2 not synced")
	}
	if rent2.CategoryID == nil || *rent2.CategoryID != income || rent2.CategorySource != store.CatSourceRule {
		t.Errorf("description rule did not categorize merchant: %+v", rent2)
	}
	// The original merchant row (same description, synced pre-rule) is
	// reclassified by the startup-style pass at end of sync.
	if n, _ := st.UncategorizedCount(ctx, uid); n != 0 {
		t.Errorf("uncategorized = %d, want 0 (reclassify should apply the new rule)", n)
	}
}

// TestASPSPResolution: StartLink must take the bank's (name, country) from
// the live /aspsps list, not the hardcoded defaults (which differ per
// environment), and cap the requested consent at the bank's maximum.
func TestASPSPResolution(t *testing.T) {
	f := fixtureEB()
	maxConsent := int64(90 * 86400) // 90 days
	f.aspsps = []ASPSPData{
		{Name: "Revolut", Country: "LT", PSUTypes: []string{"personal"}, MaxConsentValidity: &maxConsent},
		{Name: "Mock ASPSP", Country: "FI", MaxConsentValidity: &maxConsent},
	}
	svc, st := newTestService(t, f)
	ctx := context.Background()

	link, _, err := svc.StartLink(ctx, "revolut")
	if err != nil {
		t.Fatal(err)
	}
	if link.AspspName != "Revolut" || link.AspspCountry != "LT" {
		t.Errorf("aspsp not resolved from /aspsps: %+v", link)
	}
	f.mu.Lock()
	req := f.lastAuthReq
	f.mu.Unlock()
	if req.Aspsp.Name != "Revolut" {
		t.Errorf("auth sent to %q, want Revolut", req.Aspsp.Name)
	}
	until, err := time.Parse("2006-01-02T15:04:05.999999999-07:00", req.Access.ValidUntil)
	if err != nil {
		t.Fatal(err)
	}
	capD := time.Duration(maxConsent) * time.Second
	if d := time.Until(until); d > capD || d <= 0 {
		t.Errorf("consent validity %v not capped at bank max %v", d, capD)
	}

	// Fallback: unknown bank key keeps defaults (still resolvable).
	link2, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if link2.AspspName != "Mock ASPSP" {
		t.Errorf("sandbox fallback wrong: %+v", link2)
	}
	_ = st
}

// TestRemoveLink: soft removal keeps data and hides the link; purge deletes
// the transactions, accounts and the link row itself.
func TestRemoveLink(t *testing.T) {
	svc, st := newTestService(t, fixtureEB())
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	link, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err != nil {
		t.Fatal(err)
	}
	txBefore, _ := st.CountTransactions(ctx, uid, store.TxFilter{})

	// Soft remove: session revoked (fake DELETE /sessions), data kept, link hidden.
	if err := svc.RemoveLink(ctx, link.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetLink(ctx, link.ID, uid)
	if got.Status != store.LinkRemoved {
		t.Errorf("status = %q, want removed", got.Status)
	}
	if n, _ := st.CountTransactions(ctx, uid, store.TxFilter{}); n != txBefore {
		t.Errorf("soft remove deleted %d transactions, want %d kept", n, txBefore)
	}
	if links, _ := st.ListLinks(ctx, uid); len(links) != 0 {
		t.Errorf("removed link still listed: %+v", links)
	}
	if active, _ := st.ActiveLinks(ctx, uid); len(active) != 0 {
		t.Error("removed link still active")
	}

	// Purge: everything gone.
	if err := svc.RemoveLink(ctx, link.ID, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountTransactions(ctx, uid, store.TxFilter{}); n != 0 {
		t.Errorf("purge left %d transactions", n)
	}
	if accs, _ := st.ListAccounts(ctx, uid); len(accs) != 0 {
		t.Errorf("purge left %d accounts", len(accs))
	}
	if ghost, _ := st.GetLink(ctx, link.ID, uid); ghost != nil {
		t.Error("purge left the link row")
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

// TestRateLimitIsSilent: a 429 ASPSP_RATE_LIMIT_EXCEEDED during sync must not
// fire notifications, must not expire the link, and the next sync retries.
func TestRateLimitIsSilent(t *testing.T) {
	f := fixtureEB()
	f.detailsErr = &APIError{HTTPStatus: http.StatusTooManyRequests, Message: "slow down", Code: "ASPSP_RATE_LIMIT_EXCEEDED", Detail: "rate"}
	n := &fakeNotifier{}
	svc, st := newTestServiceN(t, f, n)
	ctx := context.Background()
	link, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	// CompleteLink runs the first sync — which hits the 429 on account details.
	err = svc.CompleteLink(ctx, link.State, "code-123")
	if err == nil {
		t.Fatal("expected the 429 to propagate")
	}
	if IsRetryableError(err) && !IsSessionError(err) {
		// exactly the case: retryable, but no session damage
	} else {
		t.Errorf("err not classified as retryable: %v", err)
	}
	got, _ := st.GetLink(ctx, link.ID, link.UserID)
	if got.Status != store.LinkActive {
		t.Errorf("link status = %q, want %q (rate limits must not expire links)", got.Status, store.LinkActive)
	}
	if events := n.events(); len(events) != 0 {
		t.Errorf("rate-limited sync sent %d notifications, want 0: %+v", len(events), events)
	}
	// Link auth state is intact: the sync itself merely failed transiently.
	if got.SessionID == "" {
		t.Error("session lost during 429")
	}
}

// TestTransactionsWindowFallback: when the bank rejects a deep history window
// (WRONG_TRANSACTIONS_PERIOD — e.g. Mediolanum capping PSD2 depth at ~90
// days), SyncLink retries with shorter windows and succeeds with the largest
// one the bank accepts, instead of failing the whole sync.
func TestTransactionsWindowFallback(t *testing.T) {
	f := fixtureEB()
	minFrom := time.Now().UTC().AddDate(0, 0, -90).Format("2006-01-02")
	f.minTxFrom = minFrom
	// A year-long window (the new default) gets rejected outright…
	svc, st := newTestServiceH(t, f, nil, 365)
	ctx := context.Background()
	link, _, err := svc.StartLink(ctx, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteLink(ctx, link.State, "code-123"); err != nil {
		t.Fatal(err)
	}
	res, err := svc.SyncLink(ctx, link.ID)
	if err != nil {
		t.Fatalf("sync failed on capped bank: %v", err)
	}
	// CompleteLink already synced once (its window also fell back and
	// inserted the fixture rows); the explicit SyncLink re-fetches the same
	// window and dedupes. Total stored = the fixture's 2 rows regardless.
	total, err := st.CountTransactions(ctx, link.UserID, store.TxFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("stored transactions = %d, want 2", total)
	}
	if res.TransactionsInserted != 0 || res.TransactionsSkipped != 2 {
		t.Errorf("re-sync = %d inserted / %d skipped, want 0/2", res.TransactionsInserted, res.TransactionsSkipped)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.txnFroms) < 2 {
		t.Fatalf("expected ≥2 transaction fetches (rejected + fallback), got %d: %v", len(f.txnFroms), f.txnFroms)
	}
	if f.txnFroms[0] >= minFrom {
		t.Errorf("first attempt should have asked for a window older than the cap: %s vs %s", f.txnFroms[0], minFrom)
	}
	last := f.txnFroms[len(f.txnFroms)-1]
	if last < minFrom {
		t.Errorf("no fallback window was accepted: last = %s, cap = %s", last, minFrom)
	}
}
