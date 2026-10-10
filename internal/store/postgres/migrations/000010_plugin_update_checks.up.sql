ALTER TABLE plugins
    ADD COLUMN update_check boolean NOT NULL DEFAULT false,
    ADD COLUMN update_checked_at timestamptz NULL,
    ADD COLUMN update_error text NULL,
    ADD COLUMN update_latest text NULL,
    ADD COLUMN update_latest_tag text NULL,
    ADD CONSTRAINT plugins_update_check_source CHECK (NOT update_check OR source_kind <> 'upload');
