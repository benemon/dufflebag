ALTER TABLE plugin_registries
    DROP COLUMN prune_enabled,
    DROP COLUMN prune_age_days,
    DROP COLUMN prune_schedule,
    DROP COLUMN prune_next_at,
    DROP COLUMN prune_last_run_at,
    DROP COLUMN prune_last_cleared,
    DROP COLUMN prune_last_error;
