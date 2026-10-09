CREATE TABLE plugin_registries (
    organization_id uuid PRIMARY KEY REFERENCES public.organizations(id) ON DELETE RESTRICT,
    exposed boolean NOT NULL DEFAULT false,
    enabled_at timestamptz NOT NULL DEFAULT now(),
    exposed_at timestamptz NULL
);

ALTER TABLE plugin_registries ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_registries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plugin_registries
    USING (
        organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    )
    WITH CHECK (
        organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    );
