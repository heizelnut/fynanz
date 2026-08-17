-- Rules get a match field (merchant | description) and fuzzy/contains
-- semantics, and one category per (user, field, pattern) — no more silent
-- first-match-wins when the same merchant got resolved to different
-- categories at different times (AGENTS.md §5).

ALTER TABLE rules RENAME TO rules_old;

CREATE TABLE rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    category_id INTEGER NOT NULL REFERENCES categories(id),
    pattern     TEXT NOT NULL,
    match_type  TEXT NOT NULL DEFAULT 'exact',
    field       TEXT NOT NULL DEFAULT 'merchant',
    UNIQUE(user_id, field, pattern)
);

-- Existing rules were all merchant-field; OR IGNORE drops legacy duplicates
-- (same pattern, conflicting categories) keeping the earliest learned one.
INSERT OR IGNORE INTO rules (id, user_id, category_id, pattern, match_type, field)
    SELECT id, user_id, category_id, pattern, match_type, 'merchant' FROM rules_old;

DROP TABLE rules_old;

-- Person-to-person transfers are never auto-categorized (§5): flagged at
-- sync time, forced to the Uncategorized inbox by the classify pipeline.
ALTER TABLE transactions ADD COLUMN is_p2p INTEGER NOT NULL DEFAULT 0;
