package store

import (
	"context"
	"fmt"
)

// Rule mirrors the rules table (AGENTS.md §5: grown from user answers; a
// rule-management UI is deferred to v2).
type Rule struct {
	ID         int64
	UserID     int64
	CategoryID int64
	Pattern    string
	MatchType  string
}

func (s *Store) ListRules(ctx context.Context, userID int64) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, category_id, pattern, match_type FROM rules WHERE user_id = ?`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("store: list rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.UserID, &r.CategoryID, &r.Pattern, &r.MatchType); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRule records a pattern -> category mapping. Idempotent: the UNIQUE
// (user_id, pattern, category_id) constraint makes repeats no-ops.
func (s *Store) InsertRule(ctx context.Context, userID, categoryID int64, pattern, matchType string) error {
	if matchType == "" {
		matchType = "exact"
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO rules (user_id, category_id, pattern, match_type) VALUES (?, ?, ?, ?)`,
		userID, categoryID, pattern, matchType); err != nil {
		return fmt.Errorf("store: insert rule %q: %w", pattern, err)
	}
	return nil
}
