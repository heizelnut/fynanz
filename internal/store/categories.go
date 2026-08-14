package store

import (
	"context"
	"fmt"
)

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
