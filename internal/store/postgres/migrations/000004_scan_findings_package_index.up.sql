CREATE INDEX scan_findings_package_index ON public.scan_findings USING btree (organization_id, project_id, run_id, package_name, package_version, purl);
