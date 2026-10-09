ALTER TABLE plugin_registries
    ADD COLUMN default_platforms text[] NOT NULL DEFAULT '{linux_amd64,linux_arm64,darwin_arm64}';

CREATE TABLE plugin_imports (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES plugin_registries(organization_id) ON DELETE CASCADE,
    source_kind text NOT NULL CHECK (source_kind IN ('releases-hashicorp', 'github')),
    product text NOT NULL,
    versions text[] NOT NULL,
    platforms text[] NOT NULL,
    state text NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'running', 'succeeded', 'partially_succeeded', 'failed')),
    outcomes jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz NULL,
    finished_at timestamptz NULL
);

CREATE INDEX plugin_imports_queued ON plugin_imports (created_at) WHERE state IN ('queued', 'running');

ALTER TABLE plugin_imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_imports FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plugin_imports
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);
