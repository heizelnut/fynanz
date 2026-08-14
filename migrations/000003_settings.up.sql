-- User-scoped key/value settings (AGENTS.md §6: user-scoped from day one).
-- Enables configuring the Enable Banking credentials post-deploy from the
-- Settings page instead of editing env vars on the server. Values override
-- env-based defaults (which remain the fallback for local dev).

CREATE TABLE settings (
    user_id    INTEGER NOT NULL REFERENCES users(id),
    key        TEXT    NOT NULL,
    value      TEXT    NOT NULL DEFAULT '',
    updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (user_id, key)
);
