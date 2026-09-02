# Architecture

The dufflebag deployment is a single stateless server in front of PostgreSQL, serving two
API surfaces and an embedded console. This page describes the system's
structure and the boundaries between its parts. The
[compatibility reference](../reference/compatibility.md) records everything
an external client can observe. This page covers everything else.

![dufflebag architecture: clients, the single container's serving and background surfaces, state, and optional external services](/diagrams/architecture.svg)

Solid connections are required. Dashed components and connections are
optional, chosen by the deployment.

## API planes

The dufflebag server serves one domain through two contracts.

The **compatibility plane** serves the externally owned surfaces: the OAuth
token endpoint, the Packer registry API (`/packer/2023-01-01/...`), and the
resource-manager API. This plane is frozen. Behaviour matches what the stock
clients observe from the real service, including its accidents, and its wire
models are generated from the vendored HCP API specifications. Unexpected
behaviour on this plane is usually the contract itself. The
[compatibility reference](../reference/compatibility.md) records each case.

The **platform plane** (`/api/v1/...`, with the unversioned `/sys/*`
surfaces for initialization, recovery, health and sessions) is dufflebag's
own contract. It serves tenancy, identity, audit, console sessions,
encryption control, scanning, bucket pins and Bag Drop, and is free to
evolve. Its [API reference](/platform-api.html) is generated from its
OpenAPI specification.

A capability that exists on the compatibility plane is served only there,
and every client uses it there - the console included. The platform plane
does not duplicate compatibility-plane capabilities, so the compatibility
surface remains exercised by all traffic rather than left untested.

Generated wire models stay at the edge. The domain records its own types.
The wire's `v0` incomplete-version sentinel, for example, is rendered at the
boundary rather than stored.

## Tenancy

A tenant is one `(organization_id, project_id)` pair. Isolation is enforced
in PostgreSQL with row-level security rather than application WHERE clauses.
Tenant tables force RLS, every transaction is scoped to its tenant, and the
serving role must not be a superuser or hold `BYPASSRLS` - the server
refuses to start otherwise, since either privilege disables the tenancy
boundary without an error. The test suite includes a sabotage gate proving
the isolation tests fail when RLS is disabled.

## Authorization

Every boundary denies by default. An unauthenticated caller, an unauthorized
caller, a malformed identifier, an absent scope and an indeterminate
authorization result all receive the same outcome: nothing.

Tenancy is checked before role. On the compatibility plane, scope denial and
absence answer identical 404s, because revealing that a resource exists is
itself a disclosure. Once entitlement to the tenant is established, an
insufficient role answers 403. Row-level security proves isolation between
scopes. Entitlement to a scope is decided by the authorization layer. The
two controls are independent and both are tested.

## Identity

Every client authenticates as a service principal with client credentials
over HTTP Basic, the form the HCP SDK sends, and receives a signed token
carrying identity and tenancy scope. Roles (`reader < builder < publisher <
maintainer < root`) are resolved from the database on every request rather
than embedded in the token, so revocation and role changes take effect on
the next request. Secrets are argon2id-hashed, and a principal may hold two
active secrets so rotation needs no authentication gap.

First run is a single unauthenticated `POST /sys/init` request, usable
exactly once, returning the root principal's credentials and the recovery
shares exactly once. The console's first-run wizard calls the same endpoint.
No privileged side door exists. `POST /sys/recovery` accepts a threshold of
recovery shares and mints a fresh root principal. Every attempt, including
refusals, is audited. [Principals](../administration/principals.md) covers
the role ladder, scopes and secret lifecycle.

## Audit

Every API request is audited as a request/response pair. UI asset serving
and the health probe are the declared exemptions. Admission refusals on the
anonymous surfaces (`/oauth2/token`, `/sys/recovery`) are decided before the
audit seam, so a throttled attempt is not an audit entry.

Audit fails closed. When audit is enabled and no healthy sink remains, the
request fails. An instance that cannot record a request does not serve it.
While at least one configured target accepts writes, requests proceed and
the failing sink is reported through audit health. Sensitive values enter
the trail only as HMACs, so entries can be correlated without the trail
holding a usable credential. [Audit](../administration/audit.md) covers
targets and the trail.

## Encryption at rest

Encryption at rest is decided at first boot and permanent for the instance's
lifetime, in both directions. A keyring of four independently rotated
purpose keys (payload, integrity, token signing, audit HMAC) is stored in
PostgreSQL, wrapped by the external key service. Payloads and SBOM objects
are sealed with AES-GCM under tenant-scoped additional authenticated data.
Identity and provenance rows carry per-row MACs, so a row inserted around
the application fails verification. Database write access is not
administration.

On encrypted deployments the token-signing and audit-HMAC keys live in the
keyring and their environment variables are refused. An unreachable key
service prevents startup. A running instance heartbeats the key service and
reports `degraded` on failure rather than failing its readiness probe.
[Encryption](./encryption.md) covers configuration, rotation and recovery.

## Vulnerability scanning

The dufflebag server does not scan in-process and does not mirror a vulnerability feed.
A configured adapter queries an external service with versioned package
identities derived from the stored SBOM projection. Package names,
ecosystems and versions leave the deployment. SBOM documents and raw reports
do not.

Scan work is queued per tenant and claimed at the database, so replicas
coordinate without a leader. Findings attach to the client-reported package
identity and preserve the provider's severity data verbatim. Reads follow
the most recent successful run, so a failed newer attempt does not erase
findings. The compatibility plane projects stored findings into the frozen
vulnerability shapes, with coverage and provenance in `Dufflebag-Scan-*`
response headers, since the frozen JSON has no fields for them.
[Vulnerability scanning](./vulnerability-scanning.md) covers the operator
contract.

## Bag Drop

Bag Drop mirrors a project's registry data to a destination registry: real
HCP Packer, or another dufflebag instance. The mirror is one-way. The local
registry is the source of truth for everything it mirrors, and nothing
observed at the destination is written back.

A destination is configured and enabled in one verified step, and each
mirrored bucket is an explicit association. The engine is a level-based
reconciler. On a cadence, or immediately when a mutation enqueues a pass, it
snapshots the project's completed local state in one transaction, observes
the destination, and converges the difference with idempotent writes in
dependency order. Row integrity MACs are verified before anything leaves, so
tampered provenance does not propagate. No reconciliation state is
persisted. A crashed pass loses nothing, because the next pass re-derives
the remaining difference. Reconciliation runs outside the serving paths - a
Packer build succeeds with the destination down - and per-project failures
back off without blocking other projects.

Three rules bound the reconciler's authority. The association set is its
entire authority to delete. Inside an associated bucket the source is
authoritative in both directions, and un-associating deletes the destination
copy, while nothing outside an associated bucket is touched. The managed
`latest` channel is never referenced on either side. Completion drives it.
Channel assignments mirror as pointers rather than replayed history. The
sequence of what dufflebag pushed lives in its audit trail, where every
remote mutation is recorded fail-closed before it executes.

The destination adapter carries connection mechanics only. Mirror semantics
are identical for every destination type. The destination credential is
sealed in an AES-256-GCM envelope - the keyring on encrypted deployments, an
environment key otherwise - write-only at the API and HMAC'd in audit.
Deliberately not mirrored: destination-assigned IDs, timestamps and
authorship, channel assignment history, bucket IAM, and SBOMs uploaded after
a destination build completed. [Bag Drop](../administration/bag-drop.md)
covers the operational surface.

## Deployment

One stateless container serves everything: a single binary with the console
embedded, on one listener and one hostname. PostgreSQL holds all state. SBOM
bytes and scan transcripts live in optional S3-compatible object storage,
keyed but not secured by tenant prefixes. On unencrypted deployments,
row-level security on the locator rows is the control. Encrypted deployments
also seal each object before upload. Two further dependencies are optional:
a key service (Vault transit) when encryption at rest was chosen, and an
OSV-compatible scanner endpoint when scanning is configured.

Migrations are embedded in the binary and apply at startup. The hardened
two-role deployment runs them through the `migrate` subcommand under a
privileged role instead, keeping schema changes away from the serving role.
Migrations are written expand-only, so a schema ahead of the binary does not
break it. See [Installation](../quick-start/installation.md) and
[Database](./database.md).

## Connectivity

Every connection the deployment must permit, in both directions of concern:

| From | To | Protocol | Purpose | Required |
|---|---|---|---|---|
| Clients | dufflebag | HTTPS, one hostname at the root | All API and console traffic, including token minting | Yes |
| dufflebag | PostgreSQL | SQL | All state | Yes |
| dufflebag | Object storage | S3 API | SBOM payloads, scan transcripts | Optional |
| dufflebag | Key service (Vault transit) | HTTPS | Keyring unwrap at startup, heartbeat, rotation | With encryption at rest |
| dufflebag | Scanner endpoint | HTTPS | Versioned package queries | With scanning |
| dufflebag | Destination registry | HTTPS | Bag Drop mirror pushes | With Bag Drop |
| dufflebag | Webhook receivers | HTTP(S) | Signed event deliveries | With webhooks |

The dufflebag server accepts no inbound connections other than its one listener, and
initiates nothing beyond this table.

## Failure behaviour

What happens when each dependency is unavailable:

| Dependency down | Serving | Detail |
|---|---|---|
| PostgreSQL | Stops | All state lives here. There is no degraded mode |
| Object storage | Continues | SBOM upload and download answer 503. Everything else serves |
| Key service | Continues | Running replicas keep serving. Health reports `degraded`. A process restart refuses to start (sealed) |
| Every audit sink | Stops | Audit fails closed. An instance that cannot record a request does not serve it |
| One of several audit sinks | Continues | Requests proceed. The failing sink surfaces through audit health |
| Scanner endpoint | Continues | Scans fail and retry on cadence. Findings from the last successful run remain |
| Destination registry | Continues | A Packer build succeeds with the destination down. The mirror backs off and retries |
| Webhook receiver | Continues | Deliveries retry with backoff, then drop. The domain write is never delayed |

## The console

The console is an ordinary client with no backend-for-frontend. Reads come
from the same compatibility plane every other client uses. The platform
plane serves only what has no compatibility-plane equivalent: tenancy,
principals, audit, sessions, instance metadata, encryption control and
bucket pins.

The console serves registry writes where HCP's own console offers the
capability - channel management, promotion, version revocation and restore,
bucket deletion - through the same compatibility endpoints and role rules as
any other client. Terraform remains the recommended interface for automated
registry management. Loading, error and absence are distinct rendered
states, and the console does not invent values the server did not supply.
[The console](./console.md) covers sign-in, role gates and confirmations.
