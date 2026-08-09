-- An SBOM is an object now; the row keeps only the key that finds it.
ALTER TABLE sboms DROP COLUMN compressed_data;
ALTER TABLE sboms ADD COLUMN object_key text NOT NULL;
