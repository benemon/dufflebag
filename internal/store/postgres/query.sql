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

-- name: ListBuildsByVersion :many
SELECT builds.*
FROM builds
JOIN versions ON versions.id = builds.version_id
JOIN buckets ON buckets.id = versions.bucket_id
WHERE buckets.name = $1 AND versions.fingerprint = $2
ORDER BY builds.id DESC;

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
    id, name, client_id, organization_id, project_id, role, created_at, integrity_mac
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
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
