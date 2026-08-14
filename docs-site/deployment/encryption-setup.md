# Encryption setup

## Encryption at rest (optional, decided at first boot)

With a key provider configured, dufflebag encrypts opaque payloads (build
metadata, SBOM bytes — sealed before they reach object storage and decrypted
only on the download proxy), MACs its provenance rows so alteration fails the
read, and MACs its identity rows so a principal inserted directly into the
database **cannot authenticate** — database write access is not
administration. The break-glass procedure in [Recovery](#recovery) therefore
does not work on an encrypted deployment; the recovery ceremony is the only
path back in, and losing both the root credentials and the recovery shares is
accepted as unrecoverable, exactly as lost Vault unseal keys are.

Configure it by pointing at the key service before the **first** boot:

```sh
DFBG_KEY_PROVIDER=vault                    # the only provider currently implemented
DFBG_VAULT_ADDR=https://vault.example.com:8200
DFBG_VAULT_TOKEN=...                       # token mode
DFBG_VAULT_CACERT=/etc/ssl/vault-ca.pem    # optional
DFBG_VAULT_TRANSIT_NAMESPACE=platform      # optional
DFBG_VAULT_AUTH_METHOD=token               # default
DFBG_VAULT_TRANSIT_MOUNT=transit           # default
DFBG_VAULT_TRANSIT_KEY=dufflebag           # default; created on first use
```

`DFBG_VAULT_AUTH_METHOD=token` is the default and uses
`DFBG_VAULT_TOKEN`. dufflebag does not renew this token; the operator owns its
rotation.

`DFBG_VAULT_AUTH_METHOD=kubernetes` performs native Kubernetes login. It
requires `DFBG_VAULT_K8S_ROLE`, renews its Vault token, and logs in again when
renewal is exhausted or fails. The auth mount defaults to `kubernetes` and can
be changed with
`DFBG_VAULT_K8S_MOUNT`; the projected service-account token defaults to
`/var/run/secrets/kubernetes.io/serviceaccount/token` and can be changed with
`DFBG_VAULT_K8S_TOKEN_PATH` for non-standard projections.

`DFBG_VAULT_AUTH_METHOD=approle` performs native AppRole login. It requires
`DFBG_VAULT_APPROLE_ROLE_ID` and `DFBG_VAULT_APPROLE_SECRET_ID_FILE`. The
secret-id file is read at every login, so rotating the secret ID means replacing
that file. The auth mount defaults to `approle` and can be changed with
`DFBG_VAULT_APPROLE_MOUNT`. AppRole renews its Vault token and logs in again on
the same lifecycle as Kubernetes authentication.

::: warning
The Vault SDK's native environment variables (`VAULT_ADDR`, `VAULT_TOKEN`, ...)
remain honored for edge cases. dufflebag's `DFBG_VAULT_*` values take
precedence where both are set. See the
[Vault documentation](https://developer.hashicorp.com/vault/docs/commands#environment-variables)
for the native variables.
:::

`DFBG_VAULT_ADDR` identifies the Vault service. With no accepted address, the
process refuses to start with a message naming `DFBG_VAULT_ADDR` rather than
silently targeting the SDK's localhost default. Kubernetes and AppRole modes
require `DFBG_VAULT_ADDR` or the native address escape hatch because dufflebag
performs the login itself.

::: info
`DFBG_VAULT_TRANSIT_NAMESPACE` is the operating namespace and governs transit.
`DFBG_VAULT_AUTH_NAMESPACE` scopes only the login and the token renewal that
follows it. Set it when a Vault Enterprise auth mount lives in a different
namespace from the transit engine. When it is unset, login happens in the
transit namespace.
:::

What to know before choosing it:

- **It is a one-way door, both directions.** The mode is stamped at first
  boot; a later boot whose configuration disagrees refuses to serve. There is
  no migration between postures — moving means a fresh database.
- **Keys live in a wrapped keyring, not the environment.**
  `DFBG_TOKEN_SIGNING_KEY`, `DFBG_AUDIT_HMAC_KEY`,
  `DFBG_AUDIT_HMAC_KEY_VERSION`, `DFBG_CREDENTIAL_KEY` and
  `DFBG_BAGDROP_CREDENTIAL_KEY` must NOT be
  set — data keys are generated locally, stored wrapped by the key
  service's key, and unwrapped once at startup. KEK rotation rewraps keyring
  rows only; payloads are never re-encrypted (see
  [Key rotation](#key-rotation)).
- **Sealed semantics.** The key service is a startup dependency, never a
  write-path one: if it is unreachable at boot the process refuses to start
  ("sealed"), while already-running replicas keep serving through the outage.
  Size probes and rollouts accordingly.
- A database or bucket dump yields wrapped keys and ciphertext, useless
  without the KEK — which never leaves the key service. The KEK custodian can
  still unwrap a dumped keyring offline; the admin-brick above is
  administrative, not data loss.

### Encryption health

An encrypted instance heartbeats the key service roughly every five minutes:
a real unwrap of one stored keyring row, the same operation a process start
performs. The remembered result is reported as `encryption` on `/sys/health`
and `GET /api/v1/instance` — `unconfigured`, `ok` or `degraded` — and on the
console's Encryption page.

`degraded` means the last heartbeat could not unwrap the keyring: the key
service is unreachable, the credential it authenticates with has expired, or
the key service has retired the KEK version the rows are wrapped under (the
seal-out trap under [Key rotation](#key-rotation)). It **never fails the
probe**: the write path does not touch the key service, so serving replicas
are unaffected — what `degraded` threatens is the *next* process start, which
would refuse to serve. Alert on it and fix the cause; do not restart on it,
because a restart is precisely the thing that will fail. Why the failure
happened stays in the server log, not on the unauthenticated probe.

### Key rotation

Two rotations, deliberately separate (ADR-0024). Both are root-only, audited
operations, available on the console's Encryption page and as bare endpoints.

**KEK rotation** — the key service's key, routine hygiene. Order matters:

```sh
vault write -f transit/keys/dufflebag/rotate   # 1. rotate at the key service

curl -sX POST https://registry.example.com/api/v1/encryption/rewrap \
  -H "authorization: Bearer $TOKEN"            # 2. rewrap the keyring rows
```

Then confirm every entry's `kek_ref` names the new version — the console
shows it, or `GET /api/v1/encryption` — and only **after** that raise
`min_decryption_version` on the transit key to retire the old one. Rewrap
touches only the keyring rows — four per data-key version; the data keys and
every payload are unchanged.

Raising `min_decryption_version` while any keyring row still names an older
version is the seal-out trap: running replicas keep serving (their keys are
in memory), but every unwrap now fails — `encryption` goes `degraded` within
about five minutes, and any replica that restarts refuses to serve (sealed).
Recover by lowering `min_decryption_version` back below the stranded
`kek_ref`, rewrapping, and raising it again.

**Data-key rotation** — `POST /api/v1/encryption/rotate` mints a fresh key
version for every keyring purpose; new writes use it from that moment. Old
versions stay in the keyring forever, so nothing is re-encrypted and nothing
stops verifying. What changes at the boundary:

- Tokens signed with the previous key keep verifying and age out within the
  15-minute TTL.
- Audit entries record the HMAC key version that produced them; the HMAC of
  the same sensitive value differs across the boundary, so correlation is
  per-version by design.
- On multi-replica deployments, peers adopt the new versions at their next
  heartbeat — up to about five minutes — and until then a payload or token
  minted under the new version by one replica does not verify on a peer.
  Rotate in a quiet window.

## Recovery

If every root credential is lost, present the recovery shares — at least the
threshold number, in one request — to mint a **fresh** root principal:

```sh
curl -sX POST https://registry.example.com/sys/recovery \
  -H 'content-type: application/json' \
  -d '{"shares": ["dfbg-recovery-1:...", "dfbg-recovery-1:..."]}'
```

The response carries the new root's `client_id` and `client_secret`, once,
exactly like `/sys/init`. The shares are a proof of custody, not key material:
they remain valid afterwards, and every attempt — successful or refused — is a
distinct `instance.recover` entry in the audit trail. The endpoint shares the
token endpoint's per-caller rate limit.

Losing the credentials *and* the shares leaves no supported recovery on this
surface. The floor below it is honest: anyone holding `DFBG_DATABASE_URL` on an
unencrypted deployment can mint a root directly, which is what the break-glass
procedure below writes down. Treat database access accordingly.

### Break glass

Use this only when the shares are gone, and note that it works **only on
unencrypted deployments** — on an encrypted one the inserted rows carry no
integrity MAC and the principal fails authentication (see
[Encryption at rest](#encryption-at-rest-optional-decided-at-first-boot)).
It requires the application role's
`DFBG_DATABASE_URL` and the `argon2` CLI (packaged as `argon2` in the common
distributions).

Generate a secret and hash it — the server verifies against the parameters
carried in the hash, so these need not match the server's own; the ones shown
do match, which keeps each verification inside the memory budget the token
endpoint's admission control assumes:

```sh
CLIENT_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
CLIENT_SECRET=$(openssl rand -base64 32 | tr -d '=+/')
HASH=$(printf %s "$CLIENT_SECRET" | argon2 "$(openssl rand -hex 8)" -id -t 2 -k 19456 -p 1 -l 32 -e)
```

Insert a platform-scoped root principal and its secret in one transaction:

```sh
psql "$DFBG_DATABASE_URL" <<SQL
BEGIN;
INSERT INTO principals (id, name, client_id, organization_id, project_id, role, created_at)
VALUES (gen_random_uuid()::text, 'break-glass administrator', '$CLIENT_ID', NULL, NULL, 'root', now());
INSERT INTO principal_secrets (id, principal_id, encoded_hash, created_at)
SELECT gen_random_uuid()::text, id, '$HASH', now()
FROM principals WHERE client_id = '$CLIENT_ID';
COMMIT;
SQL
```

`$CLIENT_ID`/`$CLIENT_SECRET` now authenticate at the token endpoint and the
console. This procedure is integration-tested against every release
(`TestBreakGlassRunbookMintsARootThatSignsIn`); note that unlike
`/sys/recovery` it leaves no audit trail of its own — only the sign-ins that
follow.
