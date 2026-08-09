-- SBOM storage, not analysis (dossier §8). The bytes stay exactly as the client
-- sent them — zstd-compressed — because decompressing would be the first step
-- of the analysis pipeline this project deliberately does not have.
--
-- Uniqueness is per build and name: UploadSbom is a PUT, and a re-run build
-- re-uploads under the same name, so the pair is an identity rather than a
-- collision.
CREATE TABLE sboms (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    build_id text NOT NULL,
    name text NOT NULL,
    format text NOT NULL,
    compressed_data bytea NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, build_id)
        REFERENCES builds (organization_id, project_id, id) ON DELETE CASCADE,
    UNIQUE (organization_id, project_id, build_id, name)
);

ALTER TABLE sboms ENABLE ROW LEVEL SECURITY;
ALTER TABLE sboms FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sboms USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);
