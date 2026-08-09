-- Every bucket carries a managed channel named "latest" (duf-08q). Live HCP
-- auto-creates it at CreateBucket with managed:true, restricted:true and
-- assigns each completed version to it (dossier §7, Appendix A probes 04-06,
-- 13-19). The application creates it inside the CreateBucket transaction from
-- this release on; this migration establishes the invariant for buckets that
-- already exist.
--
-- Expand-only: the new column defaults to false, so the previous release's
-- writes (which name no managed column) remain valid against this schema —
-- cmd/schema-compat exercises exactly that shape.
ALTER TABLE channels ADD COLUMN managed boolean NOT NULL DEFAULT false;

-- Channel ids are wire-visible ULIDs and internal/domain/registry/id.go parses
-- them with ulid.ParseStrict, so backfilled rows must carry real ULIDs: a
-- 48-bit millisecond timestamp then 80 random bits, Crockford base32, 26
-- characters. Randomness comes from gen_random_uuid() (built in since
-- Postgres 13) so the migration adds no extension dependency.
CREATE FUNCTION dufflebag_migration_ulid(at timestamptz) RETURNS text
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    alphabet constant text := '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
    value numeric := floor(extract(epoch FROM at) * 1000);
    random_bytes bytea := decode(replace(gen_random_uuid()::text, '-', ''), 'hex');
    result text := '';
    i integer;
BEGIN
    FOR i IN 0..9 LOOP
        value := value * 256 + get_byte(random_bytes, i);
    END LOOP;
    FOR i IN 1..26 LOOP
        result := substr(alphabet, mod(value, 32)::integer + 1, 1) || result;
        value := trunc(value / 32);
    END LOOP;
    RETURN result;
END
$$;

-- The backfill must see and write EVERY tenant's rows, but tenant isolation is
-- FORCE'd row-level security (migration 000001), which binds the table owner
-- too. Lifting FORCE for the duration of this migration lets the owner — the
-- role that runs migrations and owns these tables — cross tenants exactly
-- once, here; the policies themselves are never touched and FORCE is restored
-- below. Without this the backfill would silently match zero rows and the
-- invariant would hold only for new buckets.
ALTER TABLE buckets NO FORCE ROW LEVEL SECURITY;
ALTER TABLE channels NO FORCE ROW LEVEL SECURITY;

-- A pre-existing user-created "latest" becomes the managed channel rather than
-- colliding with it: live HCP's latest always exists managed, so a bucket
-- where clients already promote to "latest" converges on the probed shape
-- (managed:true, restricted:true — probe 04) and keeps its assignment history.
UPDATE channels SET managed = true, restricted = true WHERE name = 'latest';

INSERT INTO channels (
    organization_id, project_id, id, bucket_id, name, restricted, managed,
    created_at, updated_at
)
SELECT buckets.organization_id, buckets.project_id,
       dufflebag_migration_ulid(now()), buckets.id, 'latest', true, true,
       now(), now()
FROM buckets
WHERE NOT EXISTS (
    SELECT 1 FROM channels
    WHERE channels.organization_id = buckets.organization_id
      AND channels.project_id = buckets.project_id
      AND channels.bucket_id = buckets.id
      AND channels.name = 'latest'
);

ALTER TABLE channels FORCE ROW LEVEL SECURITY;
ALTER TABLE buckets FORCE ROW LEVEL SECURITY;

DROP FUNCTION dufflebag_migration_ulid(timestamptz);

-- Backfilled channels are deliberately left unassigned. Auto-assignment is a
-- completion-time behaviour (Appendix A probe 13-14); no completion is
-- happening during this migration, and inventing which historical version
-- "latest" should have carried would be a guess the probe does not back.
