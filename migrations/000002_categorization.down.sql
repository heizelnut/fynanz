DROP INDEX IF EXISTS idx_tx_merchant;
DROP INDEX IF EXISTS idx_tx_user_cat;
ALTER TABLE transactions DROP COLUMN internal_transfer;
ALTER TABLE transactions DROP COLUMN merchant_normalized;
ALTER TABLE transactions DROP COLUMN category_source;
ALTER TABLE transactions DROP COLUMN category_id;
