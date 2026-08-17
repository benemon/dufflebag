-- Dufflebag 0.1.0 baseline, generated from a PostgreSQL 17 schema-only dump
-- after applying the complete pre-0.1.0 migration chain.

-- Channel assignment history is append-only. Bucket deletion may remove a
-- row only after its referenced version has gone in the same cascade;
-- unassignment markers (version_id IS NULL) remain immutable and outlive what
-- they describe.
CREATE FUNCTION public.reject_channel_assignment_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND OLD.version_id IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM versions
           WHERE organization_id = OLD.organization_id
             AND project_id = OLD.project_id
             AND id = OLD.version_id
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'channel assignment history is append-only';
END
$$;

-- Scan runs and findings are immutable history. The only permitted update is
-- the one-shot integrity MAC computed from the stored row after insertion;
-- bounded-retention deletion and foreign-key cascades remain possible.
CREATE FUNCTION public.reject_scan_row_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.integrity_mac IS NULL
        AND to_jsonb(NEW) - 'integrity_mac' = to_jsonb(OLD) - 'integrity_mac' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'scan history is immutable';
END
$$;

-- Bucket scope is carried on every row below a bucket so row-level security
-- can enforce the same boundary without trusting joins in each query. The
-- composite parent keys below prove that a copied bucket id cannot drift from
-- the aggregate it describes.
--
-- Nullable integrity MACs are written and verified only on encrypted
-- deployments. They make provenance rows resist alteration and make a
-- database-inserted identity fail authentication: database write access is
-- not administration.
CREATE TABLE public.artifacts (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    build_id text NOT NULL,
    external_identifier text NOT NULL,
    region text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    integrity_mac bytea,
    bucket_id text NOT NULL
);

ALTER TABLE ONLY public.artifacts FORCE ROW LEVEL SECURITY;

-- Audit targets are platform-scoped operational configuration, not tenant
-- data, so they deliberately have no row-level security policy.
--
-- The maximum of three targets is structural rather than checked in Go. The
-- NOT NULL, CHECK, and UNIQUE constraints on slot cooperate so even a direct
-- or concurrent insert cannot create a fourth target; NOT NULL is essential
-- because PostgreSQL accepts UNKNOWN from a CHECK and does not collide NULLs
-- under UNIQUE.
CREATE TABLE public.audit_targets (
    id uuid NOT NULL,
    slot smallint NOT NULL,
    path text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT audit_targets_slot_check CHECK (((slot >= 1) AND (slot <= 3)))
);

-- Deliberately no foreign key to buckets: an association is outbound intent
-- and must outlive its local bucket so the reconciler can remove the remote
-- copy.
CREATE TABLE public.bagdrop_associations (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    bucket_name text NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    first_attempted_at timestamp with time zone,
    last_synced_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    last_attempt_at timestamp with time zone,
    last_sync_error text,
    CONSTRAINT bagdrop_associations_state_check CHECK ((state = ANY (ARRAY['active'::text, 'pending_removal'::text])))
);

ALTER TABLE ONLY public.bagdrop_associations FORCE ROW LEVEL SECURITY;

CREATE TABLE public.bagdrop_configs (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    adapter text NOT NULL,
    destination jsonb NOT NULL,
    sealed_secret bytea NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    last_verification jsonb,
    last_verified_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

ALTER TABLE ONLY public.bagdrop_configs FORCE ROW LEVEL SECURITY;

CREATE TABLE public.buckets (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    labels jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

ALTER TABLE ONLY public.buckets FORCE ROW LEVEL SECURITY;

-- Per-build scan currency has its own row and integrity MAC so the build MAC
-- contract remains untouched. current_findings_run_id moves only forward;
-- latest_attempt_run_id records the newest attempt of any outcome.
CREATE TABLE public.build_scan_state (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    build_id text NOT NULL,
    current_findings_run_id text,
    latest_attempt_run_id text NOT NULL,
    integrity_mac bytea,
    bucket_id text NOT NULL
);

ALTER TABLE ONLY public.build_scan_state FORCE ROW LEVEL SECURITY;

-- Packer supplies parent_version_id and parent_channel_id with the terminal
-- build update. metadata is opaque bytes: raw JSON when unencrypted or a
-- versioned AES-GCM envelope when encrypted; SQL never filters or joins on it.
CREATE TABLE public.builds (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    version_id text NOT NULL,
    component_type text NOT NULL,
    status text NOT NULL,
    platform text NOT NULL,
    metadata_seen boolean DEFAULT false NOT NULL,
    packer_run_uuid text DEFAULT ''::text NOT NULL,
    labels jsonb DEFAULT '{}'::jsonb NOT NULL,
    source_external_identifier text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    parent_version_id text,
    parent_channel_id text,
    metadata bytea DEFAULT '\x7b7d'::bytea NOT NULL,
    integrity_mac bytea,
    bucket_id text NOT NULL,
    CONSTRAINT builds_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'running'::text, 'done'::text, 'failed'::text, 'cancelled'::text])))
);

ALTER TABLE ONLY public.builds FORCE ROW LEVEL SECURITY;

-- Assignment rows are an append-only audit trail and deliberately do not
-- reference channels: deleting a channel must not erase what was once promoted
-- to it. An unassignment is represented by a new row with no version rather
-- than deletion. author_id distinguishes manual assignment by a principal
-- from service-authored completion assignment.
CREATE TABLE public.channel_assignments (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    channel_id text NOT NULL,
    version_id text,
    assigned_at timestamp with time zone NOT NULL,
    author_id text DEFAULT ''::text NOT NULL,
    integrity_mac bytea,
    bucket_id text NOT NULL
);

ALTER TABLE ONLY public.channel_assignments FORCE ROW LEVEL SECURITY;

-- Every bucket carries a managed, restricted channel named "latest". The
-- application creates it in the bucket transaction and assigns each completed
-- version to it; the schema keeps managed explicit so user channels remain
-- distinguishable.
CREATE TABLE public.channels (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    bucket_id text NOT NULL,
    name text NOT NULL,
    restricted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    managed boolean DEFAULT false NOT NULL
);

ALTER TABLE ONLY public.channels FORCE ROW LEVEL SECURITY;

-- Whether an instance encrypts at rest is decided on first boot and never
-- changes. Startup compares configuration with this marker and refuses a
-- mismatch instead of maintaining dual-read paths and backfill jobs.
CREATE TABLE public.encryption_mode (
    id boolean DEFAULT true NOT NULL,
    encrypted boolean NOT NULL,
    recorded_at timestamp with time zone NOT NULL,
    CONSTRAINT encryption_mode_id_check CHECK (id)
);

-- Singleton used to serialize initialization. The durable one-way-door is the
-- existence of a platform root principal; locking this row prevents concurrent
-- callers from both observing no root. Recovery shares are returned once and
-- never stored; NULL recovery fields identify an older claim that cannot use
-- the recovery ceremony.
CREATE TABLE public.instance (
    id boolean DEFAULT true NOT NULL,
    initialized_at timestamp with time zone NOT NULL,
    recovery_digest bytea,
    recovery_threshold integer,
    CONSTRAINT instance_id_check CHECK (id),
    CONSTRAINT instance_recovery_threshold_check CHECK (((recovery_threshold IS NULL) OR (recovery_threshold >= 1)))
);

-- Locally generated keys are wrapped by an external key-encryption key. The
-- recorded KEK reference lets rotation rewrap only affected rows without
-- touching payloads; unencrypted deployments leave this table empty.
CREATE TABLE public.keyring (
    purpose text NOT NULL,
    version integer NOT NULL,
    wrapped_key bytea NOT NULL,
    kek_ref text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT keyring_version_check CHECK ((version >= 1))
);

CREATE TABLE public.organizations (
    id uuid NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT organizations_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 200)))
);

-- Assignment-triggered scans coalesce per build. Tenant columns lead the key
-- so row-level security has the same shape as the scan-history tables.
CREATE TABLE public.pending_scans (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    build_id text NOT NULL,
    enqueued_at timestamp with time zone NOT NULL,
    reason text NOT NULL,
    bucket_id text NOT NULL
);

ALTER TABLE ONLY public.pending_scans FORCE ROW LEVEL SECURITY;

CREATE TABLE public.pins (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    pinned_at timestamp with time zone NOT NULL,
    pinned_by text DEFAULT ''::text NOT NULL,
    bucket_id text NOT NULL
);

ALTER TABLE ONLY public.pins FORCE ROW LEVEL SECURITY;

-- Expired secrets remain stored so authentication can report expiry rather
-- than an undifferentiated failure; deletion, not expiry, means revocation.
CREATE TABLE public.principal_secrets (
    id text NOT NULL,
    principal_id text NOT NULL,
    encoded_hash text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone,
    integrity_mac bytea,
    expires_at timestamp with time zone
);

-- A role has no default because it is part of what a principal is; authority
-- must be chosen by the caller. NULL organization_id represents PLATFORM scope
-- and is coherent only for a root. A bucket binding is narrower than a project
-- binding, never an alternative, and the composite foreign key proves the
-- bucket belongs to the principal's organization and project.
-- NULL project_id represents organization scope; the zero UUID is a distinct,
-- real project-shaped value and is deliberately unstorable.
--
-- Deliberately no row-level security: authentication looks up client_id before
-- the caller's tenant is known. Tenant settings would make authentication
-- impossible or trust an unauthenticated scope claim; the pre-authentication
-- read exposes only an Argon2id hash, never plaintext.
CREATE TABLE public.principals (
    id text NOT NULL,
    name text NOT NULL,
    client_id text NOT NULL,
    organization_id uuid,
    project_id uuid,
    created_at timestamp with time zone NOT NULL,
    role text NOT NULL,
    integrity_mac bytea,
    bucket_id text,
    CONSTRAINT principals_bucket_scope_is_coherent CHECK (((bucket_id IS NULL) OR (project_id IS NOT NULL))),
    CONSTRAINT principals_project_id_not_zero CHECK (((project_id IS NULL) OR (project_id <> '00000000-0000-0000-0000-000000000000'::uuid))),
    CONSTRAINT principals_scope_is_coherent CHECK ((((organization_id IS NOT NULL) AND (role <> 'root'::text)) OR ((organization_id IS NULL) AND (project_id IS NULL) AND (role = 'root'::text))))
);

CREATE TABLE public.projects (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT projects_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 200)))
);

-- Parsed packages are a client-reported projection, not verified facts about
-- an image; the unchanged raw document remains authoritative. CycloneDX
-- components may be nested, so component_paths preserves every bom-ref/name
-- containment path even though the read API is flat.
CREATE TABLE public.sbom_packages (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    sbom_id text NOT NULL,
    name text NOT NULL,
    version text DEFAULT ''::text NOT NULL,
    purl text DEFAULT ''::text NOT NULL,
    licenses jsonb DEFAULT '[]'::jsonb NOT NULL,
    component_paths jsonb DEFAULT '[]'::jsonb NOT NULL,
    bucket_id text NOT NULL
);

ALTER TABLE ONLY public.sbom_packages FORCE ROW LEVEL SECURITY;

-- SBOM storage, not analysis. The bytes remain exactly as sent in object
-- storage; this row keeps only the key. Uniqueness by build and name reflects
-- UploadSbom's PUT identity. parse_status starts pending to support lazy
-- projection on the first package read.
CREATE TABLE public.sboms (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    build_id text NOT NULL,
    name text NOT NULL,
    format text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    parse_status text DEFAULT 'pending'::text NOT NULL,
    parse_error text DEFAULT ''::text NOT NULL,
    object_key text NOT NULL,
    bucket_id text NOT NULL,
    CONSTRAINT sboms_parse_status_check CHECK ((parse_status = ANY (ARRAY['pending'::text, 'parsed'::text, 'unparseable'::text])))
);

ALTER TABLE ONLY public.sboms FORCE ROW LEVEL SECURITY;

-- Findings carry the complete sbom_packages identity: purl alone is neither
-- unique nor guaranteed to be non-empty.
CREATE TABLE public.scan_findings (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    run_id text NOT NULL,
    sbom_id text NOT NULL,
    package_name text NOT NULL,
    package_version text DEFAULT ''::text NOT NULL,
    purl text DEFAULT ''::text NOT NULL,
    advisory_id text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    aliases jsonb DEFAULT '[]'::jsonb NOT NULL,
    related jsonb DEFAULT '[]'::jsonb NOT NULL,
    published timestamp with time zone,
    modified timestamp with time zone,
    withdrawn timestamp with time zone,
    fixed_versions jsonb DEFAULT '[]'::jsonb NOT NULL,
    severities jsonb DEFAULT '[]'::jsonb NOT NULL,
    derived_severity text NOT NULL,
    first_seen_at timestamp with time zone NOT NULL,
    integrity_mac bytea,
    bucket_id text NOT NULL,
    CONSTRAINT scan_findings_derived_severity_check CHECK ((derived_severity = ANY (ARRAY['unknown'::text, 'negligible'::text, 'low'::text, 'medium'::text, 'high'::text, 'critical'::text])))
);

ALTER TABLE ONLY public.scan_findings FORCE ROW LEVEL SECURITY;

-- Run order is allocated before provider egress, including failed attempts.
-- A tenant-scoped counter table avoids a separate sequence USAGE grant and
-- inherits the ordinary table grants and tenant policy.
CREATE TABLE public.scan_run_counters (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    next_sequence bigint DEFAULT 1 NOT NULL
);

ALTER TABLE ONLY public.scan_run_counters FORCE ROW LEVEL SECURITY;

-- A run is inserted exactly once in a terminal state; current state lives in
-- build_scan_state rather than by mutating this history.
CREATE TABLE public.scan_runs (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    build_id text NOT NULL,
    run_sequence bigint NOT NULL,
    status text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    adapter text NOT NULL,
    engine text NOT NULL,
    database_revision text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    transcript_digest text NOT NULL,
    coverage jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL,
    integrity_mac bytea,
    bucket_id text NOT NULL,
    CONSTRAINT scan_runs_status_check CHECK ((status = ANY (ARRAY['succeeded'::text, 'failed'::text])))
);

ALTER TABLE ONLY public.scan_runs FORCE ROW LEVEL SECURITY;

-- Transcript locators are separate from immutable runs so seven-day expiry is
-- a DELETE rather than an update. The run retains the digest permanently, and
-- ciphertext associated data binds the object to its run id.
CREATE TABLE public.scan_transcripts (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    run_id text NOT NULL,
    object_key text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    integrity_mac bytea,
    bucket_id text NOT NULL
);

ALTER TABLE ONLY public.scan_transcripts FORCE ROW LEVEL SECURITY;

-- Revocation is time-derived: revoke_at is the effect time and no job flips a
-- state flag. The ancestor identity is denormalized at revocation time so
-- rendering remains join-free, and its fields travel all-or-nothing.
CREATE TABLE public.versions (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id text NOT NULL,
    bucket_id text NOT NULL,
    fingerprint text NOT NULL,
    template_type text NOT NULL,
    complete boolean DEFAULT false NOT NULL,
    sequence integer,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    author_id text DEFAULT ''::text NOT NULL,
    integrity_mac bytea,
    revoke_at timestamp with time zone,
    revocation_message text,
    revocation_author text,
    revocation_inherited_from_id text,
    revocation_inherited_from_bucket text,
    revocation_inherited_from_fingerprint text,
    revocation_inherited_from_name text,
    CONSTRAINT versions_check CHECK (((complete AND (sequence >= 1)) OR ((NOT complete) AND (sequence IS NULL)))),
    CONSTRAINT versions_revocation_ancestor_shape CHECK ((((revocation_inherited_from_id IS NULL) = (revocation_inherited_from_bucket IS NULL)) AND ((revocation_inherited_from_id IS NULL) = (revocation_inherited_from_fingerprint IS NULL)) AND ((revocation_inherited_from_id IS NULL) = (revocation_inherited_from_name IS NULL)))),
    CONSTRAINT versions_revocation_shape CHECK ((((revoke_at IS NOT NULL) AND (revocation_author IS NOT NULL)) OR ((revoke_at IS NULL) AND (revocation_message IS NULL) AND (revocation_author IS NULL) AND (revocation_inherited_from_id IS NULL)))),
    CONSTRAINT versions_template_type_check CHECK ((template_type = ANY (ARRAY['HCL2'::text, 'JSON'::text])))
);

ALTER TABLE ONLY public.versions FORCE ROW LEVEL SECURITY;

CREATE TABLE public.webhook_deliveries (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id uuid NOT NULL,
    webhook_id uuid NOT NULL,
    event_id text NOT NULL,
    operation text NOT NULL,
    status text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    first_attempted_at timestamp with time zone,
    last_attempted_at timestamp with time zone,
    next_attempt_at timestamp with time zone,
    response_code integer,
    detail text,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT webhook_deliveries_attempt_count_check CHECK (((attempt_count >= 0) AND (attempt_count <= 5))),
    CONSTRAINT webhook_deliveries_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'retrying'::text, 'delivered'::text, 'failed'::text, 'refused'::text])))
);

ALTER TABLE ONLY public.webhook_deliveries FORCE ROW LEVEL SECURITY;

CREATE TABLE public.webhook_outbox (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    event_id text NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    operation text NOT NULL,
    target jsonb NOT NULL,
    actor jsonb NOT NULL,
    payload jsonb NOT NULL,
    available_at timestamp with time zone NOT NULL
);

ALTER TABLE ONLY public.webhook_outbox FORCE ROW LEVEL SECURITY;

CREATE TABLE public.webhooks (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id uuid NOT NULL,
    name text NOT NULL,
    url text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    sealed_secret bytea,
    events text[] DEFAULT '{}'::text[] NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    last_verification_at timestamp with time zone,
    last_verification_error text,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT webhooks_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'active'::text])))
);

ALTER TABLE ONLY public.webhooks FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY public.artifacts
    ADD CONSTRAINT artifacts_organization_id_project_id_build_id_external_iden_key UNIQUE (organization_id, project_id, build_id, external_identifier);

ALTER TABLE ONLY public.artifacts
    ADD CONSTRAINT artifacts_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.audit_targets
    ADD CONSTRAINT audit_targets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.audit_targets
    ADD CONSTRAINT audit_targets_slot_key UNIQUE (slot);

ALTER TABLE ONLY public.bagdrop_associations
    ADD CONSTRAINT bagdrop_associations_pkey PRIMARY KEY (organization_id, project_id, bucket_name);

ALTER TABLE ONLY public.bagdrop_configs
    ADD CONSTRAINT bagdrop_configs_pkey PRIMARY KEY (organization_id, project_id);

ALTER TABLE ONLY public.buckets
    ADD CONSTRAINT buckets_organization_id_project_id_name_key UNIQUE (organization_id, project_id, name);

ALTER TABLE ONLY public.buckets
    ADD CONSTRAINT buckets_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.build_scan_state
    ADD CONSTRAINT build_scan_state_pkey PRIMARY KEY (organization_id, project_id, build_id);

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_bucket_id_id_key UNIQUE (organization_id, project_id, bucket_id, id);

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_organization_id_project_id_version_id_component_type_key UNIQUE (organization_id, project_id, version_id, component_type);

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.channel_assignments
    ADD CONSTRAINT channel_assignments_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_organization_id_project_id_bucket_id_name_key UNIQUE (organization_id, project_id, bucket_id, name);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.encryption_mode
    ADD CONSTRAINT encryption_mode_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.instance
    ADD CONSTRAINT instance_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.keyring
    ADD CONSTRAINT keyring_pkey PRIMARY KEY (purpose, version);

ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_name_key UNIQUE (name);

ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.pending_scans
    ADD CONSTRAINT pending_scans_pkey PRIMARY KEY (organization_id, project_id, build_id);

ALTER TABLE ONLY public.pins
    ADD CONSTRAINT pins_pkey PRIMARY KEY (organization_id, project_id, bucket_id);

ALTER TABLE ONLY public.principal_secrets
    ADD CONSTRAINT principal_secrets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.principals
    ADD CONSTRAINT principals_client_id_key UNIQUE (client_id);

ALTER TABLE ONLY public.principals
    ADD CONSTRAINT principals_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_organization_id_id_key UNIQUE (organization_id, id);

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_organization_id_name_key UNIQUE (organization_id, name);

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.sbom_packages
    ADD CONSTRAINT sbom_packages_pkey PRIMARY KEY (organization_id, project_id, sbom_id, name, version, purl);

ALTER TABLE ONLY public.sboms
    ADD CONSTRAINT sboms_bucket_id_id_key UNIQUE (organization_id, project_id, bucket_id, id);

ALTER TABLE ONLY public.sboms
    ADD CONSTRAINT sboms_organization_id_project_id_build_id_name_key UNIQUE (organization_id, project_id, build_id, name);

ALTER TABLE ONLY public.sboms
    ADD CONSTRAINT sboms_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.scan_findings
    ADD CONSTRAINT scan_findings_pkey PRIMARY KEY (organization_id, project_id, run_id, sbom_id, package_name, package_version, purl, advisory_id);

ALTER TABLE ONLY public.scan_run_counters
    ADD CONSTRAINT scan_run_counters_pkey PRIMARY KEY (organization_id, project_id);

ALTER TABLE ONLY public.scan_runs
    ADD CONSTRAINT scan_runs_bucket_id_id_key UNIQUE (organization_id, project_id, bucket_id, id);

ALTER TABLE ONLY public.scan_runs
    ADD CONSTRAINT scan_runs_organization_id_project_id_run_sequence_key UNIQUE (organization_id, project_id, run_sequence);

ALTER TABLE ONLY public.scan_runs
    ADD CONSTRAINT scan_runs_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.scan_transcripts
    ADD CONSTRAINT scan_transcripts_pkey PRIMARY KEY (organization_id, project_id, run_id);

ALTER TABLE ONLY public.versions
    ADD CONSTRAINT versions_bucket_id_id_key UNIQUE (organization_id, project_id, bucket_id, id);

ALTER TABLE ONLY public.versions
    ADD CONSTRAINT versions_organization_id_project_id_bucket_id_fingerprint_key UNIQUE (organization_id, project_id, bucket_id, fingerprint);

ALTER TABLE ONLY public.versions
    ADD CONSTRAINT versions_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_organization_id_project_id_webhook_id_ev_key UNIQUE (organization_id, project_id, webhook_id, event_id);

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_pkey PRIMARY KEY (organization_id, project_id, id);

ALTER TABLE ONLY public.webhook_outbox
    ADD CONSTRAINT webhook_outbox_pkey PRIMARY KEY (organization_id, project_id, event_id);

ALTER TABLE ONLY public.webhooks
    ADD CONSTRAINT webhooks_pkey PRIMARY KEY (organization_id, project_id, id);

CREATE INDEX artifacts_external_identifier ON public.artifacts USING btree (organization_id, project_id, external_identifier);

-- Version projections resolve recorded parents and ask whether a version has
-- descendants; without this index, a list read scans builds once per version.
CREATE INDEX builds_parent_version_id_idx ON public.builds USING btree (parent_version_id) WHERE (parent_version_id IS NOT NULL);

CREATE INDEX pending_scans_fifo ON public.pending_scans USING btree (enqueued_at, organization_id, project_id, build_id);

CREATE INDEX scan_runs_build ON public.scan_runs USING btree (organization_id, project_id, build_id, run_sequence);

-- Sequence uniqueness is tenant-qualified because bucket ids are unique only
-- within a tenant; a bare (bucket_id, sequence) key would let one tenant reject
-- another tenant's insert.
CREATE UNIQUE INDEX versions_bucket_sequence ON public.versions USING btree (organization_id, project_id, bucket_id, sequence) WHERE (sequence IS NOT NULL);

CREATE INDEX webhook_deliveries_retry_idx ON public.webhook_deliveries USING btree (organization_id, project_id, next_attempt_at) WHERE (status = ANY (ARRAY['pending'::text, 'retrying'::text]));

CREATE INDEX webhook_deliveries_ring_idx ON public.webhook_deliveries USING btree (organization_id, project_id, webhook_id, created_at DESC, id DESC);

CREATE INDEX webhook_outbox_available_idx ON public.webhook_outbox USING btree (available_at, organization_id, project_id, event_id);

CREATE TRIGGER channel_assignments_append_only BEFORE DELETE OR UPDATE ON public.channel_assignments FOR EACH ROW EXECUTE FUNCTION public.reject_channel_assignment_change();

CREATE TRIGGER scan_findings_immutable BEFORE UPDATE ON public.scan_findings FOR EACH ROW EXECUTE FUNCTION public.reject_scan_row_change();

CREATE TRIGGER scan_runs_immutable BEFORE UPDATE ON public.scan_runs FOR EACH ROW EXECUTE FUNCTION public.reject_scan_row_change();

ALTER TABLE ONLY public.artifacts
    ADD CONSTRAINT artifacts_bucket_build_fkey FOREIGN KEY (organization_id, project_id, bucket_id, build_id) REFERENCES public.builds(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.artifacts
    ADD CONSTRAINT artifacts_organization_id_project_id_build_id_fkey FOREIGN KEY (organization_id, project_id, build_id) REFERENCES public.builds(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.bagdrop_associations
    ADD CONSTRAINT bagdrop_associations_organization_id_project_id_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.bagdrop_configs(organization_id, project_id) ON DELETE CASCADE;

ALTER TABLE ONLY public.bagdrop_configs
    ADD CONSTRAINT bagdrop_configs_organization_id_project_id_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.buckets
    ADD CONSTRAINT buckets_organization_id_project_id_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.build_scan_state
    ADD CONSTRAINT build_scan_state_bucket_build_fkey FOREIGN KEY (organization_id, project_id, bucket_id, build_id) REFERENCES public.builds(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.build_scan_state
    ADD CONSTRAINT build_scan_state_organization_id_project_id_build_id_fkey FOREIGN KEY (organization_id, project_id, build_id) REFERENCES public.builds(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.build_scan_state
    ADD CONSTRAINT build_scan_state_organization_id_project_id_current_findin_fkey FOREIGN KEY (organization_id, project_id, current_findings_run_id) REFERENCES public.scan_runs(organization_id, project_id, id);

ALTER TABLE ONLY public.build_scan_state
    ADD CONSTRAINT build_scan_state_organization_id_project_id_latest_attempt_fkey FOREIGN KEY (organization_id, project_id, latest_attempt_run_id) REFERENCES public.scan_runs(organization_id, project_id, id);

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_bucket_version_fkey FOREIGN KEY (organization_id, project_id, bucket_id, version_id) REFERENCES public.versions(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_organization_id_project_id_version_id_fkey FOREIGN KEY (organization_id, project_id, version_id) REFERENCES public.versions(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.channel_assignments
    ADD CONSTRAINT channel_assignments_bucket_version_fkey FOREIGN KEY (organization_id, project_id, bucket_id, version_id) REFERENCES public.versions(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.channel_assignments
    ADD CONSTRAINT channel_assignments_organization_id_project_id_version_id_fkey FOREIGN KEY (organization_id, project_id, version_id) REFERENCES public.versions(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_organization_id_project_id_bucket_id_fkey FOREIGN KEY (organization_id, project_id, bucket_id) REFERENCES public.buckets(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.pending_scans
    ADD CONSTRAINT pending_scans_bucket_build_fkey FOREIGN KEY (organization_id, project_id, bucket_id, build_id) REFERENCES public.builds(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.pending_scans
    ADD CONSTRAINT pending_scans_organization_id_project_id_build_id_fkey FOREIGN KEY (organization_id, project_id, build_id) REFERENCES public.builds(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.pins
    ADD CONSTRAINT pins_bucket_fkey FOREIGN KEY (organization_id, project_id, bucket_id) REFERENCES public.buckets(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.principal_secrets
    ADD CONSTRAINT principal_secrets_principal_id_fkey FOREIGN KEY (principal_id) REFERENCES public.principals(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.principals
    ADD CONSTRAINT principals_bucket_scope_fkey FOREIGN KEY (organization_id, project_id, bucket_id) REFERENCES public.buckets(organization_id, project_id, id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.principals
    ADD CONSTRAINT principals_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.principals
    ADD CONSTRAINT principals_project_scope_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.sbom_packages
    ADD CONSTRAINT sbom_packages_bucket_sbom_fkey FOREIGN KEY (organization_id, project_id, bucket_id, sbom_id) REFERENCES public.sboms(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.sbom_packages
    ADD CONSTRAINT sbom_packages_organization_id_project_id_sbom_id_fkey FOREIGN KEY (organization_id, project_id, sbom_id) REFERENCES public.sboms(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.sboms
    ADD CONSTRAINT sboms_bucket_build_fkey FOREIGN KEY (organization_id, project_id, bucket_id, build_id) REFERENCES public.builds(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.sboms
    ADD CONSTRAINT sboms_organization_id_project_id_build_id_fkey FOREIGN KEY (organization_id, project_id, build_id) REFERENCES public.builds(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.scan_findings
    ADD CONSTRAINT scan_findings_bucket_run_fkey FOREIGN KEY (organization_id, project_id, bucket_id, run_id) REFERENCES public.scan_runs(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.scan_findings
    ADD CONSTRAINT scan_findings_organization_id_project_id_run_id_fkey FOREIGN KEY (organization_id, project_id, run_id) REFERENCES public.scan_runs(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.scan_findings
    ADD CONSTRAINT scan_findings_organization_id_project_id_sbom_id_package_n_fkey FOREIGN KEY (organization_id, project_id, sbom_id, package_name, package_version, purl) REFERENCES public.sbom_packages(organization_id, project_id, sbom_id, name, version, purl) ON DELETE CASCADE;

ALTER TABLE ONLY public.scan_runs
    ADD CONSTRAINT scan_runs_bucket_build_fkey FOREIGN KEY (organization_id, project_id, bucket_id, build_id) REFERENCES public.builds(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.scan_runs
    ADD CONSTRAINT scan_runs_organization_id_project_id_build_id_fkey FOREIGN KEY (organization_id, project_id, build_id) REFERENCES public.builds(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.scan_transcripts
    ADD CONSTRAINT scan_transcripts_bucket_run_fkey FOREIGN KEY (organization_id, project_id, bucket_id, run_id) REFERENCES public.scan_runs(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.scan_transcripts
    ADD CONSTRAINT scan_transcripts_organization_id_project_id_run_id_fkey FOREIGN KEY (organization_id, project_id, run_id) REFERENCES public.scan_runs(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.versions
    ADD CONSTRAINT versions_organization_id_project_id_bucket_id_fkey FOREIGN KEY (organization_id, project_id, bucket_id) REFERENCES public.buckets(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_organization_id_project_id_webhook_id_fkey FOREIGN KEY (organization_id, project_id, webhook_id) REFERENCES public.webhooks(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.webhook_outbox
    ADD CONSTRAINT webhook_outbox_organization_id_project_id_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.webhooks
    ADD CONSTRAINT webhooks_organization_id_project_id_fkey FOREIGN KEY (organization_id, project_id) REFERENCES public.projects(organization_id, id) ON DELETE CASCADE;

ALTER TABLE public.artifacts ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.bagdrop_associations ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.bagdrop_configs ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.buckets ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.build_scan_state ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.builds ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.channel_assignments ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.channels ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.pending_scans ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.pins ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.sbom_packages ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.sboms ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.scan_findings ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.scan_run_counters ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.scan_runs ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.scan_transcripts ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON public.artifacts USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.bagdrop_associations USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid)));

CREATE POLICY tenant_isolation ON public.bagdrop_configs USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid)));

CREATE POLICY tenant_isolation ON public.buckets USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.build_scan_state USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.builds USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.channel_assignments USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.channels USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.pending_scans USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.pins USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.sbom_packages USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.sboms USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.scan_findings USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.scan_run_counters USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid)));

CREATE POLICY tenant_isolation ON public.scan_runs USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.scan_transcripts USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.versions USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.webhook_deliveries USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid)));

CREATE POLICY tenant_isolation ON public.webhook_outbox USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid)));

CREATE POLICY tenant_isolation ON public.webhooks USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid)));

ALTER TABLE public.versions ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.webhook_deliveries ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.webhook_outbox ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.webhooks ENABLE ROW LEVEL SECURITY;
