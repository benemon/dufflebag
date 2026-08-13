# Audit and encryption

Two operator concerns, deliberately independent: the **audit trail** records
what the instance did, and **encryption at rest** protects what it stores.
Both are managed from the console's root-only **Audit** and **Encryption**
pages or the equivalent [platform API](/platform-api.html) endpoints; the
deeper operational contract lives in the
[deployment guide](../deployment/index.md).

## The audit trail

Every API request is audited as a request/response pair. The declared
exemptions are UI asset serving, the health probe, and admission refusals on
the anonymous surfaces (`/oauth2/token`, `/sys/recovery`), which are decided
before the audit seam.

Sensitive values never enter the trail directly — they are recorded as HMACs,
so entries can be correlated (the same secret produces the same HMAC) without
the trail holding a usable credential. Entries record the HMAC key version
that produced them.

### File targets

Audit entries go to **file targets**: up to three paths, created and removed
by `root`. Each target reports its health — `healthy` or `failing`, with
consecutive and cumulative failure counts, the last failure time, and the
last successful reopen.

An instance with **no targets configured does not audit at all** — the
console warns exactly that before letting you remove the last one.

### Fail-closed

Once auditing is enabled, it fails closed. While at least one configured
target still accepts writes, requests proceed and the failing target is
surfaced through its health. When **no** healthy target remains, the instance
stops serving requests rather than serving them unrecorded, and
`GET /sys/health` answers 503. Three targets on independent failure domains
is the posture that makes this a property rather than a liability.

### Log rotation

Send the process `SIGHUP` and it reopens every file target in write order —
the standard logrotate contract. A rename-then-reopen rotation is handled; a
failed reopen keeps writing to the previous descriptor rather than dropping
entries. `last_reopened_at` on the target tells you the rotation took.

## Encryption at rest

Encryption at rest is optional and **decided at first boot**: configure
`DFBG_KEY_PROVIDER=vault` (with the Vault address and auth settings) before
the first start, or run unencrypted. The choice is a one-way door in both
directions — a later boot whose configuration disagrees refuses to serve, and
moving between postures means a fresh database.

What the encrypted posture buys, beyond sealed payloads and SBOM bytes: the
provenance and identity rows are MAC'd, so a row altered — or a principal
inserted — by direct database access fails verification. **Database write
access is not administration.** The corollary: the unencrypted break-glass
procedure does not work here; the recovery-share ceremony is the only way
back in, and losing both the root credentials and the recovery shares is
unrecoverable by design.

Key material lives in a wrapped keyring in the database, not the environment:
on an encrypted deployment `DFBG_TOKEN_SIGNING_KEY`, `DFBG_AUDIT_HMAC_KEY`,
`DFBG_CREDENTIAL_KEY`, and its `DFBG_BAGDROP_CREDENTIAL_KEY` alias must **not**
be set. The key service is a
startup dependency only — unreachable at boot means the process refuses to
start ("sealed"), while running replicas keep serving through a key-service
outage.

### Health

An encrypted instance heartbeats the key service about every five minutes
with a real keyring unwrap. The result — `unconfigured`, `ok` or `degraded` —
appears on `/sys/health`, `GET /api/v1/instance`, and the console's
Encryption page. `degraded` never fails the readiness probe: serving is
unaffected; what it threatens is the **next process start**. Alert on it and
fix the cause — do not restart on it, because the restart is exactly what
will fail.

### Rotation

Two rotations, deliberately separate, both root-only and audited, both on the
Encryption page:

- **KEK rotation** (the key service's key): rotate at the key service, then
  **Rewrap** the keyring rows, confirm every entry names the new version, and
  only then retire the old version at the key service. Retiring it while any
  row still names it is the seal-out trap — running replicas keep serving but
  every restart refuses.
- **Data-key rotation** (**Rotate**): mints fresh key versions for every
  keyring purpose. New writes use them immediately; old versions stay in the
  keyring forever, so nothing is re-encrypted and nothing stops verifying.
  Tokens age out within their TTL, audit HMAC correlation becomes
  per-key-version, and multi-replica peers adopt the new versions at their
  next heartbeat — rotate in a quiet window.

The exact command sequences, the seal-out recovery procedure, and the
Kubernetes-native Vault auth mode are in the
[deployment guide](../deployment/encryption-setup.md#encryption-at-rest-optional-decided-at-first-boot).

## Where to go next

- [Deployment guide](../deployment/index.md)
  — serving, health probes, first run, recovery.
- [Roles and principals](./roles-principals.md) — the recovery shares and the
  root principal.
