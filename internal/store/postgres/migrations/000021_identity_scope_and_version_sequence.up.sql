-- Sequence uniqueness deliberately NOT added here: migration 000001 already
-- carries versions_bucket_sequence on (organization_id, project_id, bucket_id,
-- sequence). A bare (bucket_id, sequence) index was proposed and rejected in
-- review — buckets.id is only unique per tenant (composite primary key), so a
-- cross-tenant index would let one tenant's rows refuse another's insert.

ALTER TABLE principals ADD CONSTRAINT principals_organization_id_fkey
    FOREIGN KEY (organization_id)
    REFERENCES organizations (id) ON DELETE RESTRICT;

ALTER TABLE principals ADD CONSTRAINT principals_project_scope_fkey
    FOREIGN KEY (organization_id, project_id)
    REFERENCES projects (organization_id, id) ON DELETE RESTRICT;
