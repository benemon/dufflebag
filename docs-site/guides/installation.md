# Installation

Three ways to get a running instance, from most to least self-contained. Each
quickstart here gets you to a serving instance; the
[deployment reference](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md)
is the authority on every variable, the PostgreSQL role model, TLS, object
storage and backups. Once the instance serves, continue with
[getting started](./getting-started.md).

## Kubernetes (Helm)

The chart deploys the whole stack — dufflebag, PostgreSQL, Ceph object storage
and a community Vault providing the encryption keyring — with nothing external
required:

```sh
helm repo add dufflebag https://benemon.github.io/dufflebag/charts
helm install dufflebag dufflebag/dufflebag \
  --namespace dufflebag --create-namespace
```

When every pod is ready, the in-cluster `dufflebag` Service serves on port
8080; `ingress.enabled` adds an Ingress if you want a hostname. The bundled
Vault's lifecycle is deliberately lab-grade — its unseal key lives in a
namespace Secret — so production deployments should bring their own Vault and
database instead. The
[Helm section of the deployment reference](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md#helm)
covers the values, the trust model and the single-replica constraint.

## OpenShift

The same chart, two settings: the security profile and the Route.

```sh
helm repo add dufflebag https://benemon.github.io/dufflebag/charts
helm install dufflebag dufflebag/dufflebag \
  --namespace dufflebag --create-namespace \
  --set security.openshift=true \
  --set route.enabled=true
```

The profile keeps every pod under the restricted-v2 constraints except the
Ceph image, which must run as root and is pinned to the `anyuid` SCC.
Installing needs a user who can grant SCC use and create the chart's one
ClusterRoleBinding — in practice a cluster administrator. `route.host` fixes
the Route's hostname; without it OpenShift assigns one.

## Docker or Podman

A single container against a PostgreSQL you provide. Create the two database
roles first — a migration owner and a serving role that must hold neither
superuser nor `BYPASSRLS` — exactly as the
[PostgreSQL section](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md#postgresql-two-roles)
specifies. Then migrate and serve with the same image:

```sh
docker run --rm \
  -e DFBG_DATABASE_URL='postgres://dufflebag_migrate:<migration password>@db/dufflebag' \
  quay.io/benjamin_holmes/dufflebag:<tag> migrate

docker run -d --name dufflebag -p 8443:8443 \
  -e DFBG_DATABASE_URL='postgres://dufflebag_app:<serving password>@db/dufflebag' \
  -e DFBG_HTTP_ADDR=:8443 \
  -e DFBG_TOKEN_SIGNING_KEY='<at least 32 random bytes>' \
  -e DFBG_TOKEN_ISSUER='https://registry.example.com' \
  -e DFBG_TLS_CERT_FILE=/tls/tls.crt -e DFBG_TLS_KEY_FILE=/tls/tls.key \
  -v /path/to/certs:/tls:ro \
  quay.io/benjamin_holmes/dufflebag:<tag>
```

Podman accepts the same invocations. Two constraints worth knowing before you
choose a hostname: the instance must sit at the root of it (the Packer SDK
silently discards any path prefix), and the auth URL must be HTTPS — one
TLS-serving hostname covers everything. Without S3-compatible object storage
configured, everything serves except SBOM upload and download, which answer
503 — a reasonable way to evaluate before committing storage.

## After any of these

`GET /sys/health` is the readiness signal and reports whether the instance has
been claimed. A fresh instance is owned by whoever completes `POST /sys/init`
first — do that before exposing it, in the browser through the first-run
wizard or with one `curl`. [Getting started](./getting-started.md) picks up
from there.
