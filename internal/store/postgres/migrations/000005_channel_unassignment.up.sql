-- Clearing a channel assignment must be expressible. The provider's destroy
-- path updates a channel with an empty fingerprint under the versionFingerprint
-- mask, meaning "unassign" (duf-8em). The history table is append-only, so an
-- unassignment is recorded as a row with no version rather than by deleting the
-- rows that led to it. The latest row having no version is what "unassigned"
-- looks like.
ALTER TABLE channel_assignments ALTER COLUMN version_id DROP NOT NULL;
