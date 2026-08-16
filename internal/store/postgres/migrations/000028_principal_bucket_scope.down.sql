ALTER TABLE principals DROP CONSTRAINT principals_bucket_scope_fkey;
ALTER TABLE principals DROP CONSTRAINT principals_bucket_scope_is_coherent;
ALTER TABLE principals DROP COLUMN bucket_id;
