# Channels

Channels are named pointers into a bucket's version history. Consumers
resolve a channel, usually `latest`, instead of hard-coding a fingerprint.
Promotion reassigns the pointer. This page covers the channels tab and the
channel semantics — including restriction and what revocation does to
assignments.

::: info
Managing unrestricted channels requires the `publisher` role. Creating or
managing a **restricted** channel requires `maintainer`. When a principal has
a lower role, the console disables the action and shows the refusal reason.
:::

## Working with channels

A bucket starts with the managed `latest` channel. Dufflebag reassigns it
when a version completes. Requests to delete or assign `latest` are refused,
matching HCP Packer.

![Dufflebag channels tab showing the managed latest and a restricted channel](/screenshots/channels.png)

Prerequisites: The `publisher` role and a bucket; use `maintainer` when the
channel is restricted. Assigning a channel also requires a complete, active
version.

1. Create a user channel from **Create channel** on the bucket's **Channels**
   tab. Provide a name and, optionally, the restricted flag and an initial
   version.

2. Assign a version from **Assign version…** on the channel row. To promote
   from a version's screen, select **Promote** and choose a channel. Both
   paths send the same wire operation.

3. Delete a user channel from its channel row. The managed `latest` channel
   has no delete action.

Only complete, active versions are offered for assignment. An incomplete
(`v0`) or revoked version cannot be promoted. The API enforces the same
rule.

## Restricted channels

Restricted channels are hidden from readers: they are omitted from channel
lists, and resolving one returns the same not-found response as an absent
channel. Builders and above may see and consume them. Creating a restricted
channel, changing its restriction flag in either direction, or assigning,
updating or deleting it requires `maintainer`. The managed `latest` channel
is also restricted and hidden from readers; its existing managed-channel
refusals still prevent every role from managing it through these operations.

::: tip
Because readers cannot resolve `latest`, create an unrestricted channel —
`release`, say — as the reader-consumable pointer.
:::

## Assignment on the wire

Assignment is `UpdateChannel` with a field mask:

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
`hcp_packer_channel_assignment` resources manage channels declaratively —
see [Manage dufflebag with Terraform](../quick-start/manage-with-terraform.md).

## Channels and revocation

The server accumulates each channel's assignment history. When a version is
revoked, every channel pointing at it rolls back to the most recent
non-revoked version in that history — see
[Versions — revoking](./versions.md#revoking-a-version). After a rollback,
an `hcp_packer_channel_assignment` in Terraform state appears as drift on
the next plan.
