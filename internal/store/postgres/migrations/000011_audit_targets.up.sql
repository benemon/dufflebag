-- Audit targets: the files the audit broker writes every request and response
-- to (duf-dya). Platform-scoped operational configuration, not tenant data.
--
-- NO RLS POLICY, DELIBERATELY. Every other table carrying tenant-visible rows
-- has one, so its absence here has to be stated rather than inferred: audit
-- targets belong to the instance, are readable and writable only by root
-- through the platform API, and have no organization or project column to
-- filter on. A policy would have nothing to express.
--
-- MAX THREE IS STRUCTURAL, NOT CHECKED IN CODE. `slot` is a storage detail the
-- API never exposes: the server assigns the lowest free value, and THREE
-- cooperating constraints make a fourth target unwriteable — including by a
-- direct INSERT or a concurrent one that a read-then-count guard in Go would
-- miss. A bug that cannot be written beats a bug that is caught.
--
-- All three are load-bearing, and NOT NULL is the one that reads as redundant
-- and is not: with it removed, a NULL slot makes the CHECK evaluate to UNKNOWN,
-- which Postgres accepts, and ordinary NULLs do not collide under UNIQUE — so
-- unlimited targets become writeable while both visible constraints remain in
-- place. The test inserts a NULL slot first for exactly this reason.
CREATE TABLE audit_targets (
    id         uuid PRIMARY KEY,
    slot       smallint NOT NULL UNIQUE CHECK (slot BETWEEN 1 AND 3),
    path       text NOT NULL,
    created_at timestamptz NOT NULL
);
