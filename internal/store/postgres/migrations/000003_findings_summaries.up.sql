CREATE TABLE public.build_findings_summary (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    bucket_id text NOT NULL,
    build_id text NOT NULL,
    run_id text NOT NULL,
    scanned integer NOT NULL,
    findings integer NOT NULL,
    affected_packages integer NOT NULL,
    worst text,
    counts jsonb NOT NULL,
    computed_at timestamp with time zone NOT NULL,
    integrity_mac bytea,
    CONSTRAINT build_findings_summary_worst_check CHECK ((worst = ANY (ARRAY['unknown'::text, 'negligible'::text, 'low'::text, 'medium'::text, 'high'::text, 'critical'::text])))
);

ALTER TABLE ONLY public.build_findings_summary FORCE ROW LEVEL SECURITY;

CREATE TABLE public.version_findings_summary (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    bucket_id text NOT NULL,
    version_id text NOT NULL,
    findings integer NOT NULL,
    affected_packages integer NOT NULL,
    worst text,
    counts jsonb NOT NULL,
    builds_summarised integer NOT NULL,
    source_run_ids jsonb NOT NULL,
    computed_at timestamp with time zone NOT NULL,
    integrity_mac bytea,
    CONSTRAINT version_findings_summary_worst_check CHECK ((worst = ANY (ARRAY['unknown'::text, 'negligible'::text, 'low'::text, 'medium'::text, 'high'::text, 'critical'::text])))
);

ALTER TABLE ONLY public.version_findings_summary FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY public.build_findings_summary
    ADD CONSTRAINT build_findings_summary_pkey PRIMARY KEY (organization_id, project_id, build_id);

ALTER TABLE ONLY public.version_findings_summary
    ADD CONSTRAINT version_findings_summary_pkey PRIMARY KEY (organization_id, project_id, version_id);

ALTER TABLE ONLY public.build_findings_summary
    ADD CONSTRAINT build_findings_summary_bucket_build_fkey FOREIGN KEY (organization_id, project_id, bucket_id, build_id) REFERENCES public.builds(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.build_findings_summary
    ADD CONSTRAINT build_findings_summary_build_fkey FOREIGN KEY (organization_id, project_id, build_id) REFERENCES public.builds(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.build_findings_summary
    ADD CONSTRAINT build_findings_summary_bucket_run_fkey FOREIGN KEY (organization_id, project_id, bucket_id, run_id) REFERENCES public.scan_runs(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.build_findings_summary
    ADD CONSTRAINT build_findings_summary_run_fkey FOREIGN KEY (organization_id, project_id, run_id) REFERENCES public.scan_runs(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.version_findings_summary
    ADD CONSTRAINT version_findings_summary_bucket_version_fkey FOREIGN KEY (organization_id, project_id, bucket_id, version_id) REFERENCES public.versions(organization_id, project_id, bucket_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY public.version_findings_summary
    ADD CONSTRAINT version_findings_summary_version_fkey FOREIGN KEY (organization_id, project_id, version_id) REFERENCES public.versions(organization_id, project_id, id) ON DELETE CASCADE;

ALTER TABLE public.build_findings_summary ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.version_findings_summary ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON public.build_findings_summary USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));

CREATE POLICY tenant_isolation ON public.version_findings_summary USING (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text))))) WITH CHECK (((organization_id = (NULLIF(current_setting('app.tenant_org'::text, true), ''::text))::uuid) AND (project_id = (NULLIF(current_setting('app.tenant_project'::text, true), ''::text))::uuid) AND ((NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text) IS NULL) OR (bucket_id = NULLIF(current_setting('app.tenant_bucket'::text, true), ''::text)))));
