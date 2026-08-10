ALTER TABLE bagdrop_associations
    ADD COLUMN last_attempt_at timestamptz NULL,
    ADD COLUMN last_sync_error text NULL;
