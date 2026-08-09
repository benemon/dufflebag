-- Live history distinguishes the actor per assignment row: a manual
-- UpdateChannel records the caller's principal id (probe 41), while completion
-- auto-assignment records the service author (probe 45). Existing rows predate
-- that evidence and did not persist the actor, so their honest value is the
-- explicit unknown empty string rather than a derivation from today's channel.
--
-- Expand-only: the default keeps the previous release's named-column inserts
-- valid while old and new application versions overlap.
ALTER TABLE channel_assignments ADD COLUMN author_id text;

-- FORCE RLS binds the table owner too. Lift it only for this cross-tenant
-- backfill, leave the tenant policy intact, and restore it before commit. This
-- follows migration 000008's backfill posture.
ALTER TABLE channel_assignments NO FORCE ROW LEVEL SECURITY;
-- The table's append-only trigger correctly rejects UPDATE. Disable only that
-- named trigger around the one migration backfill, then restore it before the
-- schema becomes visible; runtime history remains immutable.
ALTER TABLE channel_assignments DISABLE TRIGGER channel_assignments_append_only;
UPDATE channel_assignments SET author_id = '' WHERE author_id IS NULL;
ALTER TABLE channel_assignments ENABLE TRIGGER channel_assignments_append_only;
ALTER TABLE channel_assignments FORCE ROW LEVEL SECURITY;

ALTER TABLE channel_assignments ALTER COLUMN author_id SET DEFAULT '';
ALTER TABLE channel_assignments ALTER COLUMN author_id SET NOT NULL;
