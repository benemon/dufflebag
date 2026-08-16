-- name: ListOrganizations :many
SELECT id, name, created_at
FROM organizations
ORDER BY created_at, id;

-- name: CreateOrganization :one
INSERT INTO organizations (id, name, created_at)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
RETURNING id, name, created_at;

-- name: GetOrganization :one
SELECT id, name, created_at
FROM organizations
WHERE id = $1;

-- name: DeleteOrganization :one
WITH target AS (
    -- Columns qualified: sqlc's analyser otherwise reads bare `id` as ambiguous
    -- between the CTE's projection and the table.
    SELECT organizations.id FROM organizations WHERE organizations.id = $1
),
deleted AS (
    DELETE FROM organizations
    WHERE id = $1
      AND NOT EXISTS (
          SELECT 1 FROM projects WHERE organization_id = $1
      )
    RETURNING id
)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM deleted) THEN 'deleted'
    WHEN EXISTS (SELECT 1 FROM target) THEN 'conflict'
    ELSE 'not_found'
END AS result;

-- name: ListProjects :many
SELECT id, organization_id, name, created_at
FROM projects
WHERE organization_id = $1
ORDER BY created_at, id;

-- name: CreateProject :one
INSERT INTO projects (id, organization_id, name, created_at)
SELECT $1, $2, $3, $4
FROM organizations
WHERE id = $2
ON CONFLICT DO NOTHING
RETURNING id, organization_id, name, created_at;

-- name: GetProject :one
SELECT id, organization_id, name, created_at
FROM projects
WHERE organization_id = $1 AND id = $2;

-- name: DeleteProject :one
WITH target AS (
    SELECT projects.id
    FROM projects
    WHERE projects.organization_id = $1 AND projects.id = $2
),
deleted AS (
    DELETE FROM projects
    WHERE organization_id = $1
      AND id = $2
      AND NOT EXISTS (
          SELECT 1
          FROM buckets
          WHERE organization_id = $1 AND project_id = $2
      )
    RETURNING id
)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM deleted) THEN 'deleted'
    WHEN EXISTS (SELECT 1 FROM target) THEN 'conflict'
    ELSE 'not_found'
END AS result;

-- name: ListPins :many
SELECT bucket_name, pinned_at, pinned_by
FROM pins
ORDER BY pinned_at, bucket_name;

-- name: InsertPin :exec
INSERT INTO pins (organization_id, project_id, bucket_name, pinned_at, pinned_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: GetPin :one
SELECT bucket_name, pinned_at, pinned_by
FROM pins
WHERE bucket_name = $1;

-- name: DeletePin :exec
DELETE FROM pins WHERE bucket_name = $1;

-- name: GetBagDropConfig :one
SELECT *
FROM bagdrop_configs;

-- name: UpsertBagDropConfig :one
INSERT INTO bagdrop_configs (
    organization_id, project_id, adapter, destination, sealed_secret, enabled,
    last_verification, last_verified_at, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (organization_id, project_id) DO UPDATE SET
    adapter = EXCLUDED.adapter,
    destination = EXCLUDED.destination,
    sealed_secret = EXCLUDED.sealed_secret,
    enabled = EXCLUDED.enabled,
    last_verification = EXCLUDED.last_verification,
    last_verified_at = EXCLUDED.last_verified_at,
    updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: DeleteBagDropConfig :execrows
DELETE FROM bagdrop_configs
WHERE NOT EXISTS (
    SELECT 1 FROM bagdrop_associations
    WHERE state = 'pending_removal' OR first_attempted_at IS NOT NULL
);

-- name: RecordBagDropVerification :one
UPDATE bagdrop_configs
SET last_verification = $1, last_verified_at = $2, updated_at = $2
RETURNING *;

-- name: EnableBagDrop :one
UPDATE bagdrop_configs
SET enabled = true, last_verification = $1, last_verified_at = $2, updated_at = $2
RETURNING *;

-- name: DisableBagDrop :one
UPDATE bagdrop_configs
SET enabled = false, updated_at = $1
RETURNING *;

-- name: ListBagDropAssociations :many
SELECT *
FROM bagdrop_associations
ORDER BY created_at, bucket_name;

-- name: UpsertBagDropAssociation :one
INSERT INTO bagdrop_associations (
    organization_id, project_id, bucket_name, state, first_attempted_at,
    last_attempt_at, last_synced_at, last_sync_error, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (organization_id, project_id, bucket_name) DO UPDATE SET
    state = EXCLUDED.state,
    first_attempted_at = COALESCE(
        bagdrop_associations.first_attempted_at, EXCLUDED.first_attempted_at
    ),
    last_synced_at = COALESCE(
        bagdrop_associations.last_synced_at, EXCLUDED.last_synced_at
    ),
    last_attempt_at = COALESCE(
        EXCLUDED.last_attempt_at, bagdrop_associations.last_attempt_at
    ),
    last_sync_error = EXCLUDED.last_sync_error,
    updated_at = CASE
        WHEN bagdrop_associations.state <> EXCLUDED.state THEN EXCLUDED.updated_at
        ELSE bagdrop_associations.updated_at
    END
RETURNING *;

-- name: MarkBagDropAssociationAttempt :one
UPDATE bagdrop_associations
SET first_attempted_at = COALESCE(first_attempted_at, $2),
    last_attempt_at = $2,
    updated_at = $2
WHERE bucket_name = $1
RETURNING *;

-- name: RecordBagDropAssociationSuccess :one
UPDATE bagdrop_associations
SET last_synced_at = $2,
    last_sync_error = NULL,
    updated_at = $2
WHERE bucket_name = $1 AND state = 'active'
RETURNING *;

-- name: RecordBagDropAssociationFailure :one
UPDATE bagdrop_associations
SET last_sync_error = $2,
    updated_at = $3
WHERE bucket_name = $1
RETURNING *;

-- name: BagDropBucketExists :one
SELECT EXISTS (SELECT 1 FROM buckets WHERE name = $1);

-- name: RemoveBagDropAssociation :one
WITH hard_deleted AS (
    DELETE FROM bagdrop_associations
    WHERE bagdrop_associations.bucket_name = $1
      AND bagdrop_associations.first_attempted_at IS NULL
    RETURNING bagdrop_associations.bucket_name
), pending AS (
    UPDATE bagdrop_associations
    SET state = 'pending_removal', updated_at = $2
    WHERE bagdrop_associations.bucket_name = $1
      AND bagdrop_associations.first_attempted_at IS NOT NULL
    RETURNING bagdrop_associations.bucket_name
)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM pending) THEN 'removal_pending'
    ELSE 'removed_clean'
END AS outcome;

-- name: DeleteBagDropAssociation :execrows
DELETE FROM bagdrop_associations
WHERE bucket_name = $1;

-- name: HasBlockingBagDropAssociations :one
SELECT EXISTS (
    SELECT 1 FROM bagdrop_associations
    WHERE state = 'pending_removal' OR first_attempted_at IS NOT NULL
);

-- name: CreateBucket :one
INSERT INTO buckets (
    organization_id, project_id, id, name, description, labels, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetBucketByName :one
SELECT * FROM buckets WHERE name = $1;

-- name: DeleteBucketByName :one
DELETE FROM buckets WHERE name = $1
RETURNING id;

-- name: UpdateBucket :one
UPDATE buckets
SET description = $2, labels = $3, updated_at = $4
WHERE name = $1
RETURNING *;

-- name: CreateVersion :one
INSERT INTO versions (
    organization_id, project_id, id, bucket_id, fingerprint, template_type,
    complete, sequence, created_at, updated_at, author_id
)
SELECT $1, $2, $3, buckets.id, $5, $6, $7, $8, $9, $10, $11
FROM buckets
WHERE buckets.name = $4
ON CONFLICT (organization_id, project_id, bucket_id, fingerprint)
DO UPDATE SET fingerprint = versions.fingerprint
RETURNING *;

-- name: GetVersionByFingerprint :one
SELECT versions.*, buckets.name AS bucket_name
FROM versions
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $1 AND versions.fingerprint = $2;

-- name: GetVersionByID :one
SELECT versions.*, buckets.name AS bucket_name
FROM versions
JOIN buckets ON buckets.id = versions.bucket_id
WHERE versions.organization_id = $1
  AND versions.project_id = $2
  AND versions.id = $3;

-- name: GetVersionRelationships :one
WITH parents AS (
    SELECT builds.parent_version_id,
           parent_versions.id AS existing_parent_id,
           current_assignment.version_id AS channel_version_id
    FROM builds
    LEFT JOIN versions AS parent_versions ON parent_versions.id = builds.parent_version_id
    LEFT JOIN channels AS parent_channels ON parent_channels.id = builds.parent_channel_id
    LEFT JOIN LATERAL (
        SELECT assignments.version_id
        FROM channel_assignments AS assignments
        WHERE assignments.channel_id = parent_channels.id
        ORDER BY assignments.assigned_at DESC, assignments.id DESC
        LIMIT 1
    ) AS current_assignment ON true
    WHERE builds.version_id = CAST(sqlc.arg(version_id) AS text)
      AND builds.parent_version_id IS NOT NULL
)
SELECT EXISTS (
           SELECT 1 FROM builds
           WHERE builds.parent_version_id = CAST(sqlc.arg(version_id) AS text)
       ) AS has_descendants,
       CASE
           WHEN NOT EXISTS (SELECT 1 FROM parents) THEN ''
           WHEN EXISTS (
               SELECT 1 FROM parents
               WHERE channel_version_id IS NOT NULL
                 AND channel_version_id <> parent_version_id
           ) THEN 'out_of_date'
           WHEN EXISTS (
               SELECT 1 FROM parents
               WHERE existing_parent_id IS NULL OR channel_version_id IS NULL
           ) THEN 'undetermined'
           ELSE 'up_to_date'
       END AS parents_status;

-- name: ListVersionRelationshipsByBucket :many
WITH bucket_versions AS (
    SELECT versions.id
    FROM versions
    JOIN buckets ON buckets.id = versions.bucket_id
    WHERE buckets.name = $1
), parents AS (
    SELECT builds.version_id AS child_version_id,
           builds.parent_version_id,
           parent_versions.id AS existing_parent_id,
           current_assignment.version_id AS channel_version_id
    FROM builds
    JOIN bucket_versions ON bucket_versions.id = builds.version_id
    LEFT JOIN versions AS parent_versions ON parent_versions.id = builds.parent_version_id
    LEFT JOIN channels AS parent_channels ON parent_channels.id = builds.parent_channel_id
    LEFT JOIN LATERAL (
        SELECT assignments.version_id
        FROM channel_assignments AS assignments
        WHERE assignments.channel_id = parent_channels.id
        ORDER BY assignments.assigned_at DESC, assignments.id DESC
        LIMIT 1
    ) AS current_assignment ON true
    WHERE builds.parent_version_id IS NOT NULL
)
SELECT bucket_versions.id AS version_id,
       EXISTS (
           SELECT 1 FROM builds
           WHERE builds.parent_version_id = bucket_versions.id
       ) AS has_descendants,
       CASE
           WHEN NOT EXISTS (
               SELECT 1 FROM parents
               WHERE parents.child_version_id = bucket_versions.id
           ) THEN ''
           WHEN EXISTS (
               SELECT 1 FROM parents
               WHERE parents.child_version_id = bucket_versions.id
                 AND parents.channel_version_id IS NOT NULL
                 AND parents.channel_version_id <> parents.parent_version_id
           ) THEN 'out_of_date'
           WHEN EXISTS (
               SELECT 1 FROM parents
               WHERE parents.child_version_id = bucket_versions.id
                 AND (parents.existing_parent_id IS NULL OR parents.channel_version_id IS NULL)
           ) THEN 'undetermined'
           ELSE 'up_to_date'
       END AS parents_status
FROM bucket_versions;

-- name: ListBuildsByVersion :many
SELECT builds.*
FROM builds
JOIN versions ON versions.id = builds.version_id
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $1 AND versions.fingerprint = $2
ORDER BY builds.id DESC;

-- name: ListBuildsByBucket :many
SELECT builds.*
FROM builds
JOIN versions ON versions.id = builds.version_id
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $1
ORDER BY builds.version_id, builds.id DESC;

-- name: GetBuild :one
SELECT builds.*
FROM builds
JOIN versions ON versions.id = builds.version_id
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $1 AND versions.fingerprint = $2 AND builds.id = $3;

-- name: CreateBuild :one
INSERT INTO builds (
    organization_id, project_id, id, version_id, component_type, status,
    platform, metadata_seen, packer_run_uuid, labels,
    source_external_identifier, parent_version_id, parent_channel_id, created_at, updated_at
)
SELECT $1, $2, $3, versions.id, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15
FROM versions
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $4 AND versions.fingerprint = $5
ON CONFLICT (organization_id, project_id, version_id, component_type)
DO UPDATE SET component_type = builds.component_type
RETURNING *;

-- name: CreateArtifact :one
INSERT INTO artifacts (
    organization_id, project_id, id, build_id, external_identifier, region, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (organization_id, project_id, build_id, external_identifier)
DO UPDATE SET external_identifier = EXCLUDED.external_identifier
RETURNING *;

-- name: ListArtifactsByBuild :many
SELECT * FROM artifacts WHERE build_id = $1 ORDER BY id DESC;

-- name: ListArtifactsByBucketBuilds :many
SELECT artifacts.*
FROM artifacts
JOIN builds ON builds.id = artifacts.build_id
JOIN versions ON versions.id = builds.version_id
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $1
ORDER BY artifacts.build_id, artifacts.id DESC;

-- name: UpdateBuild :one
UPDATE builds
SET status = $4,
    platform = $5,
    metadata_seen = $6,
    packer_run_uuid = $7,
    labels = $8,
    source_external_identifier = $9,
    parent_version_id = $10,
    parent_channel_id = $11,
    metadata = $12,
    updated_at = CASE
        WHEN ROW(status, platform, metadata_seen, packer_run_uuid, labels, source_external_identifier,
                 parent_version_id, parent_channel_id, metadata)
             IS DISTINCT FROM ROW($4, $5, $6, $7, $8, $9, $10, $11, $12)
        THEN $13
        ELSE updated_at
    END
WHERE id = $3 AND organization_id = $1 AND project_id = $2
RETURNING *;

-- name: UpsertSbom :one
INSERT INTO sboms (
    organization_id, project_id, id, build_id, name, format, object_key, created_at
)
SELECT $1, $2, $3, builds.id, $7, $8, $9, $10
FROM builds
JOIN versions ON versions.id = builds.version_id
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $4 AND versions.fingerprint = $5 AND builds.id = $6
ON CONFLICT (organization_id, project_id, build_id, name)
DO UPDATE SET format = EXCLUDED.format, object_key = EXCLUDED.object_key
RETURNING organization_id, project_id, id, build_id, name, format,
          object_key, created_at, parse_status, parse_error;

-- name: CompleteVersion :one
UPDATE versions
SET complete = true, sequence = $2, updated_at = $3
WHERE id = $1 AND complete = false
RETURNING *;

-- name: NextVersionSequence :one
SELECT coalesce(max(sequence), 0)::integer + 1
FROM versions
WHERE bucket_id = $1;

-- name: LockBucketForVersionSequence :one
SELECT id FROM buckets WHERE id = $1 FOR UPDATE;

-- name: RevokeVersion :one
UPDATE versions
SET revoke_at = $2, revocation_message = $3, revocation_author = $4,
    revocation_inherited_from_id = $5, revocation_inherited_from_bucket = $6,
    revocation_inherited_from_fingerprint = $7, revocation_inherited_from_name = $8,
    updated_at = $9
WHERE id = $1 AND revoke_at IS NULL
RETURNING *;

-- name: UnrevokeVersion :one
UPDATE versions
SET revoke_at = NULL, revocation_message = NULL, revocation_author = NULL,
    revocation_inherited_from_id = NULL, revocation_inherited_from_bucket = NULL,
    revocation_inherited_from_fingerprint = NULL, revocation_inherited_from_name = NULL,
    updated_at = $2
WHERE id = $1 AND revoke_at IS NOT NULL
RETURNING *;

-- name: UnrevokeInheritedVersion :one
UPDATE versions
SET revoke_at = NULL, revocation_message = NULL, revocation_author = NULL,
    revocation_inherited_from_id = NULL, revocation_inherited_from_bucket = NULL,
    revocation_inherited_from_fingerprint = NULL, revocation_inherited_from_name = NULL,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND revocation_inherited_from_id = CAST(sqlc.arg(restored_id) AS text)
RETURNING *;

-- name: ListVersionDescendants :many
WITH RECURSIVE descendants(id) AS (
    SELECT builds.version_id FROM builds
    WHERE builds.parent_version_id = CAST(sqlc.arg(version_id) AS text)
    UNION
    SELECT builds.version_id FROM builds
    JOIN descendants ON builds.parent_version_id = descendants.id
)
SELECT versions.*, buckets.name AS bucket_name
FROM versions
JOIN descendants ON descendants.id = versions.id
JOIN buckets ON buckets.id = versions.bucket_id
ORDER BY versions.id;

-- name: CreatePrincipal :one
INSERT INTO principals (
    id, name, client_id, organization_id, project_id, bucket_id, role, created_at, integrity_mac
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: CreatePrincipalSecret :one
INSERT INTO principal_secrets (
    id, principal_id, encoded_hash, created_at, last_used_at, expires_at, integrity_mac
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: TouchSecretLastUsed :exec
-- last_used_at is not part of principalSecretMACMessage, so this metadata-only
-- update does not require re-sealing the secret row.
UPDATE principal_secrets SET last_used_at = $2 WHERE id = $1;

-- name: GetPrincipalByClientID :one
SELECT *
FROM principals
WHERE client_id = $1;

-- name: ListPrincipalSecrets :many
SELECT *
FROM principal_secrets
WHERE principal_id = $1
ORDER BY created_at, id;

-- name: LockInitialization :exec
LOCK TABLE instance IN EXCLUSIVE MODE;

-- name: RecordInitialization :exec
INSERT INTO instance (id, initialized_at, recovery_digest, recovery_threshold)
VALUES (true, $1, $2, $3)
ON CONFLICT (id) DO NOTHING;

-- name: GetInitializationTimestamp :one
SELECT initialized_at
FROM instance
WHERE id = true;

-- name: GetRecoveryVerifier :one
SELECT recovery_digest, recovery_threshold
FROM instance
WHERE id = true;

-- name: SetVersionIntegrityMAC :exec
UPDATE versions SET integrity_mac = $2 WHERE id = $1;

-- name: SetBuildIntegrityMAC :exec
UPDATE builds SET integrity_mac = $2 WHERE id = $1;

-- name: SetArtifactIntegrityMAC :exec
UPDATE artifacts SET integrity_mac = $2 WHERE id = $1;

-- name: GetEncryptionMode :one
SELECT encrypted
FROM encryption_mode
WHERE id = true;

-- name: RecordEncryptionMode :execrows
INSERT INTO encryption_mode (id, encrypted, recorded_at)
VALUES (true, $1, $2)
ON CONFLICT (id) DO NOTHING;

-- name: ListKeyringEntries :many
SELECT purpose, version, wrapped_key, kek_ref, created_at
FROM keyring
ORDER BY purpose, version;

-- name: CreateKeyringEntry :execrows
INSERT INTO keyring (purpose, version, wrapped_key, kek_ref, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (purpose, version) DO NOTHING;

-- name: RewrapKeyringEntry :execrows
UPDATE keyring
SET wrapped_key = $3, kek_ref = $4, created_at = $5
WHERE purpose = $1 AND version = $2;

-- name: RootPrincipalExists :one
SELECT EXISTS (
    SELECT 1
    FROM principals
    WHERE role = 'root'
      AND organization_id IS NULL
      AND project_id IS NULL
) AS initialized;

-- name: ListVersionsByBucket :many
SELECT versions.*, buckets.name AS bucket_name
FROM versions
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $1
ORDER BY versions.sequence DESC, versions.created_at DESC;

-- name: GetPrincipalByID :one
SELECT *
FROM principals
WHERE id = $1;

-- name: GetPrincipalByIDForUpdate :one
SELECT *
FROM principals
WHERE id = $1
FOR UPDATE;

-- Principal listings select EXACTLY one scope, never a subtree: platform lists
-- the platform principals, an organization lists its organization-scoped
-- principals only, a project lists its own. See-where-you-stand and
-- create-where-you-stand are the same rule (duf-4qr).

-- name: ListPlatformPrincipals :many
SELECT *
FROM principals
WHERE organization_id IS NULL
ORDER BY created_at DESC, id DESC;

-- name: ListPrincipalsByOrganization :many
SELECT *
FROM principals
WHERE organization_id = $1 AND project_id IS NULL
ORDER BY created_at DESC, id DESC;

-- name: ListPrincipalsByProject :many
SELECT *
FROM principals
WHERE organization_id = $1 AND project_id = $2
ORDER BY created_at DESC, id DESC;

-- name: DeletePrincipal :execrows
DELETE FROM principals
WHERE id = $1;

-- name: DeletePrincipalSecret :execrows
DELETE FROM principal_secrets
WHERE principal_id = $1 AND id = $2;

-- name: CountRootPrincipals :one
SELECT count(*) FROM principals WHERE role = 'root';

-- name: LockRootPrincipalDeletion :exec
-- Serializes the root count-and-delete decision across transactions and server
-- instances. The fixed key is private to this database and this invariant.
SELECT pg_advisory_xact_lock(1646664799);

-- name: ListAuditTargets :many
SELECT *
FROM audit_targets
ORDER BY created_at DESC, id DESC;

-- name: CreateAuditTarget :one
-- Server-assigned slot: the lowest free value in 1..3, chosen inside the
-- caller's transaction under the slot lock. Returns no row in exactly two
-- cases, and the repository maps both to a conflict: all three slots are
-- taken, so the sub-select is empty; or the row collides on id or slot, which
-- ON CONFLICT DO NOTHING swallows rather than surfacing as a raw pg error.
INSERT INTO audit_targets (id, slot, path, created_at)
SELECT $1, free.slot, $2, $3
FROM (
    SELECT s.slot
    FROM generate_series(1, 3) AS s(slot)
    WHERE NOT EXISTS (SELECT 1 FROM audit_targets t WHERE t.slot = s.slot)
    ORDER BY s.slot
    LIMIT 1
) AS free
ON CONFLICT DO NOTHING
RETURNING *;

-- name: DeleteAuditTarget :execrows
DELETE FROM audit_targets
WHERE id = $1;

-- name: LockAuditTargetSlots :exec
-- Serializes lowest-free-slot selection so two concurrent creates cannot read
-- the same free slot and have one fail on UNIQUE. Held for the transaction,
-- which does nothing but this insert.
SELECT pg_advisory_xact_lock(1646664800);

-- name: CreateWebhook :one
INSERT INTO webhooks (
    organization_id, project_id, id, name, url, description, sealed_secret,
    events, state, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9, $9)
RETURNING *;

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE id = $1;

-- name: ListWebhooks :many
SELECT * FROM webhooks ORDER BY created_at DESC, id DESC;

-- name: UpdateWebhook :one
UPDATE webhooks
SET name = $2, url = $3, description = $4, sealed_secret = $5,
    events = $6, state = $7, last_verification_at = $8,
    last_verification_error = $9, updated_at = $10
WHERE id = $1
RETURNING *;

-- name: RecordWebhookVerification :one
UPDATE webhooks
SET state = $2, last_verification_at = $3,
    last_verification_error = $4, updated_at = $3
WHERE id = $1
RETURNING *;

-- name: DeleteWebhook :execrows
DELETE FROM webhooks WHERE id = $1;

-- name: EnqueueWebhookEvent :exec
WITH subscribed AS (
    SELECT id FROM webhooks
    WHERE state = 'active'
      AND (cardinality(events) = 0 OR sqlc.arg(operation)::text = ANY(events))
), pruned AS (
    DELETE FROM webhook_deliveries AS doomed
    WHERE doomed.webhook_id IN (SELECT id FROM subscribed) AND doomed.id IN (
        SELECT retained.id FROM webhook_deliveries AS retained
        WHERE retained.webhook_id = doomed.webhook_id
        ORDER BY retained.created_at DESC, retained.id DESC
        OFFSET 99
    )
), queued AS (
    INSERT INTO webhook_outbox (
        organization_id, project_id, event_id, occurred_at, operation,
        target, actor, payload, available_at
    )
    SELECT sqlc.arg(organization_id), sqlc.arg(project_id), sqlc.arg(event_id),
           sqlc.arg(occurred_at)::timestamptz, sqlc.arg(operation),
           sqlc.arg(target), sqlc.arg(actor), sqlc.arg(payload),
           sqlc.arg(occurred_at)::timestamptz
    WHERE EXISTS (SELECT 1 FROM subscribed)
    RETURNING event_id
)
INSERT INTO webhook_deliveries (
    organization_id, project_id, id, webhook_id, event_id, operation,
    status, attempt_count, next_attempt_at, created_at
)
SELECT sqlc.arg(organization_id), sqlc.arg(project_id), gen_random_uuid(),
       subscribed.id, queued.event_id, sqlc.arg(operation),
       'pending', 0, sqlc.arg(occurred_at)::timestamptz,
       sqlc.arg(occurred_at)::timestamptz
FROM subscribed CROSS JOIN queued;

-- name: GetNextWebhookOutboxEvent :one
SELECT * FROM webhook_outbox
WHERE available_at <= $1
ORDER BY available_at, event_id
LIMIT 1;

-- name: SetWebhookOutboxAvailableAt :exec
UPDATE webhook_outbox SET available_at = $2 WHERE event_id = $1;

-- name: DeleteWebhookOutboxEvent :execrows
DELETE FROM webhook_outbox WHERE event_id = $1;

-- name: CreateWebhookDelivery :one
INSERT INTO webhook_deliveries (
    organization_id, project_id, id, webhook_id, event_id, operation,
    status, attempt_count, next_attempt_at, created_at
) VALUES ($1, $2, $3, $4, $5, $6, 'pending', 0, $7, $7)
ON CONFLICT (organization_id, project_id, webhook_id, event_id) DO UPDATE
SET event_id = EXCLUDED.event_id
RETURNING *;

-- name: ListWebhookEventDeliveries :many
SELECT * FROM webhook_deliveries
WHERE event_id = $1
ORDER BY created_at, id;

-- name: RecordWebhookDeliveryAttempt :one
UPDATE webhook_deliveries
SET status = $2, attempt_count = $3,
    first_attempted_at = COALESCE(first_attempted_at, $4),
    last_attempted_at = $4, next_attempt_at = $5,
    response_code = $6, detail = $7
WHERE id = $1
RETURNING *;

-- name: ListWebhookDeliveries :many
SELECT * FROM webhook_deliveries
WHERE webhook_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 100;

-- name: PruneWebhookDeliveries :exec
DELETE FROM webhook_deliveries AS doomed
WHERE doomed.webhook_id = $1 AND doomed.id IN (
    SELECT retained.id FROM webhook_deliveries AS retained
    WHERE retained.webhook_id = $1
    ORDER BY retained.created_at DESC, retained.id DESC
    OFFSET 100
);
