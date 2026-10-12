ALTER TABLE plugin_imports
    ADD COLUMN plugin text NOT NULL DEFAULT '',
    ADD COLUMN started_at timestamptz NULL,
    ADD COLUMN retry_of uuid NULL;

UPDATE plugin_imports SET plugin = regexp_replace(product, '^.*packer-plugin-', '');
UPDATE plugin_imports SET started_at = claimed_at WHERE state <> 'queued' AND claimed_at IS NOT NULL;

CREATE INDEX plugin_imports_list ON plugin_imports (organization_id, created_at DESC);
