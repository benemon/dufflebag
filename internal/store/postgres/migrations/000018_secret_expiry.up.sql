-- A secret may carry an expiry (duf-2rw). NULL means it never expires; an
-- expired row stays stored so the failure is diagnosable ('expired on the
-- 4th', not 'authentication failed') — deletion is what revocation means.
ALTER TABLE principal_secrets ADD COLUMN expires_at timestamptz;
