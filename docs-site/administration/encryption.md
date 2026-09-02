# Encryption at rest

Encryption at rest is optional and decided at first boot. Configure
`DFBG_KEY_PROVIDER=vault` with the Vault address and authentication settings
before the first start, or run unencrypted.

::: warning
The encryption posture cannot be changed after first boot. A later boot refuses
to serve if its configuration disagrees with the stored posture. Moving from
encrypted to unencrypted, or from unencrypted to encrypted, requires a fresh
database.
:::

The encrypted posture seals payloads and SBOM bytes. It also applies MACs to
provenance and identity rows. A row altered through direct database access
fails verification, as does a principal inserted through direct database
access. Database write access does not grant administration.

The unencrypted break-glass procedure does not work on an encrypted instance.
The recovery-share ceremony is the only recovery path. Losing both the root
credentials and the recovery shares makes the instance unrecoverable.

Key material lives in a wrapped keyring in the database, not in the
environment. On an encrypted deployment, do not set
`DFBG_TOKEN_SIGNING_KEY`, `DFBG_AUDIT_HMAC_KEY`, `DFBG_CREDENTIAL_KEY`, or its
`DFBG_BAGDROP_CREDENTIAL_KEY` alias.

The key service is a startup dependency. If it is unreachable at boot, the
process refuses to start and remains sealed. Running replicas continue serving
during a key-service outage.

![dufflebag Encryption screen showing the keyring purposes and KEK versions](/screenshots/encryption.png)

## Health

An encrypted instance heartbeats the key service about every five minutes by
performing a real keyring unwrap. The result is `unconfigured`, `ok`, or
`degraded`. It appears on `/sys/health`, `GET /api/v1/instance`, and the
console's **Encryption** page. A `degraded` status does not fail the readiness
probe. Alert when the status becomes `degraded`.

::: warning
A degraded status does not affect serving, but the process will fail to start while the key service is unreachable. Fix the key service before restarting the instance.
:::

## Rotation

KEK rotation and data-key rotation are separate operations. Both require root,
are audited, and are available on the **Encryption** page.

### Rotate the KEK

Prerequisites: Root access to the **Encryption** page and permission to rotate
the key service's key.

1. Rotate the key at the key service.

2. Refresh the page and confirm that the **Latest KEK** column shows the
   rotated version. **Rewrap** is offered while any keyring row trails that
   version; a keyring already at the current version has nothing to rewrap,
   and the control says so.

3. Select **Rewrap** to rewrap the keyring rows.

4. Confirm that every entry names the new key version.

5. Retire the old version at the key service.

::: warning
Do not retire an old key version while any keyring row still names it. Running
replicas continue serving, but every restart will refuse to start. The
deployment guide documents the seal-out recovery procedure.
:::

### Rotate the data keys

Prerequisites: Root access to the **Encryption** page. Perform the rotation in
a quiet window.

1. Select **Rotate**.

The operation creates new key versions for every keyring purpose. New writes
use them immediately. Old versions stay in the keyring permanently, so data is
not re-encrypted and existing verification continues. Tokens expire within
their TTL. Audit HMAC correlation becomes specific to each key version.
Multi-replica peers adopt the new versions at their next heartbeat.

The [deployment guide](../components/encryption.md#encryption-at-rest-optional-decided-at-first-boot)
contains the command sequences, seal-out recovery procedure, and
Kubernetes-native Vault authentication mode.

## Where to go next

- [Installation](../quick-start/installation.md): serving, health probes, first run,
  and recovery.
- [Roles and principals](./principals.md): the recovery shares and the
  root principal.

Related: [The audit trail](./audit.md).
