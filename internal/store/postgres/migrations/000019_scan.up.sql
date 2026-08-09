-- External-scanner store (duf-o0ou.3): scan runs, per-package findings and
-- the per-build pointers. Runs and findings are immutable history — the only
-- writes after insert are the one-shot integrity MAC and bounded retention
-- deletes; currency lives solely in build_scan_state.

-- run_sequence is allocated BEFORE provider egress, so a run's ordering
-- position exists even if the attempt dies mid-flight; the scan_runs row
-- itself is inserted exactly once, already terminal.
--
-- The counter is a tenant-scoped table rather than a Postgres sequence: a
-- sequence needs USAGE granted separately from the table privileges the
-- deployment documents, so a serving role provisioned exactly as the runbook
-- says would fail to allocate. A table under the ordinary tenant policy
-- inherits the grants every other table already has.
CREATE TABLE scan_run_counters (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    next_sequence bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, project_id)
);

CREATE TABLE scan_runs (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    build_id text NOT NULL,
    run_sequence bigint NOT NULL,
    status text NOT NULL CHECK (status IN ('succeeded', 'failed')),
    error text NOT NULL DEFAULT '',
    adapter text NOT NULL,
    engine text NOT NULL,
    database_revision text NOT NULL,
    observed_at timestamptz NOT NULL,
    transcript_digest text NOT NULL,
    coverage jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL,
    integrity_mac bytea,
    PRIMARY KEY (organization_id, project_id, id),
    UNIQUE (organization_id, project_id, run_sequence),
    FOREIGN KEY (organization_id, project_id, build_id)
        REFERENCES builds (organization_id, project_id, id) ON DELETE CASCADE
);

CREATE INDEX scan_runs_build ON scan_runs (organization_id, project_id, build_id, run_sequence);

-- Findings carry the FULL sbom_packages identity: purl alone is neither
-- unique nor guaranteed non-empty, and the projection's primary key is the
-- whole tuple.
CREATE TABLE scan_findings (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    run_id text NOT NULL,
    sbom_id text NOT NULL,
    package_name text NOT NULL,
    package_version text NOT NULL DEFAULT '',
    purl text NOT NULL DEFAULT '',
    advisory_id text NOT NULL,
    summary text NOT NULL DEFAULT '',
    aliases jsonb NOT NULL DEFAULT '[]',
    related jsonb NOT NULL DEFAULT '[]',
    published timestamptz,
    modified timestamptz,
    withdrawn timestamptz,
    fixed_versions jsonb NOT NULL DEFAULT '[]',
    severities jsonb NOT NULL DEFAULT '[]',
    derived_severity text NOT NULL CHECK (derived_severity IN ('unknown', 'negligible', 'low', 'medium', 'high', 'critical')),
    first_seen_at timestamptz NOT NULL,
    integrity_mac bytea,
    PRIMARY KEY (organization_id, project_id, run_id, sbom_id, package_name, package_version, purl, advisory_id),
    FOREIGN KEY (organization_id, project_id, run_id)
        REFERENCES scan_runs (organization_id, project_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, project_id, sbom_id, package_name, package_version, purl)
        REFERENCES sbom_packages (organization_id, project_id, sbom_id, name, version, purl) ON DELETE CASCADE
);

-- Transcript locators live apart from the immutable run row: the seven-day
-- expiry is then a DELETE (row and object) rather than an update against the
-- immutability trigger. The run keeps the digest forever; the ciphertext AAD
-- binds to the run id, so a relocated or swapped object fails decryption.
CREATE TABLE scan_transcripts (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    run_id text NOT NULL,
    object_key text NOT NULL,
    expires_at timestamptz NOT NULL,
    integrity_mac bytea,
    PRIMARY KEY (organization_id, project_id, run_id),
    FOREIGN KEY (organization_id, project_id, run_id)
        REFERENCES scan_runs (organization_id, project_id, id) ON DELETE CASCADE
);

-- The per-build currency pointers, in their own row with their own MAC so the
-- existing build MAC contract is untouched. current_findings_run_id moves
-- only forward (run_sequence guard, enforced in the repository transaction);
-- latest_attempt_run_id records the newest attempt of any outcome.
CREATE TABLE build_scan_state (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    build_id text NOT NULL,
    current_findings_run_id text,
    latest_attempt_run_id text NOT NULL,
    integrity_mac bytea,
    PRIMARY KEY (organization_id, project_id, build_id),
    FOREIGN KEY (organization_id, project_id, build_id)
        REFERENCES builds (organization_id, project_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, project_id, current_findings_run_id)
        REFERENCES scan_runs (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id, latest_attempt_run_id)
        REFERENCES scan_runs (organization_id, project_id, id)
);

-- Immutability: the only permitted UPDATE on run and finding rows is the
-- one-shot integrity MAC, computed from the stored row after insert. DELETE
-- stays possible for the bounded retention path (and FK cascades).
CREATE FUNCTION reject_scan_row_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.integrity_mac IS NULL
        AND to_jsonb(NEW) - 'integrity_mac' = to_jsonb(OLD) - 'integrity_mac' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'scan history is immutable';
END
$$;

CREATE TRIGGER scan_runs_immutable
    BEFORE UPDATE ON scan_runs
    FOR EACH ROW EXECUTE FUNCTION reject_scan_row_change();

CREATE TRIGGER scan_findings_immutable
    BEFORE UPDATE ON scan_findings
    FOR EACH ROW EXECUTE FUNCTION reject_scan_row_change();

DO $$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['scan_run_counters', 'scan_runs', 'scan_findings', 'scan_transcripts', 'build_scan_state']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
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
