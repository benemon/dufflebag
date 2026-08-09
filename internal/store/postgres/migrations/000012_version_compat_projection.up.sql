-- Both columns are expand-only so the previous release's named-column writes
-- remain valid while old and new application versions overlap.
ALTER TABLE versions ADD COLUMN author_id text NOT NULL DEFAULT '';
ALTER TABLE builds ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}';

-- The version projection asks, per version, whether any build names it as a
-- parent (has_descendants) and resolves recorded parents. Both look up builds
-- by parent_version_id, which nothing indexed while the column was write-only.
-- Without this each such lookup scans builds, and a list read performs one per
-- version returned.
CREATE INDEX builds_parent_version_id_idx ON builds (parent_version_id)
    WHERE parent_version_id IS NOT NULL;
