-- History of notification send attempts (one row per channel × payload).
-- Lets the Notifications tab show what was pushed, when, and whether it
-- worked — including failures, which the counters alone hide.

CREATE TABLE notification_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    channel_id   INTEGER,
    channel_type TEXT NOT NULL,
    event        TEXT NOT NULL,
    ok           INTEGER NOT NULL,
    error        TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    message      TEXT NOT NULL DEFAULT '',
    link         TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX idx_notif_events_user ON notification_events(user_id, created_at DESC);
