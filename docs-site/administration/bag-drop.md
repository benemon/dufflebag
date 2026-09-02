# Bag Drop

Bag Drop is a one-way outbound mirror. It pushes a project's registry data to a
destination registry, including buckets, completed versions, builds,
artifacts, channels, and SBOMs. The local registry stays authoritative. The
destination converges toward it, and data never flows back to the local
registry.

This guide covers enabling a destination, associating buckets, and reading sync
status. The [operational contract](#operational-contract) below covers
egress, credential keys, and reconcile cadence. The
[architecture document](../components/architecture.md) covers the design.

Configuring Bag Drop requires the `maintainer` role on the project. Reading its
status requires `reader`.

## Destinations

A project has at most one Bag Drop configuration. Its adapter can be one of the
following types:

- **`hcp-packer`:** Mirrors to HCP Packer. It takes the destination HCP
  organisation and project IDs, plus a service principal client ID and secret.
  The instance needs outbound HTTPS to `auth.idp.hashicorp.com` and
  `api.cloud.hashicorp.com`.
- **`dufflebag`:** Mirrors to another dufflebag instance. It takes the
  destination's HTTPS endpoint, an optional PEM CA chain, the destination
  organisation and project UUIDs, and a service principal client ID and
  secret. The CA chain is stored as public configuration and augments system
  trust.

The adapter changes how the destination is reached. Both adapters use the same
mirror lifecycle semantics.

## The destination credential

The destination client secret is always stored sealed and is never stored in
plaintext:

- A deployment with encryption at rest uses the instance keyring. It requires
  no additional configuration.
- An unencrypted deployment requires the operator to set
  `DFBG_CREDENTIAL_KEY` to 32 random bytes. The former
  `DFBG_BAGDROP_CREDENTIAL_KEY` remains a supported alias. If both are set,
  they must be identical.

::: warning
On an unencrypted deployment without either credential key, writes that seal a
secret and verify or enable operations are refused. The refusal names the
missing variable. The console also warns that the environment key protects a
database dump but does not protect a compromised host.
:::

See [the credential key](#the-credential-key) for the
credential-protection contract.

## Configure and enable a destination

Prerequisites: The `maintainer` role on the project, the destination details,
and a destination service principal's client ID and secret.

1. Open the console's **Bag Drop** screen.

![dufflebag Bag Drop destination configuration screen](/screenshots/bag-drop.png)

2. Select an adapter. The selection controls which fields appear.

3. Enter the destination and credential details.

4. Select **Enable**.

**Enable** is the single write action. The server validates the configuration
and resolves the destination with the supplied credential. It stores the
configuration and enables the mirror only after resolution succeeds. Enabling
requires no further action.

::: info
If resolution fails, the server stores nothing. An existing configuration is
left unchanged and remains enabled if it was enabled before the attempt. Fix
the fields and enable again. There is no partial state to recover from. The
failure reports `credential_refused`, `project_not_found`, `unreachable`, or
`tls_failure`, with the underlying message.
:::

The client secret is required the first time. When editing an existing
configuration, leave it blank to keep the stored credential.

To confirm that a stored destination is still reachable without changing
anything, use the API's read-only verification -
`POST .../bagdrop/verify` repeats the same resolution enable performs and
persists nothing. The console offers enable only.

### Disable a destination

1. Select **Disable**.

Disabling stops the reconciler for the project without discarding the
configuration or bucket associations. Enabling again repeats the resolution
check.

### Delete a configuration

Prerequisites: A Bag Drop configuration that you want to retire.

1. Disable the configuration.

2. Un-associate its buckets.

3. Wait for destination-side deletion to finish.

4. Delete the configuration.

::: warning
Deleting an enabled configuration is refused with
`409 - Bag Drop is enabled; disable it first`. Deletion is also refused with
`Bag Drop destination cleanup is pending` while any association is pending
removal or has ever synced work to the destination. These guards prevent a
configuration from being deleted while it still owes the destination work.
:::

## Associate buckets

Nothing mirrors until you associate a bucket. Association is an explicit
opt-in for each bucket. The association set is the reconciler's entire
authority, and Bag Drop never changes anything outside an associated bucket.

Within an associated bucket, the local registry is authoritative:

- New completed versions, builds, artifacts, and channels converge to the
  destination on each reconcile pass.
- Objects created manually on the destination inside a mirrored bucket are
  drift and are deleted.
- Local deletions propagate. The console warns before deleting a bucket that
  Bag Drop currently mirrors.

::: warning
Un-associating a bucket deletes its destination copy completely. The
association enters `pending_removal`, and deletion is retried until it
succeeds. To preserve the remote copy, disable Bag Drop instead of
un-associating the bucket.
:::

Correlation between the two sides uses bucket names and version fingerprints,
never internal IDs. Version names can therefore differ between the source and
destination. Destination-side usage appears only in the destination's own
audit trail.

## What mirrors and what does not

Channels and their assignments mirror as pointers. Bag Drop never changes the
destination's managed `latest` channel. The destination assigns that channel
when a version completes, and refusals of direct writes are expected.

SBOMs mirror by name during the build's mirrored running window. An SBOM added
after the destination build completes is recorded as permanent drift. It does
not force a rebuild.

Version revocation state mirrors in both directions. Bag Drop pushes local
revocation schedules and messages. It restores a remotely revoked version when
its local source is active.

Destination-side revocation inheritance is owned by the destination. A
destination that applies inheritance when a parent's revocation is scheduled
can show descendants as inherited before this registry does. The steady state
converges once the parent's revocation mirrors.

## Status

The **Bag Drop** screen and `GET …/bagdrop/status` report whether the project is
configured, which adapter it uses, whether it is enabled, and the last
verification result. They also show one row per association with its state
(`active` or `pending_removal`), sync status (`pending`, `synced`, `error`, or
`removing`), last sync time, and last sync error if the most recent attempt
failed.

An `error` status means the association's most recent attempt failed. The
console displays a red label and the error text in the row. A destination that
continues refusing work therefore remains visibly in error instead of appearing
as `pending`.

The reconciler runs on a level basis. Associating or un-associating a bucket,
or enabling Bag Drop, enqueues a reconcile immediately. The regular interval
remains five minutes by default, with the same per-project backoff after a
failure. Reconciliation does not run on the serving path, so a slow or
unreachable destination cannot affect Packer builds or API reads. The
`POST …/bagdrop/reconcile` endpoint remains available for on-demand runs.

The console distinguishes work that is `queued` from work actively `syncing`.
It offers a manual **Retry now** action only while the project is backing off
after a failed pass.

Destination mutations are audit fail-closed. If no audit sink can record them,
sync pauses until the next tick while ordinary serving continues.

## Operational contract

Enabling or verifying an HCP Packer Bag Drop destination makes outbound
HTTPS requests to `auth.idp.hashicorp.com` for the client-credentials grant
and `api.cloud.hashicorp.com` for scoped reads and destination writes.
Permit egress to both hosts. A dufflebag destination instead uses its
configured HTTPS endpoint. A supplied PEM CA chain augments system trust and
is stored as public configuration data, not as a path or secret.

Once an enabled configuration has active associations, the background
reconciler creates and converges destination buckets, completed versions,
completed builds and their artifacts. Mirror semantics are complete. Drift
inside associated buckets is removed, local bucket deletions propagate, and
un-associating deletes the destination copy - a pending removal is retained
and retried until that deletion succeeds. Ordinary channels and their
assignments mirror as pointers. The managed `latest` channel is never
touched. The association set is the reconciler's entire authority to delete.
Nothing outside an associated bucket is ever touched.

SBOMs mirror by name during the build's mirrored running window. SBOMs added
after a destination build has completed surface as permanent drift rather
than causing the build to be deleted or recreated. A destination size
refusal skips that SBOM, records the refusal in the association's
`last_sync_error`, and does not prevent its remaining work or another
association from converging. The destination API can list and read SBOMs but
cannot delete one, so a remote-only SBOM is likewise recorded as
non-removable drift. Version revocation state mirrors in both directions:
local revocation schedules and messages are pushed, while a remotely revoked
version whose local source is active is restored.

`DFBG_BAGDROP_RECONCILE_INTERVAL` controls the level-reconcile cadence as a
Go duration and defaults to `5m`. An invalid or non-positive value refuses
startup. Per-project failures back off in memory at `interval * 2^failures`,
capped at one hour, and a successful run resets the count. A `Retry-After`
header, if a destination ever supplies one, is honoured when it asks for a
longer delay.

Destination mutations are audit fail-closed. If no configured audit sink can
accept the pre-mutation or outcome record, Bag Drop sync pauses until the
next cadence tick. The ordinary API and compatibility serving paths remain
available because reconciliation runs outside them.

### The credential key

The destination client secret is always stored in an AES-256-GCM envelope.
On an unencrypted deployment, set the general `DFBG_CREDENTIAL_KEY` to
exactly 32 random bytes. `DFBG_BAGDROP_CREDENTIAL_KEY` remains a supported
migration alias with the same behaviour. If both are set, they must be
identical. Different values refuse startup. Without either, ordinary reads
and deletion of a disabled existing configuration still work, but writes
that seal a secret and verify/enable operations that unseal one refuse and
name the missing variable. There is no plaintext fallback.

This environment key protects a database dump that does not also contain the
process environment. It does **not** resist compromise of the host,
container environment, or a process that can read the key. Treat it as
credential material and source it from the deployment's secret manager.

On a deployment with
[encryption at rest](../components/encryption.md), Bag Drop credentials use
the wrapped keyring instead. In that posture neither `DFBG_CREDENTIAL_KEY`
nor `DFBG_BAGDROP_CREDENTIAL_KEY` may be set. The process refuses to start
rather than accepting a second source of truth.

## Where to go next

- [The console](../components/console.md): role gates and confirmations,
  credential keys, reconcile cadence, and SBOM size refusals.
- [Platform API reference](/platform-api.html): the `bagdrop` endpoint family.
