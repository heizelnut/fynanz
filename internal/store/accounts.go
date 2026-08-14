package store

import (
	"context"
	"fmt"
)

// UpsertAccounts inserts or updates accounts for a link, keyed by the stable
// identification_hash (uid is session-scoped and may rotate). Returns the
// database account id per uid.
func (s *Store) UpsertAccounts(ctx context.Context, userID, linkID int64, accs []AccountInput) (map[string]int64, error) {
	ids := make(map[string]int64, len(accs))
	if len(accs) == 0 {
		return ids, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: accounts tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, a := range accs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO accounts (user_id, bank_link_id, uid, identification_hash, iban, name,
				details, product, cash_account_type, usage, currency, balance_minor, balance_currency)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id, identification_hash) DO UPDATE SET
				uid = excluded.uid,
				iban = excluded.iban,
				name = excluded.name,
				details = excluded.details,
				product = excluded.product,
				cash_account_type = excluded.cash_account_type,
				usage = excluded.usage,
				currency = excluded.currency,
				balance_minor = excluded.balance_minor,
				balance_currency = excluded.balance_currency,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			userID, linkID, a.UID, a.IdentificationHash, a.Iban, a.Name, a.Details,
			a.Product, a.CashAccountType, a.Usage, a.Currency, a.BalanceMinor, a.BalanceCurrency); err != nil {
			return nil, fmt.Errorf("store: upsert account %s: %w", a.UID, err)
		}
	}
	for _, a := range accs {
		var id int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM accounts WHERE user_id = ? AND identification_hash = ?`,
			userID, a.IdentificationHash).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("store: resolve account %s: %w", a.UID, err)
		}
		ids[a.UID] = id
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: accounts commit: %w", err)
	}
	return ids, nil
}

func (s *Store) ListAccounts(ctx context.Context, userID int64) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, bank_link_id, uid, identification_hash, iban, name, details,
			product, cash_account_type, usage, currency, balance_minor, balance_currency,
			created_at, updated_at
		 FROM accounts WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Account
	for rows.Next() {
		var a Account
		err := rows.Scan(&a.ID, &a.UserID, &a.LinkID, &a.UID, &a.IdentificationHash,
			&a.Iban, &a.Name, &a.Details, &a.Product, &a.CashAccountType, &a.Usage,
			&a.Currency, &a.BalanceMinor, &a.BalanceCurrency, &a.CreatedAt, &a.UpdatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DisplayName returns iban/name or a uid fallback for display purposes.
func (a Account) DisplayName() string {
	switch {
	case a.Name != "":
		return a.Name
	case a.Iban != "":
		return a.Iban
	case len(a.UID) > 8:
		return a.UID[:8]
	default:
		return a.UID
	}
}
