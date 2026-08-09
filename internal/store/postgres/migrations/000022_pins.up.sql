CREATE TABLE pins (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    bucket_name text NOT NULL,
    pinned_at timestamptz NOT NULL,
    pinned_by text NOT NULL DEFAULT '',
    PRIMARY KEY (organization_id, project_id, bucket_name),
    FOREIGN KEY (organization_id, project_id, bucket_name)
        REFERENCES buckets (organization_id, project_id, name) ON DELETE CASCADE
);

ALTER TABLE pins ENABLE ROW LEVEL SECURITY;
ALTER TABLE pins FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pins USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);
