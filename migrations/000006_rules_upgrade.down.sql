ALTER TABLE transactions DROP COLUMN is_p2p;

ALTER TABLE rules RENAME TO rules_new;

CREATE TABLE rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    category_id INTEGER NOT NULL REFERENCES categories(id),
    pattern     TEXT NOT NULL,
    match_type  TEXT NOT NULL DEFAULT 'exact',
    UNIQUE(user_id, pattern, category_id)
);

INSERT OR IGNORE INTO rules (id, user_id, category_id, pattern, match_type)
    SELECT id, user_id, category_id, pattern, match_type FROM rules_new;

DROP TABLE rules_new;
