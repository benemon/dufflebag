CREATE INDEX artifacts_external_identifier
    ON artifacts (organization_id, project_id, external_identifier);
