ALTER TABLE plugin_imports
    ADD COLUMN created_by text NOT NULL DEFAULT '',
    ADD COLUMN origin text NOT NULL DEFAULT 'import' CHECK (origin IN ('import', 'plugin', 'catalogue', 'upload')),
    ADD COLUMN batch_index integer NOT NULL DEFAULT 1,
    ADD COLUMN batch_size integer NOT NULL DEFAULT 1;
