# Installation

This guide provides three ways to get a running instance, from most to least
self-contained. Each quickstart gets you to a serving instance. The
[deployment reference](../deployment/index.md)
is the authority on every variable, the PostgreSQL role model, TLS, object
storage and backups. Once the instance serves, continue with
[getting started](./first-use.md).

## Kubernetes (Helm)

The chart deploys dufflebag, PostgreSQL, Ceph object storage and a community
Vault that provides the encryption keyring. It requires no external services.

1. Add the Helm repository and install the chart:

```sh
helm repo add dufflebag https://benemon.github.io/dufflebag/charts
helm install dufflebag dufflebag/dufflebag \
  --namespace dufflebag --create-namespace
```

2. Wait until every pod is ready. The in-cluster `dufflebag` Service serves on
   port 8080. Set `ingress.enabled` to add an Ingress for a hostname.

::: warning
The bundled Vault stores its unseal key in a Kubernetes Secret and is not suitable for production. Production deployments should use an external Vault and database.
:::

The
[Helm section of the deployment reference](../deployment/index.md#helm)
covers the values, the trust model and the single-replica constraint.

## OpenShift

The same chart supports OpenShift with settings for the security profile and
the Route.

Prerequisites: A user who can grant SCC use and create the chart's one
ClusterRoleBinding. In practice, this requires a cluster administrator.

1. Add the Helm repository and install the chart with the OpenShift settings:

```sh
helm repo add dufflebag https://benemon.github.io/dufflebag/charts
helm install dufflebag dufflebag/dufflebag \
  --namespace dufflebag --create-namespace \
  --set security.openshift=true \
  --set route.enabled=true
```

2. Wait until every pod is ready. The profile keeps every pod under the
   restricted-v2 constraints except the Ceph image. The Ceph image must run as
   root and is pinned to the `anyuid` SCC.

`route.host` fixes the Route's hostname. Without it, OpenShift assigns one.

## Docker or Podman

A single container runs against a PostgreSQL instance that you provide.

Prerequisites: A PostgreSQL database owned by a role that holds neither
superuser nor `BYPASSRLS`, created as the
[PostgreSQL section](../deployment/index.md#postgresql) shows. The server
creates its schema at first boot and migrates it on upgrades; no migrate
command is run. To keep schema privileges out of the serving process instead,
use the [hardened two-role setup](../deployment/index.md#hardened-two-roles).

1. Start the container:

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

   Podman accepts the same invocation.

2. Serve the instance at the root of the hostname. The Packer SDK silently
   discards any path prefix.

3. Use HTTPS for the authentication URL. One TLS-serving hostname covers
   everything.

If no S3-compatible object storage is configured, SBOM upload and download return 503. All other features work normally, so you can evaluate an instance before configuring storage.

## After any of these

1. Check `GET /sys/health`. It is the readiness signal and reports whether the
   instance has been claimed.

2. Complete `POST /sys/init` before exposing the instance. Whoever completes
   it first owns a fresh instance. Use the browser's first-run wizard or one
   `curl` request.

[Getting started](./first-use.md) picks up from there.
