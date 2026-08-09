CREATE TABLE organizations (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL
);

CREATE TABLE projects (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL,
    FOREIGN KEY (organization_id)
        REFERENCES organizations (id) ON DELETE RESTRICT,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, name)
);

CREATE TABLE buckets (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    labels jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id) ON DELETE RESTRICT,
    UNIQUE (organization_id, project_id, name)
);

CREATE TABLE versions (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    bucket_id text NOT NULL,
    fingerprint text NOT NULL,
    template_type text NOT NULL CHECK (template_type IN ('HCL2', 'JSON')),
    complete boolean NOT NULL DEFAULT false,
    sequence integer,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, bucket_id)
        REFERENCES buckets (organization_id, project_id, id) ON DELETE CASCADE,
    UNIQUE (organization_id, project_id, bucket_id, fingerprint),
    CHECK ((complete AND sequence >= 1) OR (NOT complete AND sequence IS NULL))
);

CREATE UNIQUE INDEX versions_bucket_sequence
    ON versions (organization_id, project_id, bucket_id, sequence)
    WHERE sequence IS NOT NULL;

CREATE TABLE builds (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    version_id text NOT NULL,
    component_type text NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'running', 'done', 'failed', 'cancelled')),
    platform text NOT NULL,
    metadata_seen boolean NOT NULL DEFAULT false,
    packer_run_uuid text NOT NULL DEFAULT '',
    labels jsonb NOT NULL DEFAULT '{}',
    source_external_identifier text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, version_id)
        REFERENCES versions (organization_id, project_id, id) ON DELETE CASCADE,
    UNIQUE (organization_id, project_id, version_id, component_type)
);

CREATE TABLE artifacts (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    build_id text NOT NULL,
    external_identifier text NOT NULL,
    region text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, build_id)
        REFERENCES builds (organization_id, project_id, id) ON DELETE CASCADE,
    UNIQUE (organization_id, project_id, build_id, external_identifier)
);

CREATE TABLE channels (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    bucket_id text NOT NULL,
    name text NOT NULL,
    restricted boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, bucket_id)
        REFERENCES buckets (organization_id, project_id, id) ON DELETE CASCADE,
    UNIQUE (organization_id, project_id, bucket_id, name)
);

CREATE TABLE channel_assignments (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    channel_id text NOT NULL,
    version_id text NOT NULL,
    assigned_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    -- Deliberately NOT foreign-keyed to channels. Assignment history is an
    -- append-only audit trail: deleting a channel must not erase the record of
    -- what was once promoted to it, and a cascade here would do exactly that.
    -- The rows are historical facts about a channel that existed.
    FOREIGN KEY (organization_id, project_id, version_id)
        REFERENCES versions (organization_id, project_id, id) ON DELETE CASCADE
);

CREATE FUNCTION reject_channel_assignment_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'channel assignment history is append-only';
END
$$;

CREATE TRIGGER channel_assignments_append_only
    BEFORE UPDATE OR DELETE ON channel_assignments
    FOR EACH ROW EXECUTE FUNCTION reject_channel_assignment_change();

DO $$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'buckets', 'versions', 'builds', 'artifacts', 'channels', 'channel_assignments'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (
                organization_id = NULLIF(current_setting(''app.tenant_org'', true), '''')::uuid
                AND project_id = NULLIF(current_setting(''app.tenant_project'', true), '''')::uuid
            ) WITH CHECK (
                organization_id = NULLIF(current_setting(''app.tenant_org'', true), '''')::uuid
                AND project_id = NULLIF(current_setting(''app.tenant_project'', true), '''')::uuid
            )',
            table_name
        );
    END LOOP;
END
$$;
