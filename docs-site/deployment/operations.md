# Operations

## Vulnerability scanning (optional)

Scanning is absent unless `DFBG_SCANNER_ADAPTER=osv` is configured. An
unconfigured or disabled scanner does not produce an empty result, and the
console never describes that state as clean. The configuration group is
fail-closed: any other `DFBG_SCANNER_*` variable set while the adapter is
unset — or an unknown scanner variable or value — refuses startup, as does a
configured adapter without [object storage](./object-storage.md), which holds the
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
[architecture document](../reference/architecture.md#bag-drop-the-outbound-mirror) covers
the design. Operationally:

Enabling or verifying an HCP Packer Bag Drop destination makes outbound
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
an unencrypted deployment, set the general `DFBG_CREDENTIAL_KEY` to exactly 32
random bytes. `DFBG_BAGDROP_CREDENTIAL_KEY` remains a supported migration
alias with the same behaviour it had before webhooks existed. If both are set,
they must be identical; different values refuse startup. Without either, ordinary reads and deletion of a disabled existing
configuration still work, but writes that seal a secret and verify/enable
operations that unseal one refuse and name the missing variable. There is no
plaintext fallback.

This environment key protects a database dump that does not also contain the
process environment. It does **not** resist compromise of the host, container
environment, or a process that can read the key. Treat it as credential
material and source it from the deployment's secret manager.

On a deployment with [encryption at rest](./encryption-setup.md#encryption-at-rest-optional-decided-at-first-boot),
Bag Drop credentials use the wrapped keyring instead. In that posture neither
`DFBG_CREDENTIAL_KEY` nor `DFBG_BAGDROP_CREDENTIAL_KEY` may be set; the process refuses to start
rather than accepting a second source of truth.

## Webhooks

Webhooks make outbound HTTP requests to project-configured URLs. By default
dufflebag resolves the target and refuses loopback, link-local (including the
cloud metadata address `169.254.169.254`), RFC1918, unspecified, and multicast
addresses. The checked address is the address dialled, which prevents a second
DNS lookup from rebinding the connection after admission. Redirects are never
followed, requests time out after ten seconds, and response reads are capped at
64 KiB; only a bounded snippet is retained in the last-100 delivery history.

`DFBG_WEBHOOK_ALLOW_PRIVATE=true` disables the private/local address refusal
for isolated labs whose receiver deliberately lives on a private network. It
defaults to `false`; do not enable it on a deployment where project maintainers
must not reach internal services. A refused address is recorded once as a
refused delivery and is not retried.

Signing secrets are write-only and use the same credential protection as Bag
Drop: the wrapped keyring on encrypted deployments, or the 32-byte
`DFBG_CREDENTIAL_KEY` on unencrypted deployments. The legacy
`DFBG_BAGDROP_CREDENTIAL_KEY` alias can supply this general key during
migration, including for webhook secrets. If the general key and alias are both
set, they must be byte-for-byte identical. Encrypted deployments refuse either
environment variable.

Delivery runs outside the serving path. A failed request is retried at most
five times with exponential backoff over roughly fifteen minutes, then marked
failed and dropped. The transactional outbox means a domain write and its event
commit or roll back together; endpoint latency and failure never delay that
write.

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
[`deploy/kubernetes/`](https://github.com/benemon/dufflebag/tree/main/deploy/kubernetes) provides reference manifests for
that layout. Readiness uses `/sys/health` and remains false while an instance is
unclaimed; liveness uses the serving port so first run cannot cause a restart
loop.
