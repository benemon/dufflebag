CREATE TABLE plugin_bundles (
    organization_id uuid PRIMARY KEY REFERENCES plugin_registries(organization_id) ON DELETE CASCADE,
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    requested_by text NOT NULL,
    requested_host text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz NULL,
    started_at timestamptz NULL,
    finished_at timestamptz NULL,
    exported_at timestamptz NULL,
    error text NOT NULL DEFAULT '',
    object_key text NULL,
    filename text NULL,
    size bigint NULL,
    sha256 text NULL,
    plugin_count integer NULL,
    version_count integer NULL
);

ALTER TABLE plugin_bundles ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_bundles FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plugin_bundles
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);
