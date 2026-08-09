-- Recovery ceremony verifier (ADR-0024). The digest of the recovery secret
-- and the share threshold are written in the same transaction that claims the
-- instance; the shares themselves are returned once by /sys/init and never
-- stored. NULL means the instance was initialized before recovery existed, and
-- /sys/recovery refuses on it.
ALTER TABLE instance
    ADD COLUMN recovery_digest bytea,
    ADD COLUMN recovery_threshold integer
        CHECK (recovery_threshold IS NULL OR recovery_threshold >= 1);
