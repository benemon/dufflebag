-- Bucket scope is carried on every row below a bucket, so row-level security
-- can enforce the same boundary without trusting joins in each query. The
-- parent composite keys prove that a copied bucket id cannot drift from the
-- aggregate it describes.
ALTER TABLE builds ADD COLUMN bucket_id text;
ALTER TABLE artifacts ADD COLUMN bucket_id text;
ALTER TABLE channel_assignments ADD COLUMN bucket_id text;
ALTER TABLE sboms ADD COLUMN bucket_id text;
ALTER TABLE sbom_packages ADD COLUMN bucket_id text;
ALTER TABLE scan_runs ADD COLUMN bucket_id text;
ALTER TABLE scan_findings ADD COLUMN bucket_id text;
ALTER TABLE scan_transcripts ADD COLUMN bucket_id text;
ALTER TABLE build_scan_state ADD COLUMN bucket_id text;
ALTER TABLE pending_scans ADD COLUMN bucket_id text;
ALTER TABLE pins ADD COLUMN bucket_id text;

-- FORCE RLS binds the table owner too. Lift it only for the cross-tenant
-- backfill, then restore it before the migration commits.
DO $$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'buckets', 'versions', 'builds', 'artifacts', 'channels',
        'channel_assignments', 'sboms', 'sbom_packages', 'scan_runs',
        'scan_findings', 'scan_transcripts', 'build_scan_state',
        'pending_scans', 'pins'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', table_name);
    END LOOP;
END
$$;

UPDATE builds
SET bucket_id = versions.bucket_id
FROM versions
WHERE versions.organization_id = builds.organization_id
  AND versions.project_id = builds.project_id
  AND versions.id = builds.version_id;

UPDATE artifacts
SET bucket_id = builds.bucket_id
FROM builds
WHERE builds.organization_id = artifacts.organization_id
  AND builds.project_id = artifacts.project_id
  AND builds.id = artifacts.build_id;

-- The history trigger rejects ordinary updates; this migration changes only
-- the redundant scope proof and restores the trigger before commit.
ALTER TABLE channel_assignments DISABLE TRIGGER channel_assignments_append_only;
UPDATE channel_assignments
SET bucket_id = versions.bucket_id
FROM versions
WHERE versions.organization_id = channel_assignments.organization_id
  AND versions.project_id = channel_assignments.project_id
  AND versions.id = channel_assignments.version_id;
ALTER TABLE channel_assignments ENABLE TRIGGER channel_assignments_append_only;

UPDATE sboms
SET bucket_id = builds.bucket_id
FROM builds
WHERE builds.organization_id = sboms.organization_id
  AND builds.project_id = sboms.project_id
  AND builds.id = sboms.build_id;

UPDATE sbom_packages
SET bucket_id = sboms.bucket_id
FROM sboms
WHERE sboms.organization_id = sbom_packages.organization_id
  AND sboms.project_id = sbom_packages.project_id
  AND sboms.id = sbom_packages.sbom_id;

ALTER TABLE scan_runs DISABLE TRIGGER scan_runs_immutable;
UPDATE scan_runs
SET bucket_id = builds.bucket_id
FROM builds
WHERE builds.organization_id = scan_runs.organization_id
  AND builds.project_id = scan_runs.project_id
  AND builds.id = scan_runs.build_id;
ALTER TABLE scan_runs ENABLE TRIGGER scan_runs_immutable;

ALTER TABLE scan_findings DISABLE TRIGGER scan_findings_immutable;
UPDATE scan_findings
SET bucket_id = scan_runs.bucket_id
FROM scan_runs
WHERE scan_runs.organization_id = scan_findings.organization_id
  AND scan_runs.project_id = scan_findings.project_id
  AND scan_runs.id = scan_findings.run_id;
ALTER TABLE scan_findings ENABLE TRIGGER scan_findings_immutable;

UPDATE scan_transcripts
SET bucket_id = scan_runs.bucket_id
FROM scan_runs
WHERE scan_runs.organization_id = scan_transcripts.organization_id
  AND scan_runs.project_id = scan_transcripts.project_id
  AND scan_runs.id = scan_transcripts.run_id;

UPDATE build_scan_state
SET bucket_id = builds.bucket_id
FROM builds
WHERE builds.organization_id = build_scan_state.organization_id
  AND builds.project_id = build_scan_state.project_id
  AND builds.id = build_scan_state.build_id;

UPDATE pending_scans
SET bucket_id = builds.bucket_id
FROM builds
WHERE builds.organization_id = pending_scans.organization_id
  AND builds.project_id = pending_scans.project_id
  AND builds.id = pending_scans.build_id;

UPDATE pins
SET bucket_id = buckets.id
FROM buckets
WHERE buckets.organization_id = pins.organization_id
  AND buckets.project_id = pins.project_id
  AND buckets.name = pins.bucket_name;

ALTER TABLE builds ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE artifacts ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE channel_assignments ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE sboms ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE sbom_packages ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE scan_runs ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE scan_findings ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE scan_transcripts ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE build_scan_state ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE pending_scans ALTER COLUMN bucket_id SET NOT NULL;
ALTER TABLE pins ALTER COLUMN bucket_id SET NOT NULL;

ALTER TABLE versions ADD CONSTRAINT versions_bucket_id_id_key
    UNIQUE (organization_id, project_id, bucket_id, id);
ALTER TABLE builds ADD CONSTRAINT builds_bucket_id_id_key
    UNIQUE (organization_id, project_id, bucket_id, id);
ALTER TABLE sboms ADD CONSTRAINT sboms_bucket_id_id_key
    UNIQUE (organization_id, project_id, bucket_id, id);
ALTER TABLE scan_runs ADD CONSTRAINT scan_runs_bucket_id_id_key
    UNIQUE (organization_id, project_id, bucket_id, id);

ALTER TABLE builds ADD CONSTRAINT builds_bucket_version_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, version_id)
    REFERENCES versions (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE artifacts ADD CONSTRAINT artifacts_bucket_build_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, build_id)
    REFERENCES builds (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE channel_assignments ADD CONSTRAINT channel_assignments_bucket_version_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, version_id)
    REFERENCES versions (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE sboms ADD CONSTRAINT sboms_bucket_build_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, build_id)
    REFERENCES builds (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE sbom_packages ADD CONSTRAINT sbom_packages_bucket_sbom_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, sbom_id)
    REFERENCES sboms (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE scan_runs ADD CONSTRAINT scan_runs_bucket_build_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, build_id)
    REFERENCES builds (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE scan_findings ADD CONSTRAINT scan_findings_bucket_run_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, run_id)
    REFERENCES scan_runs (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE scan_transcripts ADD CONSTRAINT scan_transcripts_bucket_run_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, run_id)
    REFERENCES scan_runs (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE build_scan_state ADD CONSTRAINT build_scan_state_bucket_build_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, build_id)
    REFERENCES builds (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;
ALTER TABLE pending_scans ADD CONSTRAINT pending_scans_bucket_build_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id, build_id)
    REFERENCES builds (organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE pins DROP CONSTRAINT pins_pkey;
ALTER TABLE pins DROP CONSTRAINT pins_organization_id_project_id_bucket_name_fkey;
ALTER TABLE pins DROP COLUMN bucket_name;
ALTER TABLE pins ADD PRIMARY KEY (organization_id, project_id, bucket_id);
ALTER TABLE pins ADD CONSTRAINT pins_bucket_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id)
    REFERENCES buckets (organization_id, project_id, id) ON DELETE CASCADE;

DO $$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'versions', 'builds', 'artifacts', 'channels', 'channel_assignments',
        'sboms', 'sbom_packages', 'scan_runs', 'scan_findings',
        'scan_transcripts', 'build_scan_state', 'pending_scans', 'pins'
    ]
    LOOP
        EXECUTE format('DROP POLICY tenant_isolation ON %I', table_name);
        EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (
            organization_id = NULLIF(current_setting(''app.tenant_org'', true), '''')::uuid
            AND project_id = NULLIF(current_setting(''app.tenant_project'', true), '''')::uuid
            AND (NULLIF(current_setting(''app.tenant_bucket'', true), '''') IS NULL
                 OR bucket_id = NULLIF(current_setting(''app.tenant_bucket'', true), ''''))
        ) WITH CHECK (
            organization_id = NULLIF(current_setting(''app.tenant_org'', true), '''')::uuid
            AND project_id = NULLIF(current_setting(''app.tenant_project'', true), '''')::uuid
            AND (NULLIF(current_setting(''app.tenant_bucket'', true), '''') IS NULL
                 OR bucket_id = NULLIF(current_setting(''app.tenant_bucket'', true), ''''))
        )', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    END LOOP;
END
$$;

DROP POLICY tenant_isolation ON buckets;
CREATE POLICY tenant_isolation ON buckets USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
    AND (NULLIF(current_setting('app.tenant_bucket', true), '') IS NULL
         OR id = NULLIF(current_setting('app.tenant_bucket', true), ''))
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
    AND (NULLIF(current_setting('app.tenant_bucket', true), '') IS NULL
         OR id = NULLIF(current_setting('app.tenant_bucket', true), ''))
);
ALTER TABLE buckets FORCE ROW LEVEL SECURITY;
