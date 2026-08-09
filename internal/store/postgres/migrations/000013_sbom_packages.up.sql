-- Parsed SBOM packages are client-reported inventory, not verified facts about
-- an image. The raw document remains authoritative and is retained unchanged.
--
-- parse_status defaults to pending so a previous application release can keep
-- inserting its old named column set while old and new releases overlap. It
-- also marks SBOMs stored before this migration for lazy projection on their
-- first packages read.
ALTER TABLE sboms ADD COLUMN parse_status text NOT NULL DEFAULT 'pending'
    CHECK (parse_status IN ('pending', 'parsed', 'unparseable'));
ALTER TABLE sboms ADD COLUMN parse_error text NOT NULL DEFAULT '';

CREATE TABLE sbom_packages (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    sbom_id text NOT NULL,
    name text NOT NULL,
    version text NOT NULL DEFAULT '',
    purl text NOT NULL DEFAULT '',
    licenses jsonb NOT NULL DEFAULT '[]',
    -- CycloneDX components may be nested. The read API is flat, but these
    -- arrays of bom-ref/name segments preserve every containment path so the
    -- source document's hierarchy is not discarded by the projection.
    component_paths jsonb NOT NULL DEFAULT '[]',
    PRIMARY KEY (organization_id, project_id, sbom_id, name, version, purl),
    FOREIGN KEY (organization_id, project_id, sbom_id)
        REFERENCES sboms (organization_id, project_id, id) ON DELETE CASCADE
);

ALTER TABLE sbom_packages ENABLE ROW LEVEL SECURITY;
ALTER TABLE sbom_packages FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sbom_packages USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);
