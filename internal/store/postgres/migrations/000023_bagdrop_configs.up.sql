CREATE TABLE bagdrop_configs (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    adapter text NOT NULL,
    destination jsonb NOT NULL,
    sealed_secret bytea NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    last_verification jsonb NULL,
    last_verified_at timestamptz NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id) ON DELETE CASCADE
);

ALTER TABLE bagdrop_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE bagdrop_configs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bagdrop_configs USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);
