package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CategorySpend is one row of the spend-by-category aggregation.
type CategorySpend struct {
	CategoryID      int64 // 0 when the transaction has no category yet
	Name            string
	TotalMinor      int64 // positive: |sum of expenses|
	IsUncategorized bool
}

// SpendByCategory totals expenses (amount < 0, internal transfers excluded)
// per category with booking_date >= dateFrom, one currency, most-spend first.
// Uncategorized expenses are grouped under their own row so no spend silently
// disappears from the dashboard.
func (s *Store) SpendByCategory(ctx context.Context, userID int64, currency, dateFrom string) ([]CategorySpend, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(c.id, 0), COALESCE(c.name, 'Uncategorized'), SUM(-t.amount_minor)
		FROM transactions t LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.user_id = ? AND t.currency = ? AND t.internal_transfer = 0
		  AND t.amount_minor < 0 AND t.booking_date >= ? AND t.booking_date != ''
		GROUP BY c.id
		ORDER BY SUM(-t.amount_minor) DESC`, userID, currency, dateFrom)
	if err != nil {
		return nil, fmt.Errorf("store: spend by category: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CategorySpend
	for rows.Next() {
		var cs CategorySpend
		if err := rows.Scan(&cs.CategoryID, &cs.Name, &cs.TotalMinor); err != nil {
			return nil, err
		}
		cs.IsUncategorized = cs.CategoryID == 0
		out = append(out, cs)
	}
	return out, rows.Err()
}

// MonthFlow is one month of income (CRDT) vs expense (DBIT), both absolute.
type MonthFlow struct {
	Month        string // "2026-08"
	IncomeMinor  int64
	ExpenseMinor int64
}

// IncomeExpenseByMonth sums income and expenses per calendar month for one
// currency, transfers excluded, months ascending, from dateFrom.
func (s *Store) IncomeExpenseByMonth(ctx context.Context, userID int64, currency, dateFrom string) ([]MonthFlow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m', booking_date) AS month,
		       SUM(CASE WHEN amount_minor > 0 THEN amount_minor ELSE 0 END),
		       SUM(CASE WHEN amount_minor < 0 THEN -amount_minor ELSE 0 END)
		FROM transactions
		WHERE user_id = ? AND currency = ? AND internal_transfer = 0
		  AND booking_date >= ? AND booking_date != ''
		GROUP BY month
		ORDER BY month`, userID, currency, dateFrom)
	if err != nil {
		return nil, fmt.Errorf("store: income/expense by month: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MonthFlow
	for rows.Next() {
		var m MonthFlow
		if err := rows.Scan(&m.Month, &m.IncomeMinor, &m.ExpenseMinor); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DailyAmount is the net movement (sum of amount_minor, ALL transactions
// including internal transfers — balance is balance) on one booking date.
type DailyAmount struct {
	Date        string
	AmountMinor int64
}

// DailyAmountSums groups movements per booking date for one account,
// ascending, for dates >= dateFrom.
func (s *Store) DailyAmountSums(ctx context.Context, userID, accountID int64, dateFrom string) ([]DailyAmount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT booking_date, SUM(amount_minor) FROM transactions
		WHERE user_id = ? AND account_id = ? AND booking_date >= ? AND booking_date != ''
		GROUP BY booking_date ORDER BY booking_date`, userID, accountID, dateFrom)
	if err != nil {
		return nil, fmt.Errorf("store: daily amounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DailyAmount
	for rows.Next() {
		var d DailyAmount
		if err := rows.Scan(&d.Date, &d.AmountMinor); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PrimaryCurrency returns the currency with the most transactions for the
// user (v1 shows native currency, no FX — AGENTS.md §4), "" when none.
func (s *Store) PrimaryCurrency(ctx context.Context, userID int64) (string, error) {
	var cur string
	err := s.db.QueryRowContext(ctx, `
		SELECT currency FROM transactions WHERE user_id = ?
		GROUP BY currency ORDER BY COUNT(*) DESC LIMIT 1`, userID).Scan(&cur)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil // no transactions yet
		}
		return ", ", fmt.Errorf("store: primary currency: %w", err)
	}
	return cur, nil
}

// IncomeByCategory mirrors SpendByCategory for credits (amount_minor > 0),
// grouped per category for the finance metrics.
func (s *Store) IncomeByCategory(ctx context.Context, userID int64, currency, dateFrom string) ([]CategorySpend, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(c.id, 0), COALESCE(c.name, 'Uncategorized'), SUM(t.amount_minor)
		FROM transactions t LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.user_id = ? AND t.currency = ? AND t.internal_transfer = 0
		  AND t.amount_minor > 0 AND t.booking_date >= ? AND t.booking_date != ''
		GROUP BY c.id
		ORDER BY SUM(t.amount_minor) DESC`, userID, currency, dateFrom)
	if err != nil {
		return nil, fmt.Errorf("store: income by category: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CategorySpend
	for rows.Next() {
		var cs CategorySpend
		if err := rows.Scan(&cs.CategoryID, &cs.Name, &cs.TotalMinor); err != nil {
			return nil, err
		}
		cs.IsUncategorized = cs.CategoryID == 0
		out = append(out, cs)
	}
	return out, rows.Err()
}

// CurrencyCount is a transaction-count/currency pair.
type CurrencyCount struct {
	Currency string
	N        int64
}

// CountByCurrency returns transaction counts per currency, most common first.
func (s *Store) CountByCurrency(ctx context.Context, userID int64) ([]CurrencyCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT currency, COUNT(*) FROM transactions WHERE user_id = ?
		 GROUP BY currency ORDER BY COUNT(*) DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: count by currency: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CurrencyCount
	for rows.Next() {
		var c CurrencyCount
		if err := rows.Scan(&c.Currency, &c.N); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountRules returns the number of categorization rules.
func (s *Store) CountRules(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM rules WHERE user_id = ?`, userID).Scan(&n)
	if n == 0 && err != nil {
		return 0, err
	}
	return n, nil
}
