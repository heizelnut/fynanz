-- Initial schema. Every table is user-scoped from day one (AGENTS.md §6):
-- multi-tenancy is additive later, no retrofit needed.

CREATE TABLE users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    email        TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    is_admin     INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Enable Banking consent sessions, one row per linked bank per user.
-- Three-legged flow: pending_consent -> active -> (expired|revoked|error|closed)
CREATE TABLE bank_links (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          INTEGER NOT NULL REFERENCES users(id),
    bank             TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending_consent',
    aspsp_name       TEXT NOT NULL,
    aspsp_country    TEXT NOT NULL DEFAULT '',
    authorization_id TEXT NOT NULL DEFAULT '',
    session_id       TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT '',
    valid_until      TEXT NOT NULL DEFAULT '',
    last_error       TEXT NOT NULL DEFAULT '',
    last_synced_at   TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(user_id, bank)
);

CREATE TABLE accounts (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL REFERENCES users(id),
    bank_link_id        INTEGER NOT NULL REFERENCES bank_links(id),
    uid                 TEXT NOT NULL UNIQUE,
    identification_hash TEXT NOT NULL DEFAULT '',
    iban                TEXT NOT NULL DEFAULT '',
    name                TEXT NOT NULL DEFAULT '',
    details             TEXT NOT NULL DEFAULT '',
    product             TEXT NOT NULL DEFAULT '',
    cash_account_type   TEXT NOT NULL DEFAULT '',
    usage               TEXT NOT NULL DEFAULT '',
    currency            TEXT NOT NULL DEFAULT 'EUR',
    balance_minor       INTEGER,
    balance_currency    TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(user_id, identification_hash)
);
CREATE INDEX idx_accounts_user ON accounts(user_id);

-- Amounts stored as signed minor units (expenses/DBIT negative, income/CRDT
-- positive) to avoid float money. Ties to currency scale (EUR -> 2).
CREATE TABLE transactions (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id                INTEGER NOT NULL REFERENCES users(id),
    account_id             INTEGER NOT NULL REFERENCES accounts(id),
    entry_reference        TEXT NOT NULL,
    booking_date           TEXT NOT NULL DEFAULT '',
    value_date             TEXT NOT NULL DEFAULT '',
    amount_minor           INTEGER NOT NULL,
    currency               TEXT NOT NULL,
    credit_debit           TEXT NOT NULL,
    status                 TEXT NOT NULL DEFAULT '',
    merchant_category_code TEXT NOT NULL DEFAULT '',
    counterparty           TEXT NOT NULL DEFAULT '',
    counterparty_account   TEXT NOT NULL DEFAULT '',
    remittance_information TEXT NOT NULL DEFAULT '',
    note                   TEXT NOT NULL DEFAULT '',
    balance_after_minor    INTEGER,
    raw                    TEXT NOT NULL DEFAULT '',
    created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(user_id, account_id, entry_reference)
);
CREATE INDEX idx_tx_user_date ON transactions(user_id, booking_date DESC);
CREATE INDEX idx_tx_account_date ON transactions(account_id, booking_date DESC);

-- v1 categories are broad and fixed but stored in the DB so they stay
-- user-editable (rename/merge/add) without a code change (AGENTS.md §5).
CREATE TABLE categories (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id   INTEGER NOT NULL REFERENCES users(id),
    name      TEXT NOT NULL,
    is_system INTEGER NOT NULL DEFAULT 0,
    UNIQUE(user_id, name)
);

-- Merchant pattern -> category. Seeded manually and grown from user answers
-- when resolving the Uncategorized inbox (AGENTS.md §5).
CREATE TABLE rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    category_id INTEGER NOT NULL REFERENCES categories(id),
    pattern     TEXT NOT NULL,
    match_type  TEXT NOT NULL DEFAULT 'exact',
    UNIQUE(user_id, pattern, category_id)
);

-- Notification channels (AGENTS.md §7), user-scoped from day one.
CREATE TABLE notification_channels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    type       TEXT NOT NULL,
    config     TEXT NOT NULL DEFAULT '{}',
    events     TEXT NOT NULL DEFAULT '{}',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Single implicit user in v1 (AGENTS.md §6).
INSERT INTO users (email, display_name, is_admin) VALUES ('local@fynanz', 'Local User', 1);

-- v1 broad fixed categories (AGENTS.md §5).
INSERT INTO categories (user_id, name, is_system) VALUES
    (1, 'Income', 1),
    (1, 'Groceries', 1),
    (1, 'Dining & Takeout', 1),
    (1, 'Transport', 1),
    (1, 'Utilities & Bills', 1),
    (1, 'Housing', 1),
    (1, 'Health', 1),
    (1, 'Shopping', 1),
    (1, 'Entertainment', 1),
    (1, 'Subscriptions', 1),
    (1, 'Transfers (internal)', 1),
    (1, 'Savings & Investments', 1),
    (1, 'Fees & Charges', 1),
    (1, 'Uncategorized', 1);
