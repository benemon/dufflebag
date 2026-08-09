-- The bytes live in object storage and cannot be recovered into a column, so a
-- rollback discards the rows rather than inventing bytes or building a
-- conversion nobody wants.
DELETE FROM sboms;
ALTER TABLE sboms DROP COLUMN object_key;
ALTER TABLE sboms ADD COLUMN compressed_data bytea NOT NULL;
