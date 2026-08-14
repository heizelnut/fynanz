package store

import (
	"context"
	"fmt"
)

// NotificationChannel mirrors the notification_channels table (AGENTS.md §7):
// one row per backend config, user-scoped, with a per-event routing map.
type NotificationChannel struct {
	ID        int64
	UserID    int64
	Type      string // ntfy | webhook
	Config    string // JSON, backend-specific (url/topic/token…)
	Events    string // JSON map[event]bool; "{}" = all events
	Enabled   bool
	CreatedAt string
}

const notifCols = `id, user_id, type, config, events, enabled, created_at`

func scanNotifChannel(row interface{ Scan(...any) error }) (NotificationChannel, error) {
	var c NotificationChannel
	err := row.Scan(&c.ID, &c.UserID, &c.Type, &c.Config, &c.Events, &c.Enabled, &c.CreatedAt)
	return c, err
}

// ListNotificationChannels returns the user's notification channels.
func (s *Store) ListNotificationChannels(ctx context.Context, userID int64) ([]NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+notifCols+` FROM notification_channels WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list notification channels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []NotificationChannel
	for rows.Next() {
		c, err := scanNotifChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// InsertNotificationChannel adds a channel; returns the new id.
func (s *Store) InsertNotificationChannel(ctx context.Context, userID int64, typ, config, events string, enabled bool) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO notification_channels (user_id, type, config, events, enabled) VALUES (?, ?, ?, ?, ?)`,
		userID, typ, config, events, enabled)
	if err != nil {
		return 0, fmt.Errorf("store: insert notification channel: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) DeleteNotificationChannel(ctx context.Context, userID, id int64) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM notification_channels WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return fmt.Errorf("store: delete notification channel %d: %w", id, err)
	}
	return nil
}
