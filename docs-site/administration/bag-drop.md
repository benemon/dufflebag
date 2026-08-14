# Bag Drop

Bag Drop is a one-way outbound mirror. It pushes a project's registry data to a
destination registry, including buckets, completed versions, builds,
artifacts, channels, and SBOMs. The local registry stays authoritative. The
destination converges toward it, and data never flows back to the local
registry.

This guide covers enabling a destination, associating buckets, and reading sync
status. The [deployment guide](../deployment/operations.md#bag-drop) covers
egress, credential keys, and reconcile cadence. The
[architecture document](../reference/architecture.md) covers the design.

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

See the [deployment guide](../deployment/operations.md#bag-drop) for the
credential-protection contract.

## Configure and enable a destination

Prerequisites: The `maintainer` role on the project, the destination details,
and a destination service principal's client ID and secret.

1. Open the console's **Bag Drop** screen.

![Dufflebag Bag Drop destination configuration screen](/screenshots/bag-drop.png)

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
anything, use the API's read-only verification —
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
`409 — Bag Drop is enabled; disable it first`. Deletion is also refused with
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

The reconciler runs on a level basis. The default interval is five minutes,
with per-project backoff after a failure. It does not run on the serving path,
so a slow or unreachable destination cannot affect Packer builds or API reads.
Use `POST …/bagdrop/reconcile` to trigger a reconcile on demand.

Destination mutations are audit fail-closed. If no audit sink can record them,
sync pauses until the next tick while ordinary serving continues.

## Where to go next

- [Deployment guide: Bag Drop](../deployment/operations.md#bag-drop): egress,
  credential keys, reconcile cadence, and SBOM size refusals.
- [Platform API reference](/platform-api.html): the `bagdrop` endpoint family.
