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

Related: [The audit trail](./audit.md).
