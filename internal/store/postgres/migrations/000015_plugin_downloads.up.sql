CREATE TABLE plugin_downloads (
    organization_id uuid NOT NULL,
    plugin_id uuid NOT NULL,
    version text NOT NULL,
    platform text NOT NULL,
    day date NOT NULL,
    count bigint NOT NULL,
    PRIMARY KEY (plugin_id, version, platform, day),
    FOREIGN KEY (plugin_id, organization_id) REFERENCES plugins(id, organization_id) ON DELETE CASCADE
);

ALTER TABLE plugin_downloads ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_downloads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plugin_downloads
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);
