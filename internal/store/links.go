package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const linkCols = `id, user_id, bank, status, aspsp_name, aspsp_country, authorization_id,
	session_id, state, valid_until, last_error, last_synced_at, created_at, updated_at`

func scanLink(row interface{ Scan(...any) error }) (BankLink, error) {
	var l BankLink
	err := row.Scan(&l.ID, &l.UserID, &l.Bank, &l.Status, &l.AspspName, &l.AspspCountry,
		&l.AuthorizationID, &l.SessionID, &l.State, &l.ValidUntil, &l.LastError,
		&l.LastSyncedAt, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

// CreateLink registers a pending consent flow for one bank of a user.
func (s *Store) CreateLink(ctx context.Context, userID int64, bank, aspspName, aspspCountry string) (*BankLink, error) {
	l := &BankLink{
		UserID:       userID,
		Bank:         bank,
		Status:       LinkPendingConsent,
		AspspName:    aspspName,
		AspspCountry: aspspCountry,
		State:        uuid.NewString(),
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO bank_links (user_id, bank, status, aspsp_name, aspsp_country, state)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		l.UserID, l.Bank, l.Status, l.AspspName, l.AspspCountry, l.State)
	if err != nil {
		return nil, fmt.Errorf("store: create link %s: %w", bank, err)
	}
	l.ID, err = res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("store: link id: %w", err)
	}
	return l, nil
}

func (s *Store) GetLink(ctx context.Context, id, userID int64) (*BankLink, error) {
	l, err := scanLink(s.queryRow(ctx,
		`SELECT `+linkCols+` FROM bank_links WHERE id = ? AND user_id = ?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get link %d: %w", id, err)
	}
	return &l, nil
}

// GetLinkByState finds the pending link whose CSRF state matches the callback.
func (s *Store) GetLinkByState(ctx context.Context, state string) (*BankLink, error) {
	l, err := scanLink(s.queryRow(ctx,
		`SELECT `+linkCols+` FROM bank_links WHERE state = ?`, state))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get link by state: %w", err)
	}
	return &l, nil
}

// GetLinkByBank finds the user's link row for a bank, if any.
func (s *Store) GetLinkByBank(ctx context.Context, userID int64, bank string) (*BankLink, error) {
	l, err := scanLink(s.queryRow(ctx,
		`SELECT `+linkCols+` FROM bank_links WHERE user_id = ? AND bank = ?`, userID, bank))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get link by bank: %w", err)
	}
	return &l, nil
}

func (s *Store) ListLinks(ctx context.Context, userID int64) ([]BankLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkCols+` FROM bank_links WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BankLink
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) ActiveLinks(ctx context.Context, userID int64) ([]BankLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkCols+` FROM bank_links WHERE user_id = ? AND status = ? ORDER BY id`,
		userID, LinkActive)
	if err != nil {
		return nil, fmt.Errorf("store: active links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BankLink
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) SetLinkAuthorizing(ctx context.Context, id int64, authorizationID string) error {
	return s.exec(ctx,
		`UPDATE bank_links SET authorization_id = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		authorizationID, id)
}

func (s *Store) ActivateLink(ctx context.Context, id int64, sessionID, validUntil string) error {
	return s.exec(ctx,
		`UPDATE bank_links SET status = ?, session_id = ?, valid_until = ?, last_error = '',
		 updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		LinkActive, sessionID, validUntil, id)
}

func (s *Store) SetLinkStatus(ctx context.Context, id int64, status, lastError string) error {
	return s.exec(ctx,
		`UPDATE bank_links SET status = ?, last_error = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		status, lastError, id)
}

// ResetLinkPending returns an existing link row to pending_consent with a
// fresh CSRF state, clearing any prior session. Used for re-authorization
// (UNIQUE(user_id, bank) means we reuse the row, not replace it).
func (s *Store) ResetLinkPending(ctx context.Context, id int64, state string) error {
	return s.exec(ctx,
		`UPDATE bank_links SET status = ?, state = ?, authorization_id = '', session_id = '',
			valid_until = '', last_error = '',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`,
		LinkPendingConsent, state, id)
}

// TouchLinkSync records that a sync run completed for the link.
func (s *Store) TouchLinkSync(ctx context.Context, id int64) error {
	return s.exec(ctx,
		`UPDATE bank_links SET last_synced_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		 updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, id)
}
