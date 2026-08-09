DROP INDEX IF EXISTS builds_parent_version_id_idx;
ALTER TABLE builds DROP COLUMN metadata;
ALTER TABLE versions DROP COLUMN author_id;
