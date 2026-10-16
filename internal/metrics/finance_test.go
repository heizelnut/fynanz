package metrics

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"fynanz/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "fin.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestFinanceCollector(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)
	link, _ := st.CreateLink(ctx, uid, "sandbox", "Mock ASPSP", "FI")
	ids, _ := st.UpsertAccounts(ctx, uid, link.ID, []store.AccountInput{{UID: "u1", IdentificationHash: "h1", Currency: "EUR"}})

	g := func(c int64) *int64 { return &c }
	bal := int64(554800) // mocked current balance
	_ = bal
	cats, _ := st.ListCategories(ctx, uid)
	// Seed relative to the current month: the collector windows on the current
	// month, so fixed dates make this test time-bomb as the calendar advances.
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	cur := func(day int) string { return monthStart.AddDate(0, 0, day-1).Format("2006-01-02") }
	prevMonth := monthStart.AddDate(0, 0, -1).Format("2006-01-02")
	ins, _, _ := st.InsertTransactions(ctx, uid, ids["u1"], []store.TransactionInput{
		{EntryReference: "m1", BookingDate: cur(5), AmountMinor: -1234, Currency: "EUR", CreditDebit: "DBIT", CategoryID: g(cats[1].ID), CategorySource: store.CatSourceRule, MerchantNormalized: "m"},
		{EntryReference: "m2", BookingDate: cur(6), AmountMinor: 50000, Currency: "EUR", CreditDebit: "CRDT", CategoryID: g(cats[0].ID), CategorySource: store.CatSourceRule, MerchantNormalized: "m"},
		{EntryReference: "m3", BookingDate: prevMonth, AmountMinor: -999, Currency: "EUR", CreditDebit: "DBIT", CategoryID: g(cats[1].ID), CategorySource: store.CatSourceRule, MerchantNormalized: "m"},
		{EntryReference: "m4", BookingDate: cur(1), AmountMinor: -111, Currency: "USD", CreditDebit: "DBIT", CategoryID: g(cats[4].ID), CategorySource: store.CatSourceRule, MerchantNormalized: "m"},
	})
	if ins != 4 {
		t.Fatalf("inserted %d, want 4", ins)
	}
	if err := st.InsertRule(ctx, uid, cats[1].ID, "pat", "exact", "merchant"); err != nil {
		t.Fatal(err)
	}
	_ = st.SetSetting(ctx, uid, "x", "y")
	// set a balance on the account (simulate a fetched balance)
	if _, err := st.DB().ExecContext(ctx, `UPDATE accounts SET balance_minor = 554800, balance_currency='EUR' WHERE id = ?`, ids["u1"]); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewFinanceCollector(st))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]float64{}
	for _, mf := range mfs {
		name := mf.GetName()
		if got[name] == nil {
			got[name] = map[string]float64{}
		}
		for _, m := range mf.GetMetric() {
			lbls := []string{}
			for _, lp := range m.GetLabel() {
				lbls = append(lbls, lp.GetName()+"="+lp.GetValue())
			}
			sort.Strings(lbls)
			got[name][strings.Join(lbls, ",")] = m.GetGauge().GetValue()
		}
	}

	// Current-month EUR expenses: 12.34 (previous-month -9.99 excluded by the window).
	exp := got["fynanz_finance_expenses"]
	if exp["category=Groceries,currency=EUR"] != 1234 {
		t.Errorf("current-month groceries expense = %v, want 1234", exp["category=Groceries,currency=EUR"])
	}
	if exp["category=Utilities & Bills,currency=USD"] != 111 {
		t.Errorf("USD category missing: %+v", exp)
	}
	inc := got["fynanz_finance_income"]
	if inc["category=Income,currency=EUR"] != 50000 {
		t.Errorf("income = %v, want 50000", inc["category=Income,currency=EUR"])
	}
	if got["fynanz_finance_net"]["currency=EUR"] != 50000-1234 {
		t.Errorf("net EUR = %v", got["fynanz_finance_net"]["currency=EUR"])
	}
	if got["fynanz_finance_transactions"]["currency=EUR"] != 3 || got["fynanz_finance_transactions"]["currency=USD"] != 1 {
		t.Errorf("tx counts wrong: %+v", got["fynanz_finance_transactions"])
	}
	if got["fynanz_finance_rules"][""] != 1 {
		t.Errorf("rules = %v, want 1", got["fynanz_finance_rules"])
	}
	if got["fynanz_finance_balance"]["account=u1,currency=EUR"] != 554800 {
		t.Errorf("balance wrong: %+v", got["fynanz_finance_balance"])
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
