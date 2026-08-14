package store

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"
)

// InsertTransactions writes transactions for one account, skipping rows whose
// (user, account, entry_reference) already exist. Returns inserted/skipped
// counts for sync metrics.
func (s *Store) InsertTransactions(ctx context.Context, userID, accountID int64, txs []TransactionInput) (int64, int64, error) {
	var inserted, skipped int64
	if len(txs) == 0 {
		return 0, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("store: tx tx begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO transactions
			(user_id, account_id, entry_reference, booking_date, value_date, amount_minor,
			 currency, credit_debit, status, merchant_category_code, counterparty,
			 counterparty_account, remittance_information, note, balance_after_minor, raw)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, 0, fmt.Errorf("store: tx prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, t := range txs {
		ref := t.EntryReference
		if ref == "" {
			// Some banks omit entry_reference; build a stable pseudo-ref so
			// re-syncs still dedupe.
			h := md5.Sum([]byte(strings.Join([]string{
				t.BookingDate, t.Counterparty, fmt.Sprint(t.AmountMinor), t.Currency,
			}, "|")))
			ref = fmt.Sprintf("auto-%x", h[:8])
		}
		res, err := stmt.ExecContext(ctx, userID, accountID, ref, t.BookingDate, t.ValueDate,
			t.AmountMinor, t.Currency, t.CreditDebit, t.Status, t.MerchantCategoryCode,
			t.Counterparty, t.CounterpartyAccount, t.RemittanceInformation, t.Note,
			t.BalanceAfterMinor, t.Raw)
		if err != nil {
			return inserted, skipped, fmt.Errorf("store: insert tx %s: %w", ref, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return inserted, skipped, err
		}
		if n > 0 {
			inserted++
		} else {
			skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return inserted, skipped, fmt.Errorf("store: tx commit: %w", err)
	}
	return inserted, skipped, nil
}

const txCols = `id, user_id, account_id, entry_reference, booking_date, value_date, amount_minor,
	currency, credit_debit, status, merchant_category_code, counterparty,
	counterparty_account, remittance_information, note, balance_after_minor, raw, created_at`

func scanTx(row interface{ Scan(...any) error }) (Transaction, error) {
	var t Transaction
	err := row.Scan(&t.ID, &t.UserID, &t.AccountID, &t.EntryReference, &t.BookingDate,
		&t.ValueDate, &t.AmountMinor, &t.Currency, &t.CreditDebit, &t.Status,
		&t.MerchantCategoryCode, &t.Counterparty, &t.CounterpartyAccount,
		&t.RemittanceInformation, &t.Note, &t.BalanceAfterMinor, &t.Raw, &t.CreatedAt)
	return t, err
}

// RecentTransactions returns the latest transactions for a user, optionally
// filtered by account.
func (s *Store) RecentTransactions(ctx context.Context, userID, accountID int64, limit int) ([]Transaction, error) {
	q := `SELECT ` + txCols + ` FROM transactions WHERE user_id = ?`
	var args []any
	args = append(args, userID)
	if accountID > 0 {
		q += ` AND account_id = ?`
		args = append(args, accountID)
	}
	q += ` ORDER BY booking_date DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: recent transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Transaction
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CountTransactions(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM transactions WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}
