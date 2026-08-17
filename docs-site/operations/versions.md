# Versions

A version is one publish of a bucket — created incomplete by `packer build`,
becoming active when every build completes. This page covers the versions
table, the version screen, consumption, and revocation and restore.

## The versions table

A bucket's versions table shows each version's completion and revocation
state. An incomplete version is named `v0` until it completes; a complete
version takes its sequence name and the managed `latest` channel moves to it.

## A version

A version shows its builds, artifacts, findings and ancestry. Its operations
card can promote the version to a channel, revoke it immediately or on a
schedule, restore it, or delete it.

![Dufflebag version screen showing the version operations card](/screenshots/version-operations.png)

## Consume this version

When a channel points at the version, the **Consume this version** card
renders a copyable Terraform `hcp_packer_version` + `hcp_packer_artifact`
block. The card provides the console's standing handoff to automation — see
[Manage dufflebag with Terraform](../quick-start/manage-with-terraform.md).

The card offers Terraform plus platform-appropriate commands for platforms
the version actually built. Terraform is always present and selected by
default; platforms without a confident command mapping fall back to
Terraform.

![Dufflebag consumption card showing the Terraform and platform command toggle](/screenshots/version-consume.png)

## Revoking a version

Revocation marks a version as unavailable for consumption, either immediately
or at a scheduled time, and rolls back the channels that pointed to it.

::: info
Revoking, restoring and deleting versions require the `publisher` role. When
a principal has a lower role, the console disables the action and shows the
refusal reason.
:::

The console's **Revoke** action on the version screen chooses immediate or
scheduled revocation, takes the message, and offers the two opt-outs below.
On the wire it is a `PATCH` with exactly one of an absolute time or a
relative duration:

```sh
curl -sX PATCH \
  "https://registry.example.com/packer/2023-01-01/organizations/$HCP_ORGANIZATION_ID/projects/$HCP_PROJECT_ID/buckets/app-image/versions/<fingerprint>" \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"revoke_at": "2026-09-01T00:00:00Z", "revocation_message": "CVE-2026-1234 in base image"}'
```

Use `"revoke_in": "72h"` to schedule the revocation relative to the current
time. A `d` unit is accepted and expanded to hours. (To mint `$TOKEN`,
exchange the client credentials at the instance's token endpoint:
`curl -s -u "$HCP_CLIENT_ID:$HCP_CLIENT_SECRET" -d 'grant_type=client_credentials' https://registry.example.com/oauth2/token`.)

Before the effect time, the version reads as `VERSION_REVOCATION_SCHEDULED`
and remains consumable. From the effect time, it reads as `VERSION_REVOKED`,
and modern Packer and Terraform data sources refuse to resolve it.

::: info
Two actions accompany a revocation by default:

- **Descendants inherit.** Versions built from the revoked version, as
  recorded in build ancestry, are revoked with it. They are marked
  `INHERITED` and name the ancestor. A descendant that already has its own
  revocation keeps it. Set `skip_descendants_revocation: true` to opt out.
  Inheritance also applies at record time: a new build that records an
  already-revoked parent starts as inherited-revoked.
- **Channels roll back.** Every channel pointing to the revoked version,
  including user channels and managed `latest`, is reassigned in the same
  transaction. It moves to the most recent version in that channel's
  assignment history that is not revoked. Set
  `disable_rollback_channels: true` to opt out and retain the assignments. A
  channel whose entire history is revoked is unchanged.
:::

## Restoring a version

The console's **Restore** action — a single-click recovery confirmation —
sends a `PATCH` with `restore` set to `true`:

```sh
curl -sX PATCH \
  "https://registry.example.com/packer/2023-01-01/organizations/$HCP_ORGANIZATION_ID/projects/$HCP_PROJECT_ID/buckets/app-image/versions/<fingerprint>" \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"restore": true}'
```

This clears a scheduled or effective revocation. Promote the restored version
again if it should regain a channel: restoration does not move channels
forward, and assignments remain at the targets chosen by revocation
rollback.

::: warning
- Restoring a version also restores descendants whose revocation was
  inherited from it. Descendants revoked manually or inherited from another
  ancestor remain revoked.
- A version with an inherited revocation cannot be restored directly. The
  request is refused with a message identifying the ancestor. Restore that
  ancestor instead.
- Restoring an active version is refused. Combining `restore` with
  `revoke_at` or `revoke_in` is also refused.
:::

The [compatibility reference](../reference/compatibility.md) records the wire
contract, refusal messages, and edge cases.
