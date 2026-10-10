ALTER TABLE plugins
    DROP CONSTRAINT plugins_update_check_source,
    DROP COLUMN update_latest_tag,
    DROP COLUMN update_latest,
    DROP COLUMN update_error,
    DROP COLUMN update_checked_at,
    DROP COLUMN update_check;
