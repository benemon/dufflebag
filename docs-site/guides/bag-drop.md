# Bag Drop

Bag Drop is a one-way outbound mirror: it pushes a project's registry data —
buckets, completed versions, builds, artifacts, channels, SBOMs — to a
destination registry. The local registry stays authoritative; the destination
converges toward it. Nothing ever flows back in.

This guide covers the workflow: configuring a destination, verifying it,
enabling the mirror, associating buckets, and reading sync status. The
[deployment guide](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md#bag-drop)
covers the operator concerns (egress, credential keys, reconcile cadence), and
the [architecture document](https://github.com/benemon/dufflebag/blob/main/docs/architecture.md)
covers the design.

Configuring Bag Drop requires the `maintainer` role on the project; reading
its status requires `reader`.

## Destinations

A project has at most one Bag Drop configuration, typed by adapter:

- **`hcp-packer`** — mirrors to real HCP Packer. Takes the destination HCP
  organisation and project IDs plus a service principal client ID and secret.
  The instance needs outbound HTTPS to `auth.idp.hashicorp.com` and
  `api.cloud.hashicorp.com`.
- **`dufflebag`** — mirrors to another dufflebag instance. Takes the
  destination's HTTPS endpoint, an optional PEM CA chain (stored as public
  configuration; it augments system trust), the destination organisation and
  project UUIDs, and a service principal client ID and secret.

The adapter only changes how the destination is reached. The mirror lifecycle
semantics are identical on both.

## The destination credential

The destination client secret is always stored sealed, never in plaintext:

- On a deployment with encryption at rest, it is sealed with the instance
  keyring — nothing to configure.
- On an unencrypted deployment, the operator must set
  `DFBG_CREDENTIAL_KEY` (32 random bytes). The former
  `DFBG_BAGDROP_CREDENTIAL_KEY` remains a supported alias; if both are set they
  must be identical. Without either, writes that seal a secret and
  verify/enable operations refuse and name the missing variable. The console
  shows a standing warning on this posture: the environment key protects a
  database dump, not a compromised host.

See the
[deployment guide](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md#bag-drop)
for the full credential-protection contract.

## Configure, verify, enable

The console's **Bag Drop** screen drives the whole flow. Selecting the adapter
gates which fields appear; saving stores the configuration disabled.

**Verify** is a read-only resolution check: dufflebag authenticates against
the destination with the stored credential and confirms the configured project
resolves. The result is `resolved`, or `failed` with a reason —
`credential_refused`, `project_not_found`, `unreachable`, or `tls_failure` —
and the underlying message. Verification changes nothing on either side.

**Enable** re-runs exactly the same resolution check server-side and only
turns the mirror on if it passes. **Disable** stops the reconciler for the
project without discarding configuration or associations.

Deleting the configuration is guarded twice: while enabled it is refused
(`409 — Bag Drop is enabled; disable it first`), and it stays refused with
`Bag Drop destination cleanup is pending` while any association is pending
removal or has ever synced work to the destination. To retire a
configuration cleanly: un-associate the buckets, let the destination-side
deletions complete, then delete. The guards exist so a configuration cannot
vanish while it still owes the destination work.

## Associating buckets

Nothing mirrors until you associate a bucket — association is an explicit
opt-in per bucket, and the association set is the reconciler's *entire*
authority. Nothing outside an associated bucket is ever touched on the
destination.

Within an associated bucket, the local registry is authoritative:

- New completed versions, builds, artifacts and channels converge to the
  destination on each reconcile pass.
- Drift is removed — objects created by hand on the destination inside a
  mirrored bucket are deleted.
- Local deletions propagate. The console warns before deleting a bucket that
  Bag Drop currently mirrors.
- **Un-associating deletes the destination copy completely.** The association
  enters `pending_removal` and is retried until the destination-side deletion
  succeeds. If you want the remote copy to survive, don't un-associate —
  disable instead.

Two honest limits: correlation between the two sides uses bucket names and
version fingerprints, never internal IDs, so version *names* may diverge
between source and destination; and destination-side usage appears only in the
destination's own audit trail.

## What mirrors, and what doesn't

Channels and their assignments mirror as pointers. The destination's managed
`latest` channel is never touched — the destination assigns it itself on
version completion, and its refusals of direct writes are expected, not
errors. SBOMs mirror by name during the build's mirrored running window; an
SBOM added after the destination build completed is recorded as permanent
drift rather than forcing a rebuild. Version revocation state mirrors in both
directions: local revocation schedules and messages are pushed, and a remotely
revoked version whose local source is active is restored.

## Status

The Bag Drop screen (and `GET …/bagdrop/status`) reports whether the project
is configured, which adapter, whether it is enabled, the last verification
result, and one row per association: its state (`active` or
`pending_removal`), its sync status (`pending`, `synced`, or `removing`),
when it last synced, and the last sync error if the most recent attempt
failed.

The reconciler runs on a level basis (default every five minutes, with
per-project backoff on failure) and never sits on the serving path — a slow or
unreachable destination cannot affect Packer builds or API reads. A reconcile
can also be triggered on demand via `POST …/bagdrop/reconcile`. Destination
mutations are audit fail-closed: if no audit sink can record them, sync pauses
until the next tick while ordinary serving continues.

## Where to go next

- [Deployment guide — Bag Drop](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md#bag-drop)
  — egress, credential keys, reconcile cadence, SBOM size refusals.
- [Platform API reference](/platform-api.html) — the full `bagdrop` endpoint
  family.
