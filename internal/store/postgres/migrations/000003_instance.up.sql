-- A singleton table used to serialize initialization attempts.
--
-- Initialization is inferred from whether a platform root principal exists.
-- Root is the durable one-way-door predicate: the API refuses to delete the
-- last one. The table itself is locked while that predicate is checked and the
-- first root is written, so concurrent callers cannot both observe no root.
--
-- initialized_at records the successful one-shot claim when the application
-- knows it. An absent row means an older claim did not record a time.
CREATE TABLE instance (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    initialized_at timestamptz NOT NULL
);
