CREATE TABLE bagdrop_associations (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    bucket_name text NOT NULL,
    state text NOT NULL DEFAULT 'active',
    first_attempted_at timestamptz NULL,
    last_synced_at timestamptz NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, bucket_name),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES bagdrop_configs (organization_id, project_id) ON DELETE CASCADE,
    CHECK (state IN ('active', 'pending_removal'))
);

-- Deliberately no foreign key to buckets: an association is outbound intent
-- and must outlive its local bucket so the reconciler can remove the remote copy.

ALTER TABLE bagdrop_associations ENABLE ROW LEVEL SECURITY;
ALTER TABLE bagdrop_associations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bagdrop_associations USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);
