# Revocation, restore and channels

Channels are named pointers into a bucket's version history: consumers resolve
a channel (most commonly `latest`) instead of hard-coding a fingerprint, and
promotion is nothing more than reassigning the pointer. Revocation marks a
version as no longer fit for consumption — immediately or at a scheduled time —
and rolls the channels that pointed at it back to their last valid target.

This guide covers the day-to-day workflows through the console and the API.
The exact wire contract, refusal messages and edge cases are recorded in the
[compatibility reference](https://github.com/benemon/dufflebag/blob/main/docs/compatibility.md);
this guide links it rather than restating it.

All the writes on this page require the `publisher` role on the project. The
console shows the refusal reason on the disabled action when the signed-in
principal's role is insufficient.

## Working with channels

A bucket starts with one channel: the managed `latest`, which dufflebag itself
reassigns whenever a version completes. It cannot be deleted or assigned
manually — those requests are refused, matching HCP Packer.

User channels are yours:

- **Create** — on the bucket's **Channels** tab, **Create channel** takes a
  name, an optional restricted flag, and an optional initial version.
- **Assign / promote** — each channel row offers **Assign version…**; a
  version's own screen offers **Promote**, which assigns that version to a
  channel you pick. Both are the same operation on the wire.
- **Delete** — from the channel row. The managed `latest` offers no delete.

Only complete, active versions are offered for assignment: an incomplete
(`v0`) or revoked version cannot be promoted. The same invariant holds on the
API.

The `restricted` flag is stored and rendered faithfully, but dufflebag does
not enforce restricted-channel visibility — every channel is readable by every
role that can read the bucket. This is a
[recorded divergence](https://github.com/benemon/dufflebag/blob/main/docs/compatibility.md)
from HCP Packer's documented behaviour.

On the wire, assignment is `UpdateChannel` with a field mask:

```sh
curl -sX PATCH \
  "https://registry.example.com/packer/2023-01-01/organizations/$HCP_ORGANIZATION_ID/projects/$HCP_PROJECT_ID/buckets/app-image/channels/production" \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"update_mask": "versionFingerprint", "version_fingerprint": "<fingerprint>"}'
```

An empty `version_fingerprint` under the same mask clears the assignment —
this is what `terraform destroy` of an `hcp_packer_channel_assignment` sends.
Channels can also be managed declaratively with `hcp_packer_channel` and
`hcp_packer_channel_assignment`; Terraform remains the recommended interface
for automation.

Bearer tokens come from the instance's own token endpoint, with the client
credentials in a Basic header:

```sh
TOKEN=$(curl -s -u "$HCP_CLIENT_ID:$HCP_CLIENT_SECRET" \
  -d 'grant_type=client_credentials' \
  https://registry.example.com/oauth2/token | jq -r .access_token)
```

## Revoking a version

Revocation is a `PATCH` on the version, with either an absolute time or a
relative duration — exactly one of the two:

```sh
curl -sX PATCH \
  "https://registry.example.com/packer/2023-01-01/organizations/$HCP_ORGANIZATION_ID/projects/$HCP_PROJECT_ID/buckets/app-image/versions/<fingerprint>" \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"revoke_at": "2026-09-01T00:00:00Z", "revocation_message": "CVE-2026-1234 in base image"}'
```

`"revoke_in": "72h"` schedules the same thing relative to now (a `d` unit is
accepted and expanded to hours). Until the effect time the version reads as
`VERSION_REVOCATION_SCHEDULED` and remains consumable; from the effect time it
reads as `VERSION_REVOKED` and the modern Packer and Terraform data sources
refuse to resolve it.

The console offers the same choice: **Revoke** on a version's screen asks
whether to revoke now or at a scheduled time, takes the message, and exposes
the two opt-outs below.

Two things happen alongside a revocation, both on by default:

- **Descendants inherit.** Versions built *from* the revoked version (recorded
  build ancestry) are revoked with it, marked `INHERITED` and naming the
  ancestor. A descendant that already carries its own revocation keeps it.
  `skip_descendants_revocation: true` opts out. Inheritance also applies at
  record time: a new build that records an already-revoked parent starts life
  inherited-revoked.
- **Channels roll back.** Every channel pointing at the revoked version — user
  channels and the managed `latest` alike — is reassigned to the most recent
  version in its own assignment history that is not revoked, in the same
  transaction. `disable_rollback_channels: true` opts out and leaves the
  assignments in place. A channel whose entire history is revoked is left
  as-is.

## Restoring a version

Restore clears a revocation, scheduled or effective:

```sh
curl -sX PATCH \
  "https://registry.example.com/packer/2023-01-01/organizations/$HCP_ORGANIZATION_ID/projects/$HCP_PROJECT_ID/buckets/app-image/versions/<fingerprint>" \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"restore": true}'
```

The console's **Restore** action on a revoked version sends the same request.

The rules to know:

- Restoring a version also restores the descendants whose revocation was
  inherited *from it*. Descendants revoked manually, or inherited from a
  different ancestor, stay revoked.
- A version whose revocation is inherited cannot be restored directly — the
  request is refused with a message pointing at the ancestor. Restore the
  ancestor instead.
- Restoring an active version is refused; so is combining `restore` with
  `revoke_at` or `revoke_in`.
- Restore does **not** forward-roll channels. Assignments rolled back by the
  revocation stay where the rollback put them; promote the restored version
  again if that is what you want.

## Where to go next

- [Compatibility reference](https://github.com/benemon/dufflebag/blob/main/docs/compatibility.md)
  — the full wire contract, including exact refusal codes and messages.
- [Getting started](./getting-started.md) — pointing Packer and Terraform at
  the instance.
