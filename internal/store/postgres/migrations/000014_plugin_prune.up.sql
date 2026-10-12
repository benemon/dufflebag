ALTER TABLE plugin_registries
    ADD COLUMN prune_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN prune_age_days integer NOT NULL DEFAULT 30,
    ADD COLUMN prune_schedule text NOT NULL DEFAULT '0 3 * * *',
    ADD COLUMN prune_next_at timestamptz NULL,
    ADD COLUMN prune_last_run_at timestamptz NULL,
    ADD COLUMN prune_last_cleared integer NOT NULL DEFAULT 0,
    ADD COLUMN prune_last_error text NOT NULL DEFAULT '';
