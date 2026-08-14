package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Setting keys managed from the Settings page (Enable Banking credentials).
const (
	SettingEBAppID          = "eb_app_id"
	SettingEBPrivateKey     = "eb_private_key"
	SettingEBBaseURL        = "eb_base_url"
	SettingEBPSUType        = "eb_psu_type"
	SettingEBMaxConsentDays = "eb_max_consent_days"
)

// GetSetting returns the user's stored value for key, or "" when unset.
func (s *Store) GetSetting(ctx context.Context, userID int64, key string) (string, error) {
	var v string
	err := s.queryRow(ctx,
		`SELECT value FROM settings WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: get setting %s: %w", key, err)
	}
	return v, nil
}

// SetSetting upserts a value; empty value removes the row (back to env).
func (s *Store) SetSetting(ctx context.Context, userID int64, key, value string) error {
	if value == "" {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM settings WHERE user_id = ? AND key = ?`, userID, key); err != nil {
			return fmt.Errorf("store: clear setting %s: %w", key, err)
		}
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (user_id, key, value, updated_at)
		 VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		userID, key, value); err != nil {
		return fmt.Errorf("store: set setting %s: %w", key, err)
	}
	return nil
}
