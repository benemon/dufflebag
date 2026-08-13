# Deploying dufflebag

This document brings up dufflebag from the published container image on
infrastructure you own: PostgreSQL, TLS, the optional object store and key
service, migrations and first run. Client configuration — pointing Packer and Terraform at the
result — is covered in [Getting started](../getting-started/first-use.md), and the
server's environment variables are listed in its
[configuration reference](#configuration-reference).

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

## Helm

The repository includes a self-contained, single-replica chart for a disposable
or lab deployment. It installs dufflebag, PostgreSQL, ceph-aio object storage,
and community Vault without dependency charts:

```sh
helm install dufflebag deploy/helm/dufflebag \
  --namespace dufflebag --create-namespace
```

The chart is also published as a Helm repository on the documentation site,
versioned per merge to main:

```sh
helm repo add dufflebag https://benemon.github.io/dufflebag/charts
helm install dufflebag dufflebag/dufflebag \
  --namespace dufflebag --create-namespace
```

Only the most recently published chart version is served.

The defaults select the image repository and tag for each component, allocate
persistent volumes for PostgreSQL, Ceph and Vault, and leave component resource
requests and limits unset. The same values file carries the internal database
and object-storage credentials; override those defaults outside an isolated
lab. `ingress.enabled` adds a plain Kubernetes Ingress. On OpenShift,
`route.enabled` adds an edge-terminated Route and `route.host` optionally fixes
its hostname. With neither enabled, the only access point is the in-cluster
`dufflebag` Service.

On OpenShift, also set `security.openshift=true`. The profile keeps every pod
under the restricted-v2 constraints — dufflebag, PostgreSQL and Vault all run
happily at an arbitrary non-root UID — with one exception: the Ceph
all-in-one image must run as root, so the chart pins its ServiceAccount to
the `anyuid` SCC via a RoleBinding. Installing therefore needs a user who can
grant SCC use and create the chart's one ClusterRoleBinding (Vault's token
reviewer); in practice that means a cluster administrator, which is also true
of most Route-bearing charts. On plain Kubernetes leave the profile off: the
PostgreSQL and Ceph images start as root and drop privileges themselves,
which the default profile permits with a documented capability set.

The chart's Vault lifecycle is deliberately lab-grade. A bootstrap Job creates
one unseal share and stores both that unseal key and Vault's root token in a
namespace Secret; an unsealer sidecar reads the Secret after restarts. Anyone
who can read that Secret controls Vault. Production deployments should bring
their own independently operated Vault instead of adopting this trust model.
Deleting the namespace deletes the escrowed credentials, so a retained Vault
volume without its matching Secret cannot be unsealed.

The chart runs exactly one dufflebag replica. It does not add leader election or
other HA machinery. A fresh instance stays NotReady until the one-shot
`POST /sys/init` request claims it, matching the readiness contract described
under [Serving](./operations.md#serving).

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
[Recovery](./encryption-setup.md#recovery)).

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
[Getting started](../getting-started/first-use.md) section covers pointing the
stock client at the result.

## Configuration reference

Infrastructure is configured by the deployment. dufflebag does not persist its
database or object-store credentials through the platform API.

### Server

| Variable | Default | Description |
|---|---|---|
| `DFBG_DATABASE_URL` | — | PostgreSQL connection string. Required; the serving role must not be a superuser and must not hold `BYPASSRLS` |
| `DFBG_TOKEN_SIGNING_KEY` | — | Token-signing key. Required and at least 32 bytes — unless encryption at rest is configured, in which case it must NOT be set (the key lives in the wrapped keyring) |
| `DFBG_TOKEN_ISSUER` | `https://dufflebag.local` | Issuer recorded in access tokens |
| `DFBG_HTTP_ADDR` | `:8080` | HTTP listen address |
| `DFBG_METRICS_ADDR` | — | Prometheus metrics listener; unset means no metrics server; bind to an internal address — the endpoint is unauthenticated by design |
| `DFBG_TLS_CERT_FILE` / `DFBG_TLS_KEY_FILE` | — | Built-in TLS certificate and key; set both or neither |
| `DFBG_TRUSTED_PROXIES` | — | Trusted reverse-proxy CIDRs. When the peer is trusted, per-caller admission keys on the rightmost untrusted `X-Forwarded-For` entry; unset means every caller keys on its own peer address |
| `DFBG_API_MAX_BODY_BYTES` | `4194304` | Maximum JSON request body on the 2023 compatibility surface |
| `DFBG_SHUTDOWN_GRACE_PERIOD` | `10s` | Shared deadline for HTTP and audit shutdown |
| `DFBG_AUDIT_HMAC_KEY` / `DFBG_AUDIT_HMAC_KEY_VERSION` | — | Required together when an audit target is configured; must NOT be set when encryption at rest is configured |

### Encryption at rest

Optional, chosen at the first boot and permanent for the instance's lifetime —
see the [encryption setup](./encryption-setup.md#encryption-at-rest-optional-decided-at-first-boot)
before setting it. The Vault connection itself uses the Vault SDK's own
environment (`VAULT_ADDR`, `VAULT_TOKEN`, `VAULT_NAMESPACE`, `VAULT_CACERT`, ...);
dufflebag's variables select native Kubernetes login when required.

| Variable | Default | Description |
|---|---|---|
| `DFBG_KEY_PROVIDER` | — | Key service wrapping the keyring. `vault` (transit) is the only provider; unset means no encryption at rest |
| `DFBG_VAULT_TRANSIT_MOUNT` | `transit` | Transit engine mount path |
| `DFBG_VAULT_TRANSIT_KEY` | `dufflebag` | Transit key name; created on first use |
| `DFBG_VAULT_AUTH_METHOD` | `env` | Vault authentication: `env` uses `VAULT_TOKEN` or an agent/proxy address; `kubernetes` performs native login |
| `DFBG_VAULT_K8S_ROLE` | — | Vault Kubernetes auth role; required when `DFBG_VAULT_AUTH_METHOD=kubernetes` |
| `DFBG_VAULT_K8S_MOUNT` | `kubernetes` | Vault Kubernetes auth mount path |
| `DFBG_VAULT_K8S_TOKEN_PATH` | `/var/run/secrets/kubernetes.io/serviceaccount/token` | Projected service-account token path; override for non-standard projections |

### Object storage

All five values are optional as a group. If any is set, every value must be
valid and the bucket must already exist; a configured but unavailable store is
a startup error.

| Variable | Description |
|---|---|
| `DFBG_OBJECT_STORAGE_ENDPOINT` | S3-compatible endpoint |
| `DFBG_OBJECT_STORAGE_REGION` | Region supplied to the S3 client |
| `DFBG_OBJECT_STORAGE_BUCKET` | Existing bucket for SBOM payloads |
| `DFBG_OBJECT_STORAGE_ACCESS_KEY` | Access key |
| `DFBG_OBJECT_STORAGE_SECRET_KEY` | Secret key |

The browser smoke test, real-Packer lane and object-store integration tests use
[Ceph RGW's S3-compatible API](https://docs.ceph.com/en/latest/radosgw/s3/).

### Vulnerability scanning

Scanning is off unless `DFBG_SCANNER_ADAPTER` is set. The group is fail-closed:
any other `DFBG_SCANNER_*` variable set without the adapter, or an unknown
scanner variable or value, is a startup error — as is a configured adapter
without object storage, which holds the scan transcripts.

| Variable | Default | Description |
|---|---|---|
| `DFBG_SCANNER_ADAPTER` | — | `osv` is the only adapter; unset means no scanning |
| `DFBG_SCANNER_ENDPOINT` | `https://api.osv.dev` | OSV-compatible query endpoint |
| `DFBG_SCANNER_FORMAT` | `purl` | Package identity sent to the scanner; `purl` is the only value |
| `DFBG_SCANNER_REQUEST_TIMEOUT` | `30s` | Per-request timeout against the scanner endpoint |
| `DFBG_SCANNER_PASS_TIMEOUT` | `15m` | Deadline for one whole scan pass |
| `DFBG_SCANNER_INTERVAL` | `24h` | Rescan cadence per build |
| `DFBG_SCANNER_RUN_RETENTION` | `2160h` | How long scan-run records are kept (90 days) |
| `DFBG_SCANNER_WORKERS` | `2` | Concurrent scan workers per replica |
| `DFBG_SCANNER_CA_FILE` | — | Extra CA bundle for a private scanner endpoint |

### Client redirection

| Variable | Description |
|---|---|
| `HCP_API_ADDRESS` | API `host[:port]`, without a scheme |
| `HCP_AUTH_URL` | HTTPS origin for the token endpoint |
| `HCP_CLIENT_ID` / `HCP_CLIENT_SECRET` | Service-principal credentials |
| `HCP_ORGANIZATION_ID` / `HCP_PROJECT_ID` | Tenant UUIDs; set both for deterministic Packer selection |
| `HCP_API_TLS` / `HCP_AUTH_TLS` | Set to `insecure` only for disposable self-signed endpoints; authentication TLS cannot be disabled |
| `HCP_SKIP_STATUS_CHECK` | Set to `true` for a redirected local client, as exercised by the real-client lanes |

These variables also redirect `terraform-provider-hcp`. Pinning both tenant IDs
removes Packer's discovery calls, but the provider still resolves a pinned
project through the resource-manager API.
