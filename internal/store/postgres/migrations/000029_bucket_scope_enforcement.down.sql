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
        EXECUTE format('DROP POLICY tenant_isolation ON %I', table_name);
        EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (
            organization_id = NULLIF(current_setting(''app.tenant_org'', true), '''')::uuid
            AND project_id = NULLIF(current_setting(''app.tenant_project'', true), '''')::uuid
        ) WITH CHECK (
            organization_id = NULLIF(current_setting(''app.tenant_org'', true), '''')::uuid
            AND project_id = NULLIF(current_setting(''app.tenant_project'', true), '''')::uuid
        )', table_name);
    END LOOP;
END
$$;

ALTER TABLE pins ADD COLUMN bucket_name text;
UPDATE pins
SET bucket_name = buckets.name
FROM buckets
WHERE buckets.organization_id = pins.organization_id
  AND buckets.project_id = pins.project_id
  AND buckets.id = pins.bucket_id;
ALTER TABLE pins ALTER COLUMN bucket_name SET NOT NULL;
ALTER TABLE pins DROP CONSTRAINT pins_pkey;
ALTER TABLE pins DROP CONSTRAINT pins_bucket_fkey;
ALTER TABLE pins ADD PRIMARY KEY (organization_id, project_id, bucket_name);
ALTER TABLE pins ADD CONSTRAINT pins_organization_id_project_id_bucket_name_fkey
    FOREIGN KEY (organization_id, project_id, bucket_name)
    REFERENCES buckets (organization_id, project_id, name) ON DELETE CASCADE;
ALTER TABLE pins DROP COLUMN bucket_id;

ALTER TABLE pending_scans DROP CONSTRAINT pending_scans_bucket_build_fkey;
ALTER TABLE build_scan_state DROP CONSTRAINT build_scan_state_bucket_build_fkey;
ALTER TABLE scan_transcripts DROP CONSTRAINT scan_transcripts_bucket_run_fkey;
ALTER TABLE scan_findings DROP CONSTRAINT scan_findings_bucket_run_fkey;
ALTER TABLE scan_runs DROP CONSTRAINT scan_runs_bucket_build_fkey;
ALTER TABLE sbom_packages DROP CONSTRAINT sbom_packages_bucket_sbom_fkey;
ALTER TABLE sboms DROP CONSTRAINT sboms_bucket_build_fkey;
ALTER TABLE channel_assignments DROP CONSTRAINT channel_assignments_bucket_version_fkey;
ALTER TABLE artifacts DROP CONSTRAINT artifacts_bucket_build_fkey;
ALTER TABLE builds DROP CONSTRAINT builds_bucket_version_fkey;

ALTER TABLE scan_runs DROP CONSTRAINT scan_runs_bucket_id_id_key;
ALTER TABLE sboms DROP CONSTRAINT sboms_bucket_id_id_key;
ALTER TABLE builds DROP CONSTRAINT builds_bucket_id_id_key;
ALTER TABLE versions DROP CONSTRAINT versions_bucket_id_id_key;

ALTER TABLE pending_scans DROP COLUMN bucket_id;
ALTER TABLE build_scan_state DROP COLUMN bucket_id;
ALTER TABLE scan_transcripts DROP COLUMN bucket_id;
ALTER TABLE scan_findings DROP COLUMN bucket_id;
ALTER TABLE scan_runs DROP COLUMN bucket_id;
ALTER TABLE sbom_packages DROP COLUMN bucket_id;
ALTER TABLE sboms DROP COLUMN bucket_id;
ALTER TABLE channel_assignments DROP COLUMN bucket_id;
ALTER TABLE artifacts DROP COLUMN bucket_id;
ALTER TABLE builds DROP COLUMN bucket_id;

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
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    END LOOP;
END
$$;
