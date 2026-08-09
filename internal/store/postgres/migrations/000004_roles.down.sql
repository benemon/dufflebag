ALTER TABLE principals DROP CONSTRAINT principals_scope_is_coherent;
ALTER TABLE principals ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE principals DROP COLUMN role;
