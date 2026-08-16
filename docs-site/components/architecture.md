# Architecture

How dufflebag is put together, and the boundaries that shape everything it
serves. The [compatibility reference](../reference/compatibility.md) governs
everything an external client can observe; this page covers everything else.

## Two API planes

One domain, two very different contracts:

- **The compatibility plane** serves the externally owned contracts — the
  OAuth token endpoint, the Packer registry API (`/packer/2023-01-01/...`),
  and the resource-manager API. It is **frozen**: behaviour matches what the
  stock clients observe from the real service, accidents included, and its
  wire models are generated from the vendored HCP API specifications. If a
  behaviour here looks odd, the
  [compatibility reference](../reference/compatibility.md) almost certainly
  records it — the strangeness is usually the contract.
- **The platform plane** (`/api/v1/...`, plus the unversioned `/sys/*`
  surfaces — init, recovery, health, session) is dufflebag's own contract for
  tenancy, identity, audit, console sessions, encryption control, scanning,
  bucket pins and Bag Drop. It is free to evolve, and its
  [API reference](/platform-api.html) is generated from its own OpenAPI
  specification.

If a capability exists in the compatibility plane, every client uses it —
including dufflebag's own console. The platform plane never grows a nicer
variant of something the compatibility surface already does, because a
compatibility surface nothing exercises is a compatibility surface that rots.

Wire models never become domain models: the generated compatibility shapes
stay at the edge, and the domain speaks its own types. The wire's `v0`
incomplete-version sentinel, for example, is rendered at the boundary — the
domain records completion as what it is.

## Tenancy is the security boundary

A tenant is `(organization_id, project_id)`. Isolation is enforced by
PostgreSQL **row-level security**, not application WHERE clauses: tenant
tables force RLS, every transaction is scoped to its tenant, and the serving
role must not be a superuser or hold `BYPASSRLS` — the server refuses to
start otherwise, because either would switch the tenancy boundary off without
an error. The test suite includes a sabotage gate that proves the isolation
tests fail when RLS is disabled — evidence that the alarm actually rings.

**Deny by default at every boundary.** An unauthenticated caller, an
unauthorized one, a malformed identifier, an absent scope and an
indeterminate authorization result all produce the same outcome: nothing.
Scope denial and absence answer identical 404s on the compatibility plane,
because existence is a disclosure; once entitlement to the tenant is
established, an insufficient role answers 403, because there is no existence
left to conceal. Tenancy is checked before role. RLS proves isolation
*between* scopes; entitlement *to* a scope is the authorization layer's job —
the two are independent and both are tested.

## Identity

The token is the contract, not the credential. Service principals
authenticate with client credentials over HTTP Basic — what the HCP SDK
sends — and receive a signed token whose claims carry identity and tenancy
scope. Roles — `reader < builder < publisher < maintainer < root` — are
resolved from the database per request, never baked into the token, so
revocation and role changes take effect on the next request. Secrets are
argon2id-hashed, with two active secrets per principal so rotation never
needs an authentication gap.

First run is one unauthenticated `POST /sys/init`, one-shot and atomic,
returning the root principal's credentials — and the recovery shares that
back the recovery ceremony — exactly once. The console wizard is an ordinary
client of the same endpoint; there is no privileged side door. Its
unauthenticated sibling `POST /sys/recovery` accepts a threshold of those
shares and mints a fresh root; every attempt, including refusals, is audited.

## Audit fails closed

Every API request is audited as a request/response pair. UI asset serving and
the health probe are the declared exemptions; admission refusals on the
anonymous surfaces (`/oauth2/token`, `/sys/recovery`) — throttling and the
verification-memory budget — are also decided outside the audit seam, so a
rejected admission attempt is not an audit entry. When audit is enabled and
no healthy sink remains to record an entry, the request fails — an instance
that cannot record a request does not serve it; while at least one configured
target still accepts writes, the request proceeds and the failing sink is
surfaced through audit health. Sensitive values enter the trail only as
HMACs, so correlation survives without the trail holding a usable credential.

## Encryption at rest is a first-boot decision

When a key provider is configured before the first boot, the instance is
stamped encrypted for life — a one-way door in both directions. A keyring of
four independently rotated purpose keys (payload, integrity, token signing,
audit HMAC) lives in PostgreSQL wrapped by the external key service; payloads
and SBOM objects are sealed with AES-GCM under tenant-scoped additional
authenticated data, and identity and provenance rows carry per-row MACs, so a
row inserted around the application fails verification — database write
access is not administration. On encrypted deployments the token-signing and
audit-HMAC keys move out of the environment into the keyring and their
variables are refused. An unreachable key service seals the process at start;
at runtime a heartbeat performs a real unwrap and reports `degraded` rather
than failing the readiness probe. [Encryption](./encryption.md) covers
configuration, rotation and recovery.

## The scanner pipeline

Dufflebag never scans in-process and never owns or mirrors a vulnerability
feed. A configured adapter queries an external service with versioned package
identities derived from the stored SBOM projection — package name, ecosystem
and version leave the deployment; SBOM documents and raw reports do not. Scan
work is queued per tenant and drained under the same row-level security as
everything else, with database-level work claiming so replicas coordinate
through the queue without a leader. Findings attach to the client-reported
package identity, preserve the provider's severity data verbatim, and derive
a display severity; reads follow the last *successful* run, so a failed newer
attempt never erases findings. Scanner activity is audited, and the audit
circuit outranks provider faults. The compatibility plane projects stored
findings into the frozen vulnerability shapes, with coverage and provenance
carried in `Dufflebag-Scan-*` response headers because the frozen JSON has no
fields for them. [Vulnerability scanning](./vulnerability-scanning.md) covers
the operator contract.

## Bag Drop: the outbound mirror

Bag Drop pushes a project's registry data to a destination registry — real
HCP Packer, or another dufflebag instance. It is one-way: the local registry
is the source of truth for everything it mirrors, and nothing observed at the
destination is ever written back. Configuration is a platform-plane resource
family per project: a destination is configured and enabled in one verified
step, and each bucket that mirrors is an explicit association. Verify and
enable share one resolution check — a token grant plus one scoped read,
composed entirely of calls the destination already serves as ordinary client
traffic; no destination needs to know it is being tested.

The engine is a level-based reconciler, not a queue. On a cadence — or
immediately, when a mutation enqueues a pass — it snapshots the project's
completed local state in one transaction, with row integrity MACs verified
before anything leaves so tampered provenance cannot propagate, observes the
destination, and converges the difference with idempotent upserts in
dependency order: bucket, versions, builds with artifacts and metadata, SBOMs
during the build's mirrored running window, then ordinary channels. Nothing
about the work is persisted; a crashed pass loses nothing because the next
pass re-derives the remaining difference. Reconciliation runs entirely
outside the serving paths — a Packer build succeeds with the destination
down — and per-project failures back off without blocking other projects.

Three authority boundaries hold everywhere. The association set is the
reconciler's **entire** authority to delete: inside an associated bucket the
source is authoritative in both directions — remote drift is removed, local
deletions propagate, and un-associating deletes the destination copy — while
nothing outside an associated bucket is ever touched. The managed `latest`
channel is never referenced on either side; completion drives it, as the live
service's own behaviour dictates. And channel assignments mirror as
*pointers*, never replayed history: each registry's assignment history is its
own provenance record, and the sequence of what dufflebag pushed lives in its
audit trail — every remote mutation emits one audit event, fail-closed before
the mutation executes, pausing sync (never serving) when audit cannot be
written.

The destination adapter seam carries connection mechanics only, never
semantics — the mirror behaves identically whatever the destination. The HCP
adapter has fixed endpoints; the dufflebag adapter takes an HTTPS endpoint
and optional CA chain per configuration, and both speak the same contract the
compatibility plane serves. The destination credential is the one secret
dufflebag must present rather than verify: sealed in an AES-256-GCM envelope
(the keyring on encrypted deployments, an environment key otherwise),
write-only at the API, HMAC'd in audit.

What deliberately does not mirror: destination-assigned IDs, timestamps and
authorship (correlation keys are bucket names and version fingerprints —
destination version *names* may diverge when local history has gaps), channel
assignment history, bucket IAM, and SBOMs appearing after a destination build
completed (the upload window has closed; they surface as permanent drift).
[Bag Drop](../administration/bag-drop.md) covers the day-to-day surface —
egress, cadence, credential keys and their honest limits.

## Deployment shape

One stateless container: a single binary with the console embedded, serving
every surface on one listener — the SDK assigns `/oauth2/token` onto its auth
URL, and the path trees do not collide, so one hostname serves both.
PostgreSQL holds all state. SBOM bytes and scan transcripts live in optional,
deployment-configured S3-compatible object storage, keyed but not secured by
tenant prefixes — on unencrypted deployments row-level security on the
locator rows is the control, while encrypted deployments additionally seal
each object before upload. Two further external dependencies are optional and
deployment-shaped: a key service (Vault transit) when encryption at rest was
chosen at first boot, and an OSV-compatible scanner endpoint when scanning is
configured. Migrations are embedded in the binary and apply at startup; the
hardened two-role deployment runs them via the `migrate` subcommand under a
privileged role instead, keeping schema changes away from the serving role.
Migrations are written expand-only, so a schema version ahead of the binary
never breaks it. See [Installation](../quick-start/installation.md) and
[Database](./database.md).

## The console

An ordinary client with no backend-for-frontend. Reads come from the same
compatibility plane every other client uses; the platform plane serves only
what has no compatibility-plane equivalent — tenancy, principals, audit,
sessions, instance metadata, encryption control, bucket pins. The console
serves registry writes where HCP's own console offers the capability —
channel management, promotion, version revocation and restore, bucket
deletion — always through the same compatibility endpoints and role rules as
any other client, while Terraform remains the recommended interface for
automated registry management. The console states what it knows honestly:
loading, error and absence are distinct rendered states, and it never invents
a value the server did not supply. [The console](./console.md) covers
sign-in, role gates and confirmations.
