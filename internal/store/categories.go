package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CreateCategory adds a user category. Returns the new id.
func (s *Store) CreateCategory(ctx context.Context, userID int64, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("store: empty category name")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO categories (user_id, name) VALUES (?, ?)`, userID, name)
	if err != nil {
		return 0, fmt.Errorf("store: create category %q: %w", name, err)
	}
	return res.LastInsertId()
}

// RenameCategory renames a category; fails on name collisions.
func (s *Store) RenameCategory(ctx context.Context, userID, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("store: empty category name")
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE categories SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID); err != nil {
		return fmt.Errorf("store: rename category %d: %w", id, err)
	}
	return nil
}

// MergeCategory points all transactions and rules of src to dst, then deletes
// src (AGENTS.md §4: merge, no code change).
func (s *Store) MergeCategory(ctx context.Context, userID, srcID, dstID int64) error {
	if srcID == dstID {
		return errors.New("store: cannot merge category into itself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE transactions SET category_id = ?, category_source = ?
		 WHERE user_id = ? AND category_id = ?`, dstID, CatSourceManual, userID, srcID); err != nil {
		return fmt.Errorf("store: merge txs: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM rules WHERE user_id = ? AND category_id = ?`, userID, srcID); err != nil {
		return fmt.Errorf("store: merge rules: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM categories WHERE id = ? AND user_id = ?`, srcID, userID); err != nil {
		return fmt.Errorf("store: delete merged category: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ListCategories(ctx context.Context, userID int64) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, is_system FROM categories WHERE user_id = ? ORDER BY id`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("store: list categories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.IsSystem); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
