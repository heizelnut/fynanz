package store

import (
	"context"
	"fmt"
)

const (
	DefaultUserEmail = "local@fynanz"
	DefaultUserName  = "Local User"
)

// EnsureDefaultUser returns the id of the single implicit v1 user, creating it
// if missing (AGENTS.md §6).
func (s *Store) EnsureDefaultUser(ctx context.Context) (int64, error) {
	var id int64
	err := s.queryRow(ctx,
		`INSERT INTO users (email, display_name, is_admin)
		 VALUES (?, ?, 1)
		 ON CONFLICT(email) DO NOTHING`, DefaultUserEmail, DefaultUserName).Scan(&id)
	if err == nil {
		return id, nil
	}
	// RowsAffected-style: query again when insert raced or no row returned.
	err = s.queryRow(ctx,
		`SELECT id FROM users WHERE email = ?`, DefaultUserEmail).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: ensure default user: %w", err)
	}
	return id, nil
}
