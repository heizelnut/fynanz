-- Some connectors (notably Revolut) may leave booking_date empty while
-- providing value_date; time-based aggregations then silently lose those
-- transactions. Backfill the fallback so charts/lists group every row.
UPDATE transactions SET booking_date = value_date
WHERE (booking_date = '' OR booking_date IS NULL) AND value_date != '' AND value_date IS NOT NULL;
