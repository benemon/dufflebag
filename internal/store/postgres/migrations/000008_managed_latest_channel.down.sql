-- Reverses the schema expansion only. Channels created by the up migration's
-- backfill remain, as ordinary unmanaged channels: deleting them here would
-- destroy assignment history written since, and rows outliving what created
-- them is the house pattern (see channel_assignments in migration 000001).
ALTER TABLE channels DROP COLUMN managed;
