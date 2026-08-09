# Architecture

The boundaries a change must respect, stated in present tense. The
[compatibility reference](compatibility.md) governs everything an external
client can observe; this document covers everything else.

## Two API planes

One domain, two very different contracts:

- **The compatibility plane** (`internal/compat/`) serves the externally owned
  contracts — the OAuth token endpoint, the Packer registry API
  (`/packer/2023-01-01/...`), and the resource-manager API. It is **frozen**:
  handlers preserve client-observed behaviour including its accidents, wire
  models are generated from vendored Swagger specs (`make generate`), and
  nothing extends it. If a behaviour here looks wrong, consult the
  [compatibility reference](compatibility.md) before touching it — the
  strangeness is usually the contract.
- **The platform plane** (`internal/platform/v1`, serving `/api/v1/...` plus
  the unversioned `/sys/*` surfaces — init, recovery, health, session) is
  dufflebag's own OpenAPI 3 contract for tenancy, identity, audit, console
  sessions, encryption control, scanning and bucket pins. It is free to
  evolve; its server interface is generated from `spec/platform/openapi.yaml`
  (`make generate-platform`).

If a capability exists in the compatibility plane, every client uses it —
including our own console. The platform plane never grows a nicer variant of
something the compatibility surface already does; that would leave the compat
surface rotting untested.

**Wire models are never domain models.** Generated models live only under
`internal/compat/*/models`, enforced by depguard import rules — the domain,
store and platform packages may not import the compat packages or the wire
SDKs; the domain speaks its own types
(`Version.Complete` is a bool — the wire's `v0` sentinel is rendered at the
adapter, never stored).

## Tenancy is the security boundary

A tenant is `(organization_id, project_id)`. Isolation is enforced by Postgres
**row-level security**, not application WHERE clauses: `FORCE ROW LEVEL
SECURITY` on tenant tables, session variables set per transaction, and a
serving role that must not be a superuser or hold `BYPASSRLS` — the server
refuses to start otherwise. `make test-rls-sabotage` proves the isolation
tests fail when RLS is sabotaged; a green run of that gate is the evidence the
alarm rings.

**Deny by default at every boundary.** An unauthenticated caller, an
unauthorized one, a malformed identifier, an absent scope and an indeterminate
authorization result all produce the same outcome: nothing. Scope denial and
absence answer identical 404s on the compatibility plane, because existence is
a disclosure; once entitlement to the tenant is established, an insufficient
role answers 403, because there is no existence left to conceal. Tenancy is
checked before role. RLS proves isolation *between*
scopes; entitlement *to* a scope is the authorization funnel's job — the two
are independent and both are tested.

## Identity

The token is the contract, not the credential. Service principals authenticate
with client credentials over HTTP Basic (what the HCP SDK sends); the issuer
seam mints a signed JWT whose claims carry identity and tenancy scope. Roles —
`reader < builder < publisher < maintainer < root` — are resolved from the
database **per request**, never baked into the token, so revocation and role
changes take effect immediately. Secrets are argon2id-hashed, two active
secrets per principal to make rotation routine.

First run is one unauthenticated `POST /sys/init`, one-shot and atomic,
returning the root principal's credentials — and the recovery shares that
back the break-glass ceremony — exactly once. The console wizard is an
ordinary client of the same endpoint; there is no privileged side door. Its
unauthenticated sibling `POST /sys/recovery` accepts a threshold of those
shares and mints a fresh root; every attempt, including refusals, is audited.

## Audit fails closed

Every API request is audited as a request/response pair. UI asset serving and
the health probe are the declared exemptions; admission refusals on the
anonymous surfaces (`/oauth2/token`, `/sys/recovery`) — throttling and the
verification-memory budget — are also decided outside the audit seam, so a
rejected admission attempt is not an audit entry. When audit is enabled and no
healthy sink remains to record an entry, the request fails — an instance that
cannot record a request does not serve it; while at least one configured
target still accepts writes, the request proceeds and the failing sink is
surfaced through audit health. Sensitive values enter the trail only as HMACs,
so correlation survives without the trail holding a usable credential.

## Encryption at rest is a first-boot decision

When a key provider is configured before the first boot, the instance is
stamped encrypted for life — a one-way door in both directions. A keyring of
four independently rotated purpose keys (payload, integrity, token signing,
audit HMAC) lives in Postgres wrapped by the external key service; payloads
and SBOM objects are sealed with AES-GCM under tenant-scoped additional
authenticated data, and identity and provenance rows carry per-row MACs, so a
row inserted around the application fails verification. On encrypted
deployments the token-signing and audit-HMAC keys move out of the environment
into the keyring and their variables are refused. An unreachable key service
seals the process at start; at runtime a heartbeat performs a real unwrap and
reports `degraded` rather than failing the readiness probe.

## The scanner pipeline

dufflebag never scans in-process and never owns or mirrors a vulnerability
feed. A configured adapter (`internal/scan`; OSV is the only one) queries an
external service with versioned package identities derived from the stored
SBOM projection — package name, ecosystem and version leave the deployment,
SBOM documents and raw reports do not. Scan work is queued per tenant and
drained under forced RLS with `FOR UPDATE SKIP LOCKED`, so replicas coordinate
through the queue without a leader. Findings attach to the client-reported
package identity, preserve the provider's severity data verbatim, and derive a
display severity; reads follow the last *successful* run, so a failed newer
attempt never erases findings. Scanner activity is audited as
`system:scanner` and the audit circuit outranks provider faults. The
compatibility plane projects stored findings into the frozen vulnerability
shapes, with coverage and provenance carried in `Dufflebag-Scan-*` response
headers because the frozen JSON has no fields for them.

## Deployment shape

One stateless container: Go binary with the console embedded via `go:embed`,
serving every surface on one listener (the SDK assigns `/oauth2/token` onto
its auth URL, and the path trees do not collide — one hostname serves both).
Postgres holds all state. SBOM bytes and scan transcripts live in optional,
deployment-configured S3-compatible object storage, keyed but not secured by
tenant prefixes — on unencrypted deployments RLS on the locator rows is the
control, while encrypted deployments additionally seal each object before
upload. Two further external dependencies are optional and deployment-shaped:
a key service (Vault transit) behind `internal/keyring` when encryption at
rest was chosen at first boot, and an OSV-compatible scanner endpoint when
scanning is configured. Migrations are embedded in the binary and run via the
`migrate` subcommand under a privileged role; the serving role cannot alter
schema. See [deployment.md](deployment.md).

Migrations are written expand-only, and an expand/contract gate exists
(`cmd/schema-compat`, `make expand-contract`) that runs the previous release's
binary against the new schema. Wiring that gate into CI is deferred until a
deployment shape exists where two application versions share a database — a
rolling update or more than one replica.

## The console

An ordinary client with no backend-for-frontend. Reads come from the same
compatibility plane every other client uses; the platform plane serves only
what has no compatibility-plane equivalent (tenancy, principals, audit,
sessions, instance metadata, encryption rotate/rewrap, rescan requests,
bucket pins). The console states what it knows honestly — loading, error and
absence are distinct rendered states, and it never invents a value the server
did not supply. Registry contents — buckets, channels, promotion — are
managed with Terraform, never console writes; the console's platform-plane
writes are confined to state with no registry consequence.
