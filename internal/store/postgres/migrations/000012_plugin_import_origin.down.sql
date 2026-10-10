ALTER TABLE plugin_imports
    DROP COLUMN created_by,
    DROP COLUMN origin,
    DROP COLUMN batch_index,
    DROP COLUMN batch_size;
