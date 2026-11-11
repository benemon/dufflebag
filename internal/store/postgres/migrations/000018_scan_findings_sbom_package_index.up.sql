CREATE INDEX scan_findings_sbom_package_index ON public.scan_findings USING btree (organization_id, project_id, sbom_id, package_name, package_version, purl);
