ALTER TABLE principal_secrets DROP COLUMN integrity_mac;
ALTER TABLE principals DROP COLUMN integrity_mac;
ALTER TABLE channel_assignments DROP COLUMN integrity_mac;
ALTER TABLE artifacts DROP COLUMN integrity_mac;
ALTER TABLE builds DROP COLUMN integrity_mac;
ALTER TABLE versions DROP COLUMN integrity_mac;

ALTER TABLE builds ALTER COLUMN metadata DROP DEFAULT;
ALTER TABLE builds ALTER COLUMN metadata TYPE jsonb USING convert_from(metadata, 'UTF8')::jsonb;
ALTER TABLE builds ALTER COLUMN metadata SET DEFAULT '{}';

DROP TABLE keyring;
DROP TABLE encryption_mode;
