# Installation

This page gets a serving instance running, then covers the deployment detail
- hostname constraints, serving, readiness and the core configuration - on
the same page. Component-specific configuration lives with each component:
[Database](../components/database.md),
[Object storage](../components/object-storage.md),
[Encryption](../components/encryption.md) and
[Vulnerability scanning](../components/vulnerability-scanning.md).

Once the instance serves, continue with [Bootstrap](./bootstrap.md).

## Quick start

Three ways to a serving instance, from most to least self-contained.

### Kubernetes (Helm)

The chart deploys dufflebag, PostgreSQL, Ceph object storage and a community
Vault that provides the encryption keyring. It requires no external services.

```sh
helm repo add dufflebag https://benemon.github.io/dufflebag/charts
helm install dufflebag dufflebag/dufflebag \
  --namespace dufflebag --create-namespace
```

Wait until every pod is ready. The in-cluster `dufflebag` Service serves on
port 8080. `ingress.enabled` adds an Ingress for a hostname. Only the most
recently published chart version is served from the Helm repository.

::: warning
The bundled Vault stores its unseal key in a Kubernetes Secret and is not
suitable for production. Anyone who can read that Secret controls Vault, and
deleting the namespace deletes the escrowed credentials - a retained Vault
volume without its matching Secret cannot be unsealed. Production deployments
should bring their own independently operated Vault and database.
:::

The chart uses the single-role database model (the server migrates at
startup), allocates persistent volumes for PostgreSQL, Ceph and Vault, and
carries the internal database and object-storage credentials in the same
values file - override those defaults outside an isolated lab. It runs
exactly one dufflebag replica and adds no leader election or other HA
machinery. A fresh instance stays NotReady until first run claims it (see
[Serving and readiness](#serving-and-readiness)).

### OpenShift

The same chart supports OpenShift with settings for the security profile and
the Route:

```sh
helm repo add dufflebag https://benemon.github.io/dufflebag/charts
helm install dufflebag dufflebag/dufflebag \
  --namespace dufflebag --create-namespace \
  --set security.openshift=true \
  --set route.enabled=true
```

The profile keeps every pod under the restricted-v2 constraints - dufflebag,
PostgreSQL, Vault and Ceph all run at an arbitrary non-root UID - no SCC
grants, no exceptions. Installing needs a user who can create the chart's one
cluster-scoped object, the ClusterRoleBinding for Vault's token reviewer.
Nothing else reaches past the namespace. `route.host` fixes the Route's
hostname. Without it, OpenShift assigns one. On plain Kubernetes leave the
profile off: the PostgreSQL and Ceph images start as root and drop privileges
themselves, which the default profile permits with a documented capability
set. The Ceph image is pulled with `Always`. Its version tags move when the
image is rebuilt, and a node's cached copy must not trail them.

### Docker or Podman

A single container runs against a PostgreSQL database that you provide, owned
by a role that holds neither superuser nor `BYPASSRLS` - see
[Database](../components/database.md) for the role model:

```sh
docker run -d --name dufflebag -p 8443:8443 \
  -e DFBG_DATABASE_URL='postgres://dufflebag:<password>@db/dufflebag' \
  -e DFBG_HTTP_ADDR=:8443 \
  -e DFBG_TOKEN_SIGNING_KEY='<at least 32 random bytes>' \
  -e DFBG_TOKEN_ISSUER='https://registry.example.com' \
  -e DFBG_TLS_CERT_FILE=/tls/tls.crt -e DFBG_TLS_KEY_FILE=/tls/tls.key \
  -v /path/to/certs:/tls:ro \
  quay.io/benjamin_holmes/dufflebag:<tag>
```

Podman accepts the same invocation. The server creates its schema at first
boot and migrates it on upgrades. No migrate command is run. If no
S3-compatible object storage is configured, SBOM upload and download return
503 and everything else works - a reasonable way to evaluate an instance
before committing storage to it.

### After any of these

1. Check `GET /sys/health` - the readiness signal, which also reports whether
   the instance has been claimed.
2. Complete `POST /sys/init` **before** exposing the instance: whoever
   completes it first owns a fresh instance.

[Bootstrap](./bootstrap.md) picks up from there.

## The image

```
quay.io/benjamin_holmes/dufflebag:<tag>
```

Tags mirror git tags. Plain `vX.Y.Z` tags are releases and do not expire.
`-rc` tags are release candidates and are deleted from the registry
automatically after their expiry window. The image is based on Red Hat UBI
(ubi9 micro), runs as a non-root user, and serves everything from one
process: the OAuth token endpoint, the Packer registry API, the
resource-manager API, first-run initialisation and the embedded console.

## One hostname, no path prefix

Plan the ingress around two constraints that come from the Packer SDK, not
from dufflebag:

- **One hostname serves everything.** The SDK requires `HCP_AUTH_URL` to be
  HTTPS, and the auth and API path trees do not collide, so a single hostname
  and listener serve both. Nothing needs to be split.
- **A path prefix is impossible.** The SDK *assigns* `/oauth2/token` onto
  `HCP_AUTH_URL` rather than appending to it, so any path prefix in the URL
  vanishes silently and authentication fails. dufflebag must sit at the root
  of its hostname - `https://registry.example.com`, never
  `https://example.com/registry`.

TLS can terminate in a proxy or ingress in front of the process (leave
`DFBG_TLS_CERT_FILE`/`DFBG_TLS_KEY_FILE` unset and serve plaintext behind
it), or in the process itself by mounting a certificate and setting both
variables.

Behind a proxy or OpenShift Route, set `DFBG_TRUSTED_PROXIES` to the proxy or
ingress egress range, or the per-caller token throttle collapses to one
shared bucket. Never include ranges that clients can occupy.

## Serving and readiness

`GET /sys/health` is the readiness probe. It needs no credential, and it
reports whether the instance has been initialised:

```sh
curl https://registry.example.com/sys/health
```

It answers 200 only once first run has completed. An unclaimed instance
answers 501, and an instance whose every audit sink is unhealthy answers 503.
Wired as a stock Kubernetes readiness probe, a fresh pod therefore does not
become ready until first run completes - drive first run against the pod
directly, or tolerate the not-ready window.

Prometheus metrics use a separate listener configured by `DFBG_METRICS_ADDR`,
so the public hostname carries no unauthenticated operational surface. The
metrics endpoint is unauthenticated by design; scope its exposure with the
bind address or a network policy. When the variable is unset, no metrics
server is started.

On Kubernetes or OpenShift the same pieces map to: a `migrate`-subcommand
init container using the migration role's `DFBG_DATABASE_URL` (two-role
setups only - see [Database](../components/database.md)), the serving
container using the application role's, a Secret for the connection strings
and the signing key, and an Ingress or Route carrying the hostname - subject
to the no-path-prefix constraint above. The plain YAML in
[`deploy/kubernetes/`](https://github.com/benemon/dufflebag/tree/main/deploy/kubernetes)
provides reference manifests for that layout. Readiness uses `/sys/health`
and remains false while an instance is unclaimed. Liveness uses the serving
port so first run cannot cause a restart loop.

## Configuration reference

Infrastructure is configured by the deployment. dufflebag does not persist
its database or object-store credentials through the platform API. The
component-specific variable groups live with their components:
[database](../components/database.md),
[object storage](../components/object-storage.md#configuration),
[encryption at rest](../components/encryption.md#configuration) and
[vulnerability scanning](../components/vulnerability-scanning.md#configuration).

| Variable | Default | Description |
|---|---|---|
| `DFBG_DATABASE_URL` | - | PostgreSQL connection string. Required; the serving role must not be a superuser and must not hold `BYPASSRLS` |
| `DFBG_TOKEN_SIGNING_KEY` | - | Token-signing key. Required and at least 32 bytes - unless encryption at rest is configured, in which case it must NOT be set (the key lives in the wrapped keyring) |
| `DFBG_TOKEN_ISSUER` | `https://dufflebag.local` | Issuer recorded in access tokens |
| `DFBG_HTTP_ADDR` | `:8080` | HTTP listen address |
| `DFBG_METRICS_ADDR` | - | Prometheus metrics listener; unset means no metrics server; bind to an internal address - the endpoint is unauthenticated by design |
| `DFBG_TLS_CERT_FILE` / `DFBG_TLS_KEY_FILE` | - | Built-in TLS certificate and key; set both or neither |
| `DFBG_TRUSTED_PROXIES` | - | Trusted reverse-proxy CIDRs. When the peer is trusted, per-caller admission keys on the rightmost untrusted `X-Forwarded-For` entry; unset means every caller keys on its own peer address |
| `DFBG_API_MAX_BODY_BYTES` | `16777216` | Maximum JSON request body on the 2023 compatibility surface |
| `DFBG_SHUTDOWN_GRACE_PERIOD` | `10s` | Shared deadline for HTTP and audit shutdown |
| `DFBG_AUDIT_HMAC_KEY` / `DFBG_AUDIT_HMAC_KEY_VERSION` | - | Required together when an audit target is configured; must NOT be set when encryption at rest is configured |

### Client redirection

| Variable | Description |
|---|---|
| `HCP_API_ADDRESS` | API `host[:port]`, without a scheme |
| `HCP_AUTH_URL` | HTTPS origin for the token endpoint |
| `HCP_CLIENT_ID` / `HCP_CLIENT_SECRET` | Service-principal credentials |
| `HCP_ORGANIZATION_ID` / `HCP_PROJECT_ID` | Tenant UUIDs; set both for deterministic Packer selection |
| `HCP_API_TLS` / `HCP_AUTH_TLS` | Set to `insecure` only for disposable self-signed endpoints; authentication TLS cannot be disabled |
| `HCP_SKIP_STATUS_CHECK` | Set to `true` for a redirected local client, as exercised by the real-client lanes |

These variables also redirect `terraform-provider-hcp`. Pinning both tenant
IDs removes Packer's discovery calls, but the provider still resolves a
pinned project through the resource-manager API. The console's
[Instance screen](../administration/instance.md) generates this block for a
selected tenancy.

## Next

[Bootstrap](./bootstrap.md) - initialize the instance, create an
organisation and project, and mint a builder credential.
