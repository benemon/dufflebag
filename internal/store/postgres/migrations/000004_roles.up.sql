-- A principal's role: what it may do within the scope it holds (ADR-0019).
--
-- No default. A role is part of what a principal IS, and defaulting it would
-- let a caller that forgot to set one create a principal with authority nobody
-- chose. Least privilege is applied where principals are created, not here.
ALTER TABLE principals ADD COLUMN role text NOT NULL;

-- organization_id becomes nullable, because `root` is held at PLATFORM scope —
-- above every tenancy rather than inside one.
ALTER TABLE principals ALTER COLUMN organization_id DROP NOT NULL;

-- The two shapes that are not scopes at all.
--
-- A project without an organization is malformed rather than narrow, and would
-- otherwise pass every other check by being neither platform nor tenancy
-- scoped. And only `root` may sit at platform scope: a platform-scoped builder
-- would be entitled to no tenancy and able to do nothing, which is a
-- misconfiguration rather than a safe default.
ALTER TABLE principals ADD CONSTRAINT principals_scope_is_coherent CHECK (
    (organization_id IS NOT NULL AND role <> 'root')
    OR (organization_id IS NULL AND project_id IS NULL AND role = 'root')
);
