CREATE TABLE webhooks (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id uuid NOT NULL,
    name text NOT NULL,
    url text NOT NULL,
    description text NOT NULL DEFAULT '',
    sealed_secret bytea NULL,
    events text[] NOT NULL DEFAULT '{}',
    state text NOT NULL DEFAULT 'pending',
    last_verification_at timestamptz NULL,
    last_verification_error text NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id) ON DELETE CASCADE,
    CHECK (state IN ('pending', 'active'))
);

CREATE TABLE webhook_outbox (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    event_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    operation text NOT NULL,
    target jsonb NOT NULL,
    actor jsonb NOT NULL,
    payload jsonb NOT NULL,
    available_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, event_id),
    FOREIGN KEY (organization_id, project_id)
        REFERENCES projects (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX webhook_outbox_available_idx
    ON webhook_outbox (available_at, organization_id, project_id, event_id);

CREATE TABLE webhook_deliveries (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id uuid NOT NULL,
    webhook_id uuid NOT NULL,
    event_id text NOT NULL,
    operation text NOT NULL,
    status text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    first_attempted_at timestamptz NULL,
    last_attempted_at timestamptz NULL,
    next_attempt_at timestamptz NULL,
    response_code integer NULL,
    detail text NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, project_id, id),
    UNIQUE (organization_id, project_id, webhook_id, event_id),
    FOREIGN KEY (organization_id, project_id, webhook_id)
        REFERENCES webhooks (organization_id, project_id, id) ON DELETE CASCADE,
    CHECK (status IN ('pending', 'retrying', 'delivered', 'failed', 'refused')),
    CHECK (attempt_count >= 0 AND attempt_count <= 5)
);

CREATE INDEX webhook_deliveries_retry_idx
    ON webhook_deliveries (organization_id, project_id, next_attempt_at)
    WHERE status IN ('pending', 'retrying');
CREATE INDEX webhook_deliveries_ring_idx
    ON webhook_deliveries (organization_id, project_id, webhook_id, created_at DESC, id DESC);

ALTER TABLE webhooks ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhooks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhooks USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);

ALTER TABLE webhook_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_outbox USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);

ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_deliveries USING (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
) WITH CHECK (
    organization_id = NULLIF(current_setting('app.tenant_org', true), '')::uuid
    AND project_id = NULLIF(current_setting('app.tenant_project', true), '')::uuid
);
