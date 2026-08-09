-- Assignment-triggered scans coalesce per build. The tenant columns lead the
-- key so row-level security has the same shape as the scan history tables.
CREATE TABLE pending_scans (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    build_id text NOT NULL,
    enqueued_at timestamptz NOT NULL,
    reason text NOT NULL,
    PRIMARY KEY (organization_id, project_id, build_id),
    FOREIGN KEY (organization_id, project_id, build_id)
        REFERENCES builds (organization_id, project_id, id) ON DELETE CASCADE
);

CREATE INDEX pending_scans_fifo
    ON pending_scans (enqueued_at, organization_id, project_id, build_id);

ALTER TABLE pending_scans ENABLE ROW LEVEL SECURITY;
ALTER TABLE pending_scans FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pending_scans USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);
