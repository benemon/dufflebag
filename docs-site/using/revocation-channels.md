# Revocation, restore and channels

Channels are named pointers into a bucket's version history. Consumers resolve
a channel, usually `latest`, instead of hard-coding a fingerprint. Promotion
reassigns the pointer. Revocation marks a version as unavailable for
consumption, either immediately or at a scheduled time, and rolls back the
channels that pointed to it.

This guide covers the console and API workflows. The
[compatibility reference](../reference/compatibility.md) records the wire
contract, refusal messages, and edge cases.

::: info
Every write on this page requires the `publisher` role on the project. When a
principal has a lower role, the console disables the action and shows the
refusal reason.
:::

## Working with channels

A bucket starts with the managed `latest` channel. Dufflebag reassigns it when
a version completes. Requests to delete or assign `latest` are refused,
matching HCP Packer.

Prerequisites: The `publisher` role and a bucket. Assigning a channel also
requires a complete, active version.

1. Create a user channel from **Create channel** on the bucket's **Channels**
   tab. Provide a name and, optionally, the restricted flag and an initial
   version.

2. Assign a version from **Assign version…** on the channel row. To promote
   from a version's screen, select **Promote** and choose a channel. Both paths
   send the same wire operation.

3. Delete a user channel from its channel row. The managed `latest` channel
   has no delete action.

Only complete, active versions are offered for assignment. An incomplete
(`v0`) or revoked version cannot be promoted. The API enforces the same rule.

::: warning
The `restricted` flag is stored, returned, and shown in the console, but
dufflebag does not enforce restricted-channel visibility. Every role that can
read a bucket can read each of its channels. The
[compatibility reference](../reference/compatibility.md) records this
divergence from HCP Packer's documented behavior.
:::

On the wire, assignment is `UpdateChannel` with a field mask:

```sh
curl -sX PATCH \
  "https://registry.example.com/packer/2023-01-01/organizations/$HCP_ORGANIZATION_ID/projects/$HCP_PROJECT_ID/buckets/app-image/channels/production" \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"update_mask": "versionFingerprint", "version_fingerprint": "<fingerprint>"}'
```

An empty `version_fingerprint` with the same mask clears the assignment. This
is the request sent by `terraform destroy` for an
`hcp_packer_channel_assignment`. The `hcp_packer_channel` and
`hcp_packer_channel_assignment` resources manage channels declaratively. Use
Terraform for automated channel management.

## Get a bearer token

Prerequisites: A service principal's client ID and secret.

1. Exchange the client credentials at the instance's token endpoint. The
   request sends the credentials in a Basic authorization header:

```sh
TOKEN=$(curl -s -u "$HCP_CLIENT_ID:$HCP_CLIENT_SECRET" \
  -d 'grant_type=client_credentials' \
  https://registry.example.com/oauth2/token | jq -r .access_token)
```

## Revoking a version

Prerequisites: The `publisher` role, a bearer token, and the fingerprint of the
version to revoke.

1. Choose either an absolute time or a relative duration. Send exactly one of
   the two in a `PATCH` request. The following request uses an absolute time:

```sh
curl -sX PATCH \
  "https://registry.example.com/packer/2023-01-01/organizations/$HCP_ORGANIZATION_ID/projects/$HCP_PROJECT_ID/buckets/app-image/versions/<fingerprint>" \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"revoke_at": "2026-09-01T00:00:00Z", "revocation_message": "CVE-2026-1234 in base image"}'
```

   Use `"revoke_in": "72h"` to schedule the revocation relative to the current
   time. A `d` unit is accepted and expanded to hours.

2. Check the version state. Before the effect time, it reads as
   `VERSION_REVOCATION_SCHEDULED` and remains consumable. From the effect time,
   it reads as `VERSION_REVOKED`, and modern Packer and Terraform data sources
   refuse to resolve it.

The console performs the same operation. Select **Revoke** on the version's
screen, choose immediate or scheduled revocation, enter the message, and
choose whether to use the two opt-outs below.

::: info
Two actions accompany a revocation by default:

- **Descendants inherit.** Versions built from the revoked version, as recorded
  in build ancestry, are revoked with it. They are marked `INHERITED` and name
  the ancestor. A descendant that already has its own revocation keeps it.
  Set `skip_descendants_revocation: true` to opt out. Inheritance also applies
  at record time: a new build that records an already-revoked parent starts as
  inherited-revoked.
- **Channels roll back.** Every channel pointing to the revoked version,
  including user channels and managed `latest`, is reassigned in the same
  transaction. It moves to the most recent version in that channel's
  assignment history that is not revoked. Set
  `disable_rollback_channels: true` to opt out and retain the assignments. A
  channel whose entire history is revoked is unchanged.
:::

## Restoring a version

Prerequisites: The `publisher` role, a bearer token, and a version with a
scheduled or effective revocation.

1. Send a `PATCH` request with `restore` set to `true`:

```sh
curl -sX PATCH \
  "https://registry.example.com/packer/2023-01-01/organizations/$HCP_ORGANIZATION_ID/projects/$HCP_PROJECT_ID/buckets/app-image/versions/<fingerprint>" \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"restore": true}'
```

   This clears a scheduled or effective revocation. The console's **Restore**
   action sends the same request.

2. Promote the restored version again if it should regain a channel.
   Restoration does not move channels forward. Assignments remain at the
   targets chosen by revocation rollback.

::: warning
- Restoring a version also restores descendants whose revocation was inherited
  from it. Descendants revoked manually or inherited from another ancestor
  remain revoked.
- A version with an inherited revocation cannot be restored directly. The
  request is refused with a message identifying the ancestor. Restore that
  ancestor instead.
- Restoring an active version is refused. Combining `restore` with `revoke_at`
  or `revoke_in` is also refused.
:::

## Where to go next

- [Compatibility reference](../reference/compatibility.md): the wire contract,
  including refusal codes and messages.
- [Getting started](../getting-started/first-use.md): pointing Packer and
  Terraform at the instance.
