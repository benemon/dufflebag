CREATE TABLE principals (
    id text PRIMARY KEY,
    name text NOT NULL,
    client_id text NOT NULL UNIQUE,
    organization_id uuid NOT NULL,
    project_id uuid NULL,
    created_at timestamptz NOT NULL,
    -- NULL is how an organization-scoped principal is stored. A zero UUID is a
    -- different thing wearing the same clothes: it reads as a real project to
    -- anything that does not special-case it (ADR-0016), so make it unstorable
    -- rather than merely detected on the way out.
    CONSTRAINT principals_project_id_not_zero
        CHECK (project_id IS NULL OR project_id <> '00000000-0000-0000-0000-000000000000'::uuid)
);

CREATE TABLE principal_secrets (
    id text PRIMARY KEY,
    principal_id text NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    encoded_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    last_used_at timestamptz NULL
);

-- Deliberately no row-level security: authentication looks up client_id before
-- the caller's tenant is known. Tenant session variables here would either make
-- authentication impossible or trust an unauthenticated tenancy claim.
-- The unauthenticated read exposes only an argon2id hash, never the plaintext.
