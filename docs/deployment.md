# Deploying dufflebag

This document brings up dufflebag from the published container image on
infrastructure you own: PostgreSQL, TLS, the optional object store and key
service, migrations and first run. Client configuration — pointing Packer and Terraform at the
result — is covered in the [README](../README.md#getting-started), and the
server's environment variables are listed in its
[configuration reference](../README.md#configuration-reference).

## The image

```
quay.io/benjamin_holmes/dufflebag:<tag>
```

Tags mirror git tags. Plain `vX.Y.Z` tags are releases and do not expire;
`-rc` tags are release candidates and are deleted from the registry
automatically after their expiry window. The image is based on Red Hat UBI
(ubi9 micro), runs as a non-root user, and serves everything from one process:
the OAuth token endpoint, the Packer registry API, the resource-manager API,
first-run initialisation and the embedded console.

## One hostname, no path prefix

Plan the ingress around two constraints that come from the Packer SDK, not
from dufflebag:

- **One hostname serves everything.** The SDK requires `HCP_AUTH_URL` to be
  HTTPS, and the auth and API path trees do not collide, so a single hostname
  and listener serve both. Nothing needs to be split.
- **A path prefix is impossible.** The SDK *assigns* `/oauth2/token` onto
  `HCP_AUTH_URL` rather than appending to it, so any path prefix in the URL
  vanishes silently and authentication fails. dufflebag must sit at the root
  of its hostname — `https://registry.example.com`, never
  `https://example.com/registry`.

TLS can terminate in a proxy or ingress in front of the process (leave
`DFBG_TLS_CERT_FILE`/`DFBG_TLS_KEY_FILE` unset and serve plaintext behind it), or in the
process itself by mounting a certificate and setting both variables.

Behind a proxy or OpenShift Route, set `DFBG_TRUSTED_PROXIES` to the proxy or
ingress egress range, or the per-caller token throttle collapses to one shared
bucket. Never include ranges that clients can occupy.

## PostgreSQL: two roles

Migrations legitimately need privileges the serving process must not hold. The
serving role is refused at startup if it is a superuser or holds `BYPASSRLS`,
because either would disable row-level security — the tenancy boundary —
without any error. On PostgreSQL 15 and later, the database ownership below is
what confers `CREATE` on schema `public` (earlier versions granted it to every
role by default). Create one role that owns the schema and one that uses it:

```sql
CREATE DATABASE dufflebag;
CREATE ROLE dufflebag_migrate LOGIN PASSWORD '<migration password>';
ALTER DATABASE dufflebag OWNER TO dufflebag_migrate;

CREATE ROLE dufflebag_app LOGIN PASSWORD '<serving password>'
    NOSUPERUSER NOBYPASSRLS;
```

Then, connected to the `dufflebag` database as `dufflebag_migrate` (after the
first migration run, or before it — default privileges cover tables that do
not exist yet):

```sql
GRANT USAGE ON SCHEMA public TO dufflebag_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public
    TO dufflebag_app;
ALTER DEFAULT PRIVILEGES FOR ROLE dufflebag_migrate IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dufflebag_app;
```

The `ALTER DEFAULT PRIVILEGES` line is what makes upgrades routine: tables
added by future migrations are usable by the serving role without a manual
grant step.

## Migrations

The same image is the migration tool. Run it with the privileged role before
(or as an init container alongside) the serving process:

```sh
docker run --rm \
  -e DFBG_DATABASE_URL='postgres://dufflebag_migrate:<migration password>@db/dufflebag' \
  quay.io/benjamin_holmes/dufflebag:<tag> migrate
```

It applies any pending schema migrations and exits zero; running it again is a
no-op, so it is safe on every deploy. The server also attempts migration at
startup — on a database the migrate step has prepared, that attempt does
nothing, and on a schema change it cannot apply with the serving role it fails
rather than serving a schema it does not understand.

## Object storage

SBOMs live in an S3-compatible object store. Without one configured, the
server starts and serves everything except SBOM upload and download, which
answer 503 — a reasonable way to evaluate dufflebag before committing storage
to it. Configure it with the `DFBG_OBJECT_STORAGE_*` variables in the configuration
reference; the bucket must already exist, and the server verifies it is
reachable at startup. Vulnerability scanning stores its transcripts here too,
so a configured scanner without object storage is a startup error, not a
degraded mode.

## Vulnerability scanning (optional)

Scanning is absent unless `DFBG_SCANNER_ADAPTER=osv` is configured. An
unconfigured or disabled scanner does not produce an empty result, and the
console never describes that state as clean. The configuration group is
fail-closed: any other `DFBG_SCANNER_*` variable set while the adapter is
unset — or an unknown scanner variable or value — refuses startup, as does a
configured adapter without [object storage](#object-storage), which holds the
scan transcripts.

The default OSV endpoint is the live `api.osv.dev` service. When enabled,
dufflebag sends ecosystem-form queries derived from versioned package purls;
those package inventory names and versions therefore leave the deployment. It
never sends an SBOM document or a raw scanner report. Provider response bodies
are the exception: dufflebag keeps an audit transcript — encrypted on
deployments with encryption at rest — for seven days, then deletes the
transcript while retaining its digest.

The repository's long-lived `make demo-up` stack deliberately selects this
live endpoint and therefore requires internet egress. CI uses recorded
responses on an internal Docker network instead.

## Bag Drop

Bag Drop mirrors a project's registry data to a destination registry; the
[architecture document](architecture.md#bag-drop-the-outbound-mirror) covers
the design. Operationally:

Configuring or verifying an HCP Packer Bag Drop destination makes outbound
HTTPS requests to `auth.idp.hashicorp.com` for the client-credentials grant and
`api.cloud.hashicorp.com` for scoped reads and destination writes. Permit egress
to both hosts. A destination may instead be another dufflebag instance,
configured with its HTTPS endpoint and an optional PEM CA chain. The supplied
chain augments system trust and is stored as public configuration data, not as
a path or secret. Dufflebag destinations use the same token and Packer API wire
contract, and their mirror lifecycle semantics are identical to HCP destinations.
Once an enabled configuration has active associations, the
background reconciler creates and converges destination buckets, completed
versions, completed builds and their artifacts. Mirror semantics are complete:
drift inside associated buckets is removed, local bucket deletions propagate,
and un-associating deletes the destination copy — a pending removal is
retained and retried until that deletion succeeds. Ordinary channels and their
assignments mirror as pointers; the managed `latest` channel is never touched.
The association set is the reconciler's entire authority to delete: nothing
outside an associated bucket is ever touched.

SBOMs mirror by name during the build's mirrored running window. SBOMs added
after a destination build has completed surface as permanent drift rather than
causing the build to be deleted or recreated. Dufflebag reads each local
document through the same object download and decryption path used by the
compatibility proxy, then sends the vendored zstd `compressed_sbom` upload
shape. A destination size refusal (the observed bare gateway 504, HTTP 413, or
another size-shaped refusal) skips that SBOM, records the refusal in the
association's `last_sync_error`, and does not prevent its remaining work or
another association from converging. The destination API can list and read
SBOMs but cannot delete one, so a remote-only SBOM is likewise recorded as
non-removable drift. Version revocation state mirrors in both directions: local
revocation schedules and messages are pushed, while a remotely revoked version
whose local source is active is restored.

`DFBG_BAGDROP_RECONCILE_INTERVAL` controls the level-reconcile cadence as a Go
duration and defaults to `5m`; an invalid or non-positive value refuses
startup. Per-project failures back off in memory at `interval * 2^failures`,
capped at one hour, and a successful run resets the count. A `Retry-After`
header, if a destination ever supplies one, is honoured when it asks for a
longer delay.

Destination mutations are audit fail-closed. If no configured audit sink can
accept the pre-mutation or outcome record, Bag Drop sync pauses until the next
cadence tick; the ordinary API and compatibility serving paths remain
available because reconciliation runs outside them.

The destination client secret is always stored in an AES-256-GCM envelope. On
an unencrypted deployment, set `DFBG_BAGDROP_CREDENTIAL_KEY` to exactly 32
random bytes. Without it, ordinary reads and deletion of a disabled existing
configuration still work, but writes that seal a secret and verify/enable
operations that unseal one refuse and name the missing variable. There is no
plaintext fallback.

This environment key protects a database dump that does not also contain the
process environment. It does **not** resist compromise of the host, container
environment, or a process that can read the key. Treat it as credential
material and source it from the deployment's secret manager.

On a deployment with [encryption at rest](#encryption-at-rest-optional-decided-at-first-boot),
Bag Drop credentials use the wrapped keyring instead. In that posture
`DFBG_BAGDROP_CREDENTIAL_KEY` must not be set; the process refuses to start
rather than accepting a second source of truth.

Scan transcripts are written to object storage tagged
`dufflebag-class=transcript`. dufflebag deletes each referenced transcript
after its seven-day window, but two narrow crash windows can leave an object
behind with no database row referencing it — storage waste, not a
correctness or disclosure problem. To collect those strays, configure a
bucket lifecycle rule filtering on that tag with an expiry comfortably past
the seven days (say 14): referenced transcripts are gone before the rule
fires, and orphans age out on their own. Never apply an expiry to untagged
objects — SBOMs live in the same bucket and live forever.

## Serving

```sh
docker run -d --name dufflebag -p 8443:8443 \
  -e DFBG_DATABASE_URL='postgres://dufflebag_app:<serving password>@db/dufflebag' \
  -e DFBG_HTTP_ADDR=:8443 \
  -e DFBG_TOKEN_SIGNING_KEY='<at least 32 random bytes>' \
  -e DFBG_TOKEN_ISSUER='https://registry.example.com' \
  -e DFBG_TLS_CERT_FILE=/tls/tls.crt -e DFBG_TLS_KEY_FILE=/tls/tls.key \
  -v /path/to/certs:/tls:ro \
  quay.io/benjamin_holmes/dufflebag:<tag>
```

`GET /sys/health` is the readiness probe. It needs no credential, and it
reports whether the instance has been initialised:

```sh
curl https://registry.example.com/sys/health
```

It answers 200 only once first run has completed: an unclaimed instance
answers 501, and an instance whose every audit sink is unhealthy answers 503.
Wired as a stock Kubernetes readiness probe, a fresh pod therefore does not
become ready until first run completes — drive first run against the pod
directly, or tolerate the not-ready window.

Prometheus metrics use a separate listener configured by `DFBG_METRICS_ADDR`,
so the public hostname carries no unauthenticated operational surface. The
metrics endpoint is unauthenticated by design; scope its exposure with the bind
address or a network policy. When the variable is unset, no metrics server is
started.

On Kubernetes or OpenShift the same pieces map to: a `migrate`-subcommand init
container using the migration role's `DFBG_DATABASE_URL`, the serving container
using the application role's, a Secret for the two connection strings and the
signing key, and an Ingress or Route carrying the hostname — subject to the
no-path-prefix constraint above. The plain YAML in
[`deploy/kubernetes/`](../deploy/kubernetes/) provides reference manifests for
that layout. Readiness uses `/sys/health` and remains false while an instance is
unclaimed; liveness uses the serving port so first run cannot cause a restart
loop.

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
DFBG_KEY_PROVIDER=vault          # the only provider currently implemented
VAULT_ADDR=https://vault.example.com:8200
# plus the Vault SDK's own environment: VAULT_TOKEN, VAULT_NAMESPACE,
# VAULT_CACERT, ...
DFBG_VAULT_AUTH_METHOD=env       # default: token, Agent or proxy ambient auth
DFBG_VAULT_TRANSIT_MOUNT=transit # default
DFBG_VAULT_TRANSIT_KEY=dufflebag # default; created on first use
```

`DFBG_VAULT_AUTH_METHOD=env` is the default and preserves the Vault SDK's
ambient contract: `VAULT_TOKEN`, or `VAULT_AGENT_ADDR`/`VAULT_PROXY_ADDR`
pointing at a co-located agent or proxy, supplies the credential. This is also
the VM answer: Vault Agent covers that workload, so dufflebag deliberately has
no AppRole mode. dufflebag does not renew credentials in `env` mode.

In a cluster, `DFBG_VAULT_AUTH_METHOD=kubernetes` is recommended. It performs
native Kubernetes login, requires `DFBG_VAULT_K8S_ROLE`, and automatically
renews its Vault token and logs in again when renewal is exhausted or fails.
The auth mount defaults to `kubernetes` and can be changed with
`DFBG_VAULT_K8S_MOUNT`; the projected service-account token defaults to
`/var/run/secrets/kubernetes.io/serviceaccount/token` and can be changed with
`DFBG_VAULT_K8S_TOKEN_PATH` for non-standard projections.

`VAULT_ADDR` is required: with no address configured (and, in `env` mode, no
`VAULT_AGENT_ADDR`/`VAULT_PROXY_ADDR`), the process refuses to start with a
message naming it rather than silently targeting the SDK's localhost default.
Kubernetes mode always requires `VAULT_ADDR`; an agent or proxy address does
not satisfy it because dufflebag performs the login itself.

What to know before choosing it:

- **It is a one-way door, both directions.** The mode is stamped at first
  boot; a later boot whose configuration disagrees refuses to serve. There is
  no migration between postures — moving means a fresh database.
- **Keys live in a wrapped keyring, not the environment.**
  `DFBG_TOKEN_SIGNING_KEY`, `DFBG_AUDIT_HMAC_KEY`,
  `DFBG_AUDIT_HMAC_KEY_VERSION` and `DFBG_BAGDROP_CREDENTIAL_KEY` must NOT be
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

## First run

An uninitialised instance is claimable by whoever reaches it first — that is
the accepted bootstrap trade-off, so complete first run **before** exposing
the hostname beyond your own reach.

Either open the console in a browser and follow the wizard, or claim it
headlessly — both use the same endpoint, and it works exactly once:

```sh
curl -sX POST https://registry.example.com/sys/init \
  -H 'content-type: application/json' -d '{}'
```

The response contains the root principal's `client_id` and `client_secret`,
plus one or more **recovery shares** and the `recovery_threshold` — the
number of shares `/sys/recovery` will demand. **All of it is shown exactly once and
cannot be retrieved again** — store the credentials in your secret manager and
the shares offline, separately, before doing anything else. The shares are the
supported path back in if the root secret is lost (see
[Recovery](#recovery)).

By default there is a single share. For a threshold ceremony — *k* of *n*
custodians must co-operate to recover — pass the parameters in the request
body; they are fixed at initialisation and cannot be changed later. The bounds
are `1 ≤ k ≤ n ≤ 255`; anything else answers 400:

```sh
curl -sX POST https://registry.example.com/sys/init \
  -H 'content-type: application/json' \
  -d '{"recovery_share_count": 5, "recovery_threshold": 3}'
```

From here the console (or the platform API) creates an organisation, a
project, and a project-scoped service principal for Packer; the README's
[Getting started](../README.md#getting-started) section covers pointing the
stock client at the result.

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
