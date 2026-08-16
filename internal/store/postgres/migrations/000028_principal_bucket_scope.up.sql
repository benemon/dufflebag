-- A bucket binding is narrower than a project binding, never an alternative to
-- one. The project claim remains the authorization scope until bucket-aware
-- enforcement is added in a later phase.
ALTER TABLE principals ADD COLUMN bucket_id text NULL;

-- A bucket without a project is malformed rather than narrow, and would
-- otherwise leave the bucket's organization and project impossible to prove.
ALTER TABLE principals ADD CONSTRAINT principals_bucket_scope_is_coherent CHECK (
    bucket_id IS NULL OR project_id IS NOT NULL
);

-- The composite key proves both that the bucket exists and that it belongs to
-- the principal's organization and project. RESTRICT prevents deletion from
-- silently widening or orphaning the principal.
ALTER TABLE principals ADD CONSTRAINT principals_bucket_scope_fkey
    FOREIGN KEY (organization_id, project_id, bucket_id)
    REFERENCES buckets (organization_id, project_id, id) ON DELETE RESTRICT;
