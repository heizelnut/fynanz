package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Rule mirrors the rules table: a (field, pattern) -> category mapping grown
// from inbox answers and editable at /rules (AGENTS.md §5, rule UI landed).
type Rule struct {
	ID         int64
	UserID     int64
	CategoryID int64
	Pattern    string
	MatchType  string // exact | contains | fuzzy
	Field      string // merchant | description
}

func (s *Store) ListRules(ctx context.Context, userID int64) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, category_id, pattern, match_type, field
		 FROM rules WHERE user_id = ? ORDER BY id`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("store: list rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.UserID, &r.CategoryID, &r.Pattern, &r.MatchType, &r.Field); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRule records a (field, pattern) -> category mapping. Idempotent:
// UNIQUE(user_id, field, pattern) makes repeats no-ops — the first learned
// category wins unless the rule is edited/deleted at /rules.
func (s *Store) InsertRule(ctx context.Context, userID, categoryID int64, pattern, matchType, field string) error {
	if matchType == "" {
		matchType = "exact"
	}
	if field == "" {
		field = "merchant"
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO rules (user_id, category_id, pattern, match_type, field) VALUES (?, ?, ?, ?, ?)`,
		userID, categoryID, pattern, matchType, field); err != nil {
		return fmt.Errorf("store: insert rule %q: %w", pattern, err)
	}
	return nil
}

// UpdateRule edits one of the user's rules. The (field, pattern) uniqueness
// is enforced by the schema; conflicts surface as a friendly error.
func (s *Store) UpdateRule(ctx context.Context, userID, ruleID, categoryID int64, pattern, matchType, field string) error {
	if matchType == "" {
		matchType = "exact"
	}
	if field == "" {
		field = "merchant"
	}
	n, err := s.db.ExecContext(ctx,
		`UPDATE rules SET category_id = ?, pattern = ?, match_type = ?, field = ?
		 WHERE id = ? AND user_id = ?`,
		categoryID, pattern, matchType, field, ruleID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errors.New("another rule already uses this pattern for this match field — edit that one instead")
		}
		return fmt.Errorf("store: update rule %d: %w", ruleID, err)
	}
	if affected, _ := n.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteRule removes one of the user's rules.
func (s *Store) DeleteRule(ctx context.Context, userID, ruleID int64) error {
	n, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id = ? AND user_id = ?`, ruleID, userID)
	if err != nil {
		return fmt.Errorf("store: delete rule %d: %w", ruleID, err)
	}
	if affected, _ := n.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
