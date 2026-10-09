CREATE TABLE plugins (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES plugin_registries(organization_id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (
        char_length(name) <= 63
        AND name ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
        AND name NOT LIKE 'packer-%'
    ),
    source_kind text NOT NULL CHECK (source_kind IN ('upload', 'releases-hashicorp', 'github')),
    source_repository text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name),
    UNIQUE (id, organization_id)
);

CREATE TABLE plugin_versions (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    plugin_id uuid NOT NULL,
    version text NOT NULL,
    revoked boolean NOT NULL DEFAULT false,
    protocol_version text NULL,
    listed_platforms text[] NOT NULL,
    sums_key text NOT NULL,
    sums_size bigint NOT NULL,
    signature_key text NULL,
    signature_size bigint NULL,
    manifest_key text NULL,
    manifest_size bigint NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (plugin_id, organization_id) REFERENCES plugins(id, organization_id) ON DELETE RESTRICT,
    UNIQUE (plugin_id, version),
    UNIQUE (id, organization_id)
);

CREATE TABLE plugin_files (
    organization_id uuid NOT NULL,
    version_id uuid NOT NULL,
    filename text NOT NULL,
    os text NOT NULL,
    arch text NOT NULL,
    sha256 text NOT NULL,
    size bigint NOT NULL,
    object_key text NOT NULL,
    PRIMARY KEY (version_id, filename),
    FOREIGN KEY (version_id, organization_id) REFERENCES plugin_versions(id, organization_id) ON DELETE CASCADE
);

ALTER TABLE plugins ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugins FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plugins
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);

ALTER TABLE plugin_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_versions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plugin_versions
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);

ALTER TABLE plugin_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_files FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plugin_files
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);
