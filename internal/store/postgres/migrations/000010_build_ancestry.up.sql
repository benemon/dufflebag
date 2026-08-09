-- Packer sends these identifiers on the terminal UpdateBuild. Nullable,
-- expand-only columns keep previous releases' named-column inserts valid while
-- old and new application versions overlap.
ALTER TABLE builds ADD COLUMN parent_version_id text;
ALTER TABLE builds ADD COLUMN parent_channel_id text;
