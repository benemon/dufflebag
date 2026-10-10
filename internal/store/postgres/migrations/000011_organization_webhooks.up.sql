CREATE TABLE organization_webhooks (
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    id uuid NOT NULL,
    name text NOT NULL,
    url text NOT NULL,
    description text NOT NULL DEFAULT '',
    sealed_secret bytea,
    events text[] NOT NULL DEFAULT '{}',
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active')),
    last_verification_at timestamptz,
    last_verification_error text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, id)
);

CREATE TABLE organization_webhook_outbox (
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    event_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    operation text NOT NULL,
    target jsonb NOT NULL,
    actor jsonb NOT NULL,
    payload jsonb NOT NULL,
    available_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, event_id)
);

CREATE INDEX organization_webhook_outbox_available_idx
    ON organization_webhook_outbox (available_at, organization_id, event_id);

CREATE TABLE organization_webhook_deliveries (
    organization_id uuid NOT NULL,
    id uuid NOT NULL,
    webhook_id uuid NOT NULL,
    event_id text NOT NULL,
    operation text NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'retrying', 'delivered', 'failed', 'refused')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= 5),
    first_attempted_at timestamptz,
    last_attempted_at timestamptz,
    next_attempt_at timestamptz,
    response_code integer,
    detail text,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, webhook_id, event_id),
    FOREIGN KEY (organization_id, webhook_id) REFERENCES organization_webhooks (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX organization_webhook_deliveries_retry_idx
    ON organization_webhook_deliveries (organization_id, next_attempt_at)
    WHERE status IN ('pending', 'retrying');
CREATE INDEX organization_webhook_deliveries_ring_idx
    ON organization_webhook_deliveries (organization_id, webhook_id, created_at DESC, id DESC);

ALTER TABLE organization_webhooks ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_webhooks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON organization_webhooks
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);

ALTER TABLE organization_webhook_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_webhook_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON organization_webhook_outbox
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);

ALTER TABLE organization_webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_webhook_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON organization_webhook_deliveries
    USING (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid);
