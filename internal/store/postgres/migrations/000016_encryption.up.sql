-- Encryption at rest (ADR-0024).

-- The mode marker: whether this instance has encryption at rest is decided at
-- first boot and never changes. Startup compares the configured mode against
-- this row and refuses on mismatch — the one-way door, closed honestly,
-- instead of dual-read paths and backfill jobs.
CREATE TABLE encryption_mode (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    encrypted boolean NOT NULL,
    recorded_at timestamptz NOT NULL
);

-- The keyring: locally generated keys stored wrapped by the external key
-- service's KEK. kek_ref records which KEK version wrapped each row, so KEK
-- rotation rewraps exactly the rows that need it and payloads are never
-- touched. Unencrypted deployments leave this table empty.
CREATE TABLE keyring (
    purpose text NOT NULL,
    version integer NOT NULL CHECK (version >= 1),
    wrapped_key bytea NOT NULL,
    kek_ref text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (purpose, version)
);

-- builds.metadata becomes opaque bytes: raw JSON on unencrypted deployments,
-- a versioned AES-GCM envelope on encrypted ones. The payload was already
-- opaque to SQL — nothing filters or joins on it — so jsonb bought nothing
-- the envelope does not.
ALTER TABLE builds ALTER COLUMN metadata DROP DEFAULT;
ALTER TABLE builds ALTER COLUMN metadata TYPE bytea USING convert_to(metadata::text, 'UTF8');
ALTER TABLE builds ALTER COLUMN metadata SET DEFAULT '\x7b7d';  -- '{}'

-- Row integrity MACs, written and verified only on encrypted deployments;
-- NULL everywhere else. Provenance rows resist alteration (the audit trail is
-- the deletion detector — no hash chain, ADR-0024), and identity rows make a
-- psql-inserted principal fail authentication: database write access is not
-- administration.
ALTER TABLE versions ADD COLUMN integrity_mac bytea;
ALTER TABLE builds ADD COLUMN integrity_mac bytea;
ALTER TABLE artifacts ADD COLUMN integrity_mac bytea;
ALTER TABLE channel_assignments ADD COLUMN integrity_mac bytea;
ALTER TABLE principals ADD COLUMN integrity_mac bytea;
ALTER TABLE principal_secrets ADD COLUMN integrity_mac bytea;
