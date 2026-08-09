-- DeleteBucket "deletes the bucket and all its information, such as versions,
-- builds and artifacts" (vendored spec, PackerService_DeleteBucket). Deleting a
-- bucket cascades through versions to channel_assignments, and the cascade
-- fires the append-only trigger row by row — so bucket deletion was impossible
-- while the trigger rejected every DELETE.
--
-- The invariant narrows rather than disappears: a row whose version still
-- exists is live history and stays immutable; a row whose version is gone is
-- being taken by the cascade that removed the version, which is the one
-- legitimate way these rows die (the FK has said so since migration 000001).
-- Unassignment markers (version_id IS NULL) remain immutable — like history
-- for a deleted channel, they outlive what they describe.
CREATE OR REPLACE FUNCTION reject_channel_assignment_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND OLD.version_id IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM versions
           WHERE organization_id = OLD.organization_id
             AND project_id = OLD.project_id
             AND id = OLD.version_id
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'channel assignment history is append-only';
END
$$;
