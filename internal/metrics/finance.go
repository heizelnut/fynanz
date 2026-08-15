package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"fynanz/internal/store"
)

// FinanceSource is the store surface the finance collector needs. Keeping it
// an interface lets the collector be tested against the real store without
// importing the world.
type FinanceSource interface {
	EnsureDefaultUser(ctx context.Context) (int64, error)
	SpendByCategory(ctx context.Context, userID int64, currency, dateFrom string) ([]store.CategorySpend, error)
	IncomeByCategory(ctx context.Context, userID int64, currency, dateFrom string) ([]store.CategorySpend, error)
	CountByCurrency(ctx context.Context, userID int64) ([]store.CurrencyCount, error)
	ListAccounts(ctx context.Context, userID int64) ([]store.Account, error)
	CountRules(ctx context.Context, userID int64) (int64, error)
}

// FinanceCollector exposes dimensioned, CURRENT-state money gauges computed
// from SQLite at scrape time (AGENTS.md §8). History (month-over-month)
// stays in the dashboard/SQL where it belongs — Prometheus would be series
// explosion. Amounts are in minor units (e.g. EUR: ×0.01).
type FinanceCollector struct {
	src FinanceSource

	expensesDesc *prometheus.Desc
	incomeDesc   *prometheus.Desc
	netDesc      *prometheus.Desc
	balanceDesc  *prometheus.Desc
	txsDesc      *prometheus.Desc
	rulesDesc    *prometheus.Desc
}

func NewFinanceCollector(src FinanceSource) *FinanceCollector {
	labels := func(subsystem, help string, labelNames ...string) *prometheus.Desc {
		return prometheus.NewDesc("fynanz_"+subsystem, help, labelNames, nil)
	}
	return &FinanceCollector{
		src:          src,
		expensesDesc: labels("finance_expenses", "Expenses in the current calendar month, in minor units (transfers excluded).", "category", "currency"),
		incomeDesc:   labels("finance_income", "Income in the current calendar month, in minor units (transfers excluded).", "category", "currency"),
		netDesc:      labels("finance_net", "Income minus expenses in the current calendar month, in minor units.", "currency"),
		balanceDesc:  labels("finance_balance", "Latest fetched account balance, in minor units.", "account", "currency"),
		txsDesc:      labels("finance_transactions", "Total transactions stored per currency.", "currency"),
		rulesDesc:    labels("finance_rules", "Categorization rules learned."),
	}
}

func (c *FinanceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.expensesDesc
	ch <- c.incomeDesc
	ch <- c.netDesc
	ch <- c.balanceDesc
	ch <- c.txsDesc
	ch <- c.rulesDesc
}

// Collect runs the same queries the dashboard uses, at scrape time.
func (c *FinanceCollector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()
	userID, err := c.src.EnsureDefaultUser(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")

	counts, err := c.src.CountByCurrency(ctx, userID)
	if err != nil {
		counts = nil
	}
	for _, cc := range counts {
		emit(ch, c.txsDesc, float64(cc.N), cc.Currency)
		expenses, err1 := c.src.SpendByCategory(ctx, userID, cc.Currency, monthStart)
		income, err2 := c.src.IncomeByCategory(ctx, userID, cc.Currency, monthStart)
		var totExp, totInc float64
		if err1 == nil {
			for _, e := range expenses {
				if e.IsTransfer {
					continue // money movement, not spending (help text: transfers excluded)
				}
				emit(ch, c.expensesDesc, float64(e.TotalMinor), e.Name, cc.Currency)
				totExp += float64(e.TotalMinor)
			}
		}
		if err2 == nil {
			for _, e := range income {
				emit(ch, c.incomeDesc, float64(e.TotalMinor), e.Name, cc.Currency)
				totInc += float64(e.TotalMinor)
			}
		}
		emit(ch, c.netDesc, totInc-totExp, cc.Currency)
	}

	accounts, err := c.src.ListAccounts(ctx, userID)
	if err == nil {
		for _, a := range accounts {
			if a.BalanceMinor != nil {
				emit(ch, c.balanceDesc, float64(*a.BalanceMinor), a.DisplayName(), a.Currency)
			}
		}
	}
	if n, err := c.src.CountRules(ctx, userID); err == nil {
		emit(ch, c.rulesDesc, float64(n))
	}
}

func emit(ch chan<- prometheus.Metric, desc *prometheus.Desc, v float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, labels...)
}

// RegisterFinanceCollector wires the collector into the default registry
// served at /metrics.
func RegisterFinanceCollector(src FinanceSource) {
	prometheus.MustRegister(NewFinanceCollector(src))
}
