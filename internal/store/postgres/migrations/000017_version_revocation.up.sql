-- Version revocation (expand-only, nullable): revoke_at is the effect time —
-- readers derive whether it has passed, no job flips state. The inherited-from
-- identity is denormalized at revocation time (bucket and version names are
-- fixed by then) so rendering stays join-free.
ALTER TABLE versions
    ADD COLUMN revoke_at timestamptz,
    ADD COLUMN revocation_message text,
    ADD COLUMN revocation_author text,
    ADD COLUMN revocation_inherited_from_id text,
    ADD COLUMN revocation_inherited_from_bucket text,
    ADD COLUMN revocation_inherited_from_fingerprint text,
    ADD COLUMN revocation_inherited_from_name text,
    -- Revocation state travels together: an effect time needs an author, and
    -- nothing else may exist without the effect time.
    ADD CONSTRAINT versions_revocation_shape CHECK (
        (revoke_at IS NOT NULL AND revocation_author IS NOT NULL)
        OR (revoke_at IS NULL AND revocation_message IS NULL
            AND revocation_author IS NULL AND revocation_inherited_from_id IS NULL)
    ),
    -- The ancestor identity is all-or-nothing.
    ADD CONSTRAINT versions_revocation_ancestor_shape CHECK (
        (revocation_inherited_from_id IS NULL) = (revocation_inherited_from_bucket IS NULL)
        AND (revocation_inherited_from_id IS NULL) = (revocation_inherited_from_fingerprint IS NULL)
        AND (revocation_inherited_from_id IS NULL) = (revocation_inherited_from_name IS NULL)
    );
