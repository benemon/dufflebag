# dufflebag

> **Independent community project.** dufflebag is not maintained, supported or
> endorsed by IBM or HashiCorp. HCP and Packer are their products; dufflebag
> implements a client-observed API contract and is not affiliated with either
> company.

*Chuck it in the bag.*

dufflebag is a self-hosted registry for build metadata produced by
[Packer](https://developer.hashicorp.com/packer/docs). It reimplements the API
contract used by Packer's
[HCP registry integration](https://developer.hashicorp.com/packer/docs/hcp),
including client quirks that are absent from the published specification, so
the stock community `packer` binary can publish to an endpoint you run instead
of `api.hashicorp.cloud`.

The same endpoint supports the Packer resources and data sources in
[`terraform-provider-hcp`](https://registry.terraform.io/providers/hashicorp/hcp/latest/docs).
An embedded [PatternFly](https://www.patternfly.org/) console provides first-run
initialisation, registry browsing, service-principal management, audit
configuration, encryption and key-rotation status, and vulnerability findings
when a scanner is configured.

## Architectural boundaries

**Two API planes.** The frozen compatibility plane serves the externally owned
auth, resource-manager and registry contracts. Its wire models are generated
from vendored Swagger specifications; handlers preserve client-observed
behaviour, including its accidents. The platform plane is dufflebag's own
OpenAPI contract for tenancy, identity, audit and console sessions. See
[docs/architecture.md](docs/architecture.md) and the
[compatibility reference](docs/compatibility.md).

**Metadata, not machine images.** dufflebag records buckets, versions, builds,
channels, artefact identifiers and client-reported SBOMs. It does not store or
scan the machine images Packer creates. When a scanner adapter is configured it
checks the client-reported SBOM package inventory against an external
vulnerability service; it never certifies that an SBOM describes the image it
was reported against.

**Existing clients are the interfaces.** There is no dufflebag client CLI; the
server binary's only subcommand is `migrate`. Packer
publishes build metadata, Terraform manages supported registry resources, and
the console covers bootstrap and operational workflows. The supported
Terraform surface is recorded in the
[compatibility reference](docs/compatibility.md).

## Prerequisites

To run dufflebag:

- PostgreSQL, with separate migration and unprivileged serving roles; and
- a TLS certificate trusted by clients, or TLS termination in front of the
  process — the Packer SDK requires an HTTPS authentication URL.

### Optional backing services

dufflebag starts and serves without any of these, but each is the
prerequisite for a feature worth having:

- **An S3-compatible object store** enables SBOM storage. Without one, builds
  publish normally and only SBOM upload and download answer 503 — a
  reasonable way to evaluate dufflebag before committing storage to it.
- **A key service — HashiCorp Vault with the transit engine** enables
  [encryption at rest](docs/deployment.md#encryption-at-rest-optional-decided-at-first-boot):
  payloads and SBOM bytes encrypted, provenance and identity rows
  tamper-evident, and the signing and audit keys held in a wrapped keyring
  instead of the environment. Vault Enterprise namespaces are supported.
  Decide before the **first** boot: encryption is a one-way door stamped at
  first run, and it cannot be enabled — or removed — afterwards.
- **An external vulnerability service — OSV.dev or an API-compatible endpoint**
  enables [SBOM scanning](docs/deployment.md#vulnerability-scanning-optional).
  Scanning requires object storage to be configured, and the default endpoint
  is the live `api.osv.dev`, so package names and versions from scanned SBOMs
  leave the deployment. Neither SBOM documents nor raw scanner reports are
  sent.

The server is only as available as these choices imply: the object store is
consulted per SBOM operation, while the key service is a startup and rotation
dependency only — never on the write path. The scanner runs asynchronously and
its availability never affects serving.

Development uses Go 1.26.4 and npm; the local integration,
browser and Packer lanes also require Docker. The tested Packer lane uses stock
Packer 1.16.0, while the compatibility scope starts at Packer 1.15.4. The
Terraform lane pins Terraform 1.14.7 and `terraform-provider-hcp` 0.112.0; the
provider compatibility floor is 0.84.0.

## Getting started

To deploy from the published container image — PostgreSQL roles, migrations,
TLS, object storage and first run — follow [Deploying dufflebag](docs/deployment.md).

For a from-source local instance: create the two PostgreSQL roles exactly as
the deployment guide shows, then

```sh
make server SERVER_BIN=./dufflebag
DFBG_DATABASE_URL='postgres://dufflebag_migrate:...@127.0.0.1:5432/dufflebag?sslmode=disable' ./dufflebag migrate
DFBG_DATABASE_URL='postgres://dufflebag_app:...@127.0.0.1:5432/dufflebag?sslmode=disable' \
DFBG_HTTP_ADDR=:8443 DFBG_TOKEN_SIGNING_KEY="$(openssl rand -hex 32)" \
DFBG_TOKEN_ISSUER=https://localhost:8443 \
DFBG_TLS_CERT_FILE=tls/tls.crt DFBG_TLS_KEY_FILE=tls/tls.key ./dufflebag
```

with a certificate of your own (self-signed is fine locally — pair it with the
`insecure` client switches below). Open the console, complete first run, and
store the credentials it shows you: they appear exactly once.

Once an instance is running, the stock binary needs only environment variables
and an ordinary Packer template.

Set the client environment. `HCP_API_ADDRESS` is `host[:port]` without a scheme;
`HCP_AUTH_URL` must be HTTPS. Use a client ID that has never been used against
HCP because the SDK token cache is keyed by client ID and geography.

```sh
export HCP_API_ADDRESS=dufflebag.example.com
export HCP_AUTH_URL=https://dufflebag.example.com
export HCP_SKIP_STATUS_CHECK=true
export HCP_CLIENT_ID='<project-scoped principal client ID>'
export HCP_CLIENT_SECRET='<principal secret>'
export HCP_ORGANIZATION_ID='<organisation UUID>'
export HCP_PROJECT_ID='<project UUID>'
```

For a disposable endpoint with a self-signed certificate, also set
`HCP_API_TLS=insecure` and `HCP_AUTH_TLS=insecure`. Do not use those switches
with a trusted deployment.

With Docker available, save this as `example.pkr.hcl`:

```hcl
packer {
  required_plugins {
    docker = {
      source  = "github.com/hashicorp/docker"
      version = "= 1.1.4"
    }
  }
}

hcp_packer_registry {
  bucket_name = "dufflebag-example"
}

source "docker" "example" {
  image  = "alpine:3.20"
  pull   = true
  commit = true
}

build {
  sources = ["source.docker.example"]
}
```

Run the unmodified client:

```sh
packer init example.pkr.hcl
packer build example.pkr.hcl
```

The completed version appears in the `dufflebag-example` bucket. Adding an
`hcp-sbom` provisioner requires object storage; `make test-packer` exercises
that path with the stock binary and Ceph.

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
see the [deployment guide](docs/deployment.md#encryption-at-rest-optional-decided-at-first-boot)
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

## Storage and security

**Tenant isolation.** PostgreSQL
[row-level security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)
enforces isolation for registry data. The process refuses to serve through a
superuser or `BYPASSRLS` role. `make test-rls-sabotage` proves the isolation
test fails when policies are dropped or disabled. See
[docs/architecture.md](docs/architecture.md).

**Audit.** When audit is enabled, a failed request-record write prevents the
operation from running. Response records are written after response bytes, so a
late failure is exposed through audit health rather than rewriting a committed
response. The exact contract and availability trade are in
[docs/architecture.md](docs/architecture.md).

**SBOM custody.** Compressed SBOM payloads live only in object storage;
PostgreSQL holds object keys and the client-reported package projection. An
unconfigured instance returns 503 for SBOM uploads while continuing to serve
other operations. Downloads pass through dufflebag rather than a presigned
object-store URL so the transfer can be audited. See
[docs/architecture.md](docs/architecture.md).

## Development and testing

```sh
make build              # web console and all Go packages
make test               # Go and web unit tests
make test-integration   # PostgreSQL integration tests via testcontainers
make test-contract      # generated hcp-sdk-go client against a running server
make test-e2e-terraform # real Terraform CLI and provider against a live stack
make test-smoke         # real browser and console against PostgreSQL and Ceph
make test-packer        # stock Packer and hcp-sbom against PostgreSQL and Ceph
make test-scanner       # scanner against recorded fixtures on a network with no egress
make test-rls-sabotage  # prove the tenant-isolation alarm fires
make lint               # go vet and golangci-lint
```

`make test-packer` as shown drives the lab CA, hostname certificate and DNS
entry, so that invocation is local-only; CI runs the same gate as
`make test-packer-ci` (and `-encrypted`) against a CA minted inside the run.
Run `make help` for generation, schema-compatibility, demo and other targets.

## Clean-room development

dufflebag is a clean-room reimplementation. The closed server implementation
has not been read. Compatibility evidence comes from public specifications and
documentation, open client and SDK source, and observed traffic from stock
clients.

The working rule is equally important: use `hcp-sdk-go` directly where
appropriate, but never copy source from `packer/internal/hcp`. Record the
contract that client code implies and cite it. The full position and working
rules are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

dufflebag is licensed under the [Mozilla Public License 2.0](LICENSE).
