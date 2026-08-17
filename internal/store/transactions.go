package store

import (
	"context"
	"crypto/md5"
	"database/sql"
	"errors"
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
			 counterparty_account, remittance_information, note, balance_after_minor, raw,
			 merchant_normalized, category_id, category_source, internal_transfer, is_p2p)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
			t.BalanceAfterMinor, t.Raw, t.MerchantNormalized, t.CategoryID,
			t.CategorySource, t.InternalTransfer, t.IsP2P)
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

const txCols = `t.id, t.user_id, t.account_id, t.entry_reference, t.booking_date, t.value_date,
	t.amount_minor, t.currency, t.credit_debit, t.status, t.merchant_category_code,
	t.counterparty, t.counterparty_account, t.remittance_information, t.note,
	t.balance_after_minor, t.raw, t.created_at, t.merchant_normalized, t.category_id,
	t.category_source, t.internal_transfer, t.is_p2p`

const txFrom = `FROM transactions t LEFT JOIN categories c ON c.id = t.category_id`

// UncategorizedCat is the sentinel for the "no category yet" filter value.
const UncategorizedCat = -1

// TxFilter narrows transaction listings: account, category
// (UncategorizedCat for "no category"), booking-date range, a P2P flag and a
// free-text query on merchant/counterparty. Zero values mean "all".
//
// P2P is tri-state: P2PAny = both kinds, P2POnly = only person-to-person,
// P2PExcluded = only non-P2P rows.
type TxFilter struct {
	AccountID  int64
	CategoryID int64
	DateFrom   string
	DateTo     string
	Query      string
	P2P        int
}

// P2P filter values (TxFilter.P2P).
const (
	P2PAny      = 0
	P2POnly     = 1
	P2PExcluded = -1
)

// txWhere builds the shared WHERE clause for listings and counts.
func txWhere(userID int64, f TxFilter) (string, []any) {
	q := ` WHERE t.user_id = ?`
	args := []any{userID}
	if f.AccountID > 0 {
		q += ` AND t.account_id = ?`
		args = append(args, f.AccountID)
	}
	if f.CategoryID == UncategorizedCat {
		q += ` AND t.category_id IS NULL`
	} else if f.CategoryID != 0 {
		q += ` AND t.category_id = ?`
		args = append(args, f.CategoryID)
	}
	if f.DateFrom != "" {
		q += ` AND t.booking_date >= ?`
		args = append(args, f.DateFrom)
	}
	if f.DateTo != "" {
		q += ` AND t.booking_date <= ?`
		args = append(args, f.DateTo)
	}
	if f.Query != "" {
		pat := "%" + likeEscape(f.Query) + "%"
		q += ` AND (t.merchant_normalized LIKE ? ESCAPE '\' OR t.counterparty LIKE ? ESCAPE '\')`
		args = append(args, pat, pat)
	}
	switch f.P2P {
	case P2POnly:
		q += ` AND t.is_p2p = 1`
	case P2PExcluded:
		q += ` AND t.is_p2p = 0`
	}
	return q, args
}

// likeEscape neutralises LIKE wildcards so user input matches literally.
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// RecentTransactions returns transactions for a user, newest first with a
// stable tiebreaker (booking_date desc, id desc) so pagination is consistent.
func (s *Store) RecentTransactions(ctx context.Context, userID int64, f TxFilter, limit, offset int) ([]Transaction, error) {
	q := `SELECT ` + txCols + `, COALESCE(c.name, '') ` + txFrom
	where, args := txWhere(userID, f)
	q += where + ` ORDER BY t.booking_date DESC, t.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: recent transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Transaction
	for rows.Next() {
		t, err := scanTxJoined(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// scanTxJoined reads a row produced by `txCols + ", COALESCE(c.name, ”)"` —
// one extra column after the 23 tx fields (database/sql Scan requires an
// exact dest count).
func scanTxJoined(row interface{ Scan(...any) error }) (Transaction, error) {
	var t Transaction
	err := row.Scan(&t.ID, &t.UserID, &t.AccountID, &t.EntryReference, &t.BookingDate,
		&t.ValueDate, &t.AmountMinor, &t.Currency, &t.CreditDebit, &t.Status,
		&t.MerchantCategoryCode, &t.Counterparty, &t.CounterpartyAccount,
		&t.RemittanceInformation, &t.Note, &t.BalanceAfterMinor, &t.Raw, &t.CreatedAt,
		&t.MerchantNormalized, &t.CategoryID, &t.CategorySource, &t.InternalTransfer,
		&t.IsP2P, &t.CategoryName)
	return t, err
}

// AllTransactions returns every transaction matching the filter (no page
// limit) — used by the CSV export.
func (s *Store) AllTransactions(ctx context.Context, userID int64, f TxFilter) ([]Transaction, error) {
	q := `SELECT ` + txCols + `, COALESCE(c.name, '') ` + txFrom
	where, args := txWhere(userID, f)
	q += where + ` ORDER BY t.booking_date DESC, t.id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: all transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Transaction
	for rows.Next() {
		t, err := scanTxJoined(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountTransactions counts transactions matching the filter (pagination).
func (s *Store) CountTransactions(ctx context.Context, userID int64, f TxFilter) (int64, error) {
	where, args := txWhere(userID, f)
	var n int64
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM transactions t`+where, args...).Scan(&n)
	return n, err
}

// SetTransactionCategory assigns a category (or clears it with nil) and
// records where the assignment came from.
func (s *Store) SetTransactionCategory(ctx context.Context, userID, txID int64, catID *int64, source string) error {
	n, err := s.db.ExecContext(ctx,
		`UPDATE transactions SET category_id = ?, category_source = ? WHERE id = ? AND user_id = ?`,
		catID, source, txID, userID)
	if err != nil {
		return fmt.Errorf("store: categorize tx %d: %w", txID, err)
	}
	if affected, _ := n.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CategoryUpdate is one reclassification of an existing row (backfill job).
type CategoryUpdate struct {
	TxID               int64
	MerchantNormalized string
	CategoryID         *int64
	Source             string
	InternalTransfer   bool
}

// ApplyCategoryUpdates writes a batch of reclassifications in one tx.
func (s *Store) ApplyCategoryUpdates(ctx context.Context, userID int64, updates []CategoryUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx,
			`UPDATE transactions SET merchant_normalized = ?, category_id = ?,
				category_source = ?, internal_transfer = ? WHERE id = ? AND user_id = ?`,
			u.MerchantNormalized, u.CategoryID, u.Source, u.InternalTransfer, u.TxID, userID); err != nil {
			return fmt.Errorf("store: apply category update %d: %w", u.TxID, err)
		}
	}
	return tx.Commit()
}

// ListUncategorized returns transactions without a category (the inbox), in
// booking order (most recent first), paginated.
func (s *Store) ListUncategorized(ctx context.Context, userID int64, limit, offset int) ([]Transaction, error) {
	q := `SELECT ` + txCols + `, COALESCE(c.name, '') ` + txFrom + `
		WHERE t.user_id = ? AND t.category_id IS NULL
		ORDER BY t.booking_date DESC, t.id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: uncategorized list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Transaction
	for rows.Next() {
		t, err := scanTxJoined(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetP2PFlag updates the person-to-person flag on one transaction (used by
// the reclassify pass that re-derives the flag after detector changes).
func (s *Store) SetP2PFlag(ctx context.Context, userID, txID int64, p2p bool) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE transactions SET is_p2p = ? WHERE id = ? AND user_id = ?`,
		p2p, txID, userID); err != nil {
		return fmt.Errorf("store: set p2p flag %d: %w", txID, err)
	}
	return nil
}

// UncategorizeP2PRules removes rule-derived categories from person-to-person
// transactions. A rule can never auto-categorize a P2P transfer — only
// manual answers stick — so any P2P row carrying a 'rule' category is a
// leftover from before P2P detection and returns to the inbox.
func (s *Store) UncategorizeP2PRules(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE transactions SET category_id = NULL, category_source = ''
		 WHERE user_id = ? AND is_p2p = 1 AND category_source = ?`,
		userID, CatSourceRule)
	if err != nil {
		return fmt.Errorf("store: uncategorize p2p: %w", err)
	}
	return nil
}

// GetTransaction returns one of the user's transactions by id.
func (s *Store) GetTransaction(ctx context.Context, userID, txID int64) (Transaction, error) {
	q := `SELECT ` + txCols + `, COALESCE(c.name, '') ` + txFrom + ` WHERE t.id = ? AND t.user_id = ?`
	t, err := scanTxJoined(s.queryRow(ctx, q, txID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return t, sql.ErrNoRows
	}
	if err != nil {
		return t, fmt.Errorf("store: get transaction %d: %w", txID, err)
	}
	return t, nil
}

func (s *Store) UncategorizedCount(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM transactions WHERE user_id = ? AND category_id IS NULL`, userID).Scan(&n)
	return n, err
}
