CREATE TABLE plugin_bundle_imports (
    organization_id uuid PRIMARY KEY REFERENCES plugin_registries(organization_id) ON DELETE CASCADE,
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'partially_succeeded', 'failed')),
    requested_by text NOT NULL,
    object_key text NOT NULL,
    filename text NOT NULL,
    size bigint NOT NULL,
    sha256 text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz NULL,
    started_at timestamptz NULL,
    finished_at timestamptz NULL,
    error text NOT NULL DEFAULT '',
    bundle_host text NOT NULL DEFAULT '',
    bundle_organization text NOT NULL DEFAULT '',
    bundle_exported_at timestamptz NULL,
    bundle_version text NOT NULL DEFAULT '',
    plugin_count integer NOT NULL DEFAULT 0,
    version_count integer NOT NULL DEFAULT 0,
    outcomes jsonb NOT NULL DEFAULT '[]'
);

ALTER TABLE plugin_bundle_imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_bundle_imports FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plugin_bundle_imports
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);
