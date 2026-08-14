-- Categorization support (AGENTS.md §5): category per transaction, the
-- normalized merchant used for rule matching, and the internal-transfer flag
-- (own-account movements excluded from aggregate views).

ALTER TABLE transactions ADD COLUMN category_id INTEGER REFERENCES categories(id);
ALTER TABLE transactions ADD COLUMN category_source TEXT NOT NULL DEFAULT '';
ALTER TABLE transactions ADD COLUMN merchant_normalized TEXT NOT NULL DEFAULT '';
ALTER TABLE transactions ADD COLUMN internal_transfer INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_tx_user_cat ON transactions(user_id, category_id);
CREATE INDEX idx_tx_merchant ON transactions(user_id, merchant_normalized);

-- Cheap backfill for rows already in the DB; the app re-derives this properly
-- from counterparty/remittance on its next classification pass.
UPDATE transactions SET merchant_normalized = lower(trim(counterparty));
