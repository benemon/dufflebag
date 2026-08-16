# The console

The web console uses the same APIs as every other client. It has no privileged
paths or console-only endpoints. Anything it can do is also available through
`curl`, Terraform, or the [platform API](/platform-api.html). The same role
rules apply to every client.

## Sign in

Prerequisites: A service principal's client ID and secret. On a fresh instance,
the console opens the first-run wizard instead. See
[Roles and principals](./roles-principals.md) for what initialization mints.

1. Sign in with the service principal's client ID and secret.

2. Select an organisation and project.

The console follows the system theme. Its masthead toggle remembers your
choice.

## What your role unlocks

The console hides navigation your role cannot use. Actions that require a higher role are shown disabled, with the required role named.
The disabled action displays the requirement as `Requires <role>`.

Navigation by role:

| Section | Minimum role |
|---|---|
| Registry (buckets, versions, builds), Bag Drop status, Instance | `reader` |
| Principals | `maintainer` |
| Audit, Encryption | `root` |

Actions by role:

| Action | Minimum role |
|---|---|
| Pin buckets | `builder` |
| Revoke / restore versions, delete versions, manage unrestricted channels, delete buckets | `publisher` |
| Manage restricted channels | `maintainer` |
| Manage principals and secrets, configure Bag Drop | `maintainer` |
| Configure audit targets, manage encryption | `root` |

Maintainers can create projects from the project context picker. Platform-scoped
roots can create organisations from the organisation context picker.

Until the console resolves who you are, it shows the reader-tier navigation.
A direct URL to a restricted screen still reports that access is restricted.

::: warning
Every destructive action opens a confirmation modal. The modal arms only after
you type a confirmation. Deleting a bucket, version, channel, webhook,
principal, or Bag Drop configuration, revoking a version, or stopping a mirror
requires the resource's name. Removing an audit target requires the target
path. Revoking a secret, rotating encryption keys, or rewrapping the keyring
requires the action word itself.
:::

![Dufflebag typed confirmation modal for revoking a version](/screenshots/typed-confirm.png)

Restoring a version uses a single-click recovery confirmation. For set actions,
the modal lists every selected resource and any exclusions. It then requires
the fixed action word `stop mirroring`, `revoke`, or `delete`. Single-resource
flows still require the resource name.

## The screens

Console screens refresh automatically every few seconds while work is in motion,
such as running builds, incomplete versions, or Bag Drop convergence, and every
half-minute when the screen is stable. Refresh pauses while the browser tab is
hidden. The manual refresh button starts an immediate background refresh without
blanking the screen or replacing its current data with a loading state.

### Buckets

The **Buckets** screen lists the project's registry.

![Dufflebag Buckets screen showing the project registry](/screenshots/buckets.png)

Builders can pin buckets and unpin them from the pinned card. Publishers can
delete a bucket.

::: warning
When Bag Drop currently mirrors a bucket, the console warns that deleting the
bucket also deletes it from the destination.
:::

### Bucket

A bucket opens onto its versions and channels. The versions table shows
completion and revocation state.

![Dufflebag bucket screen showing bucket details, ancestry, versions, and channels](/screenshots/bucket-facets.png)

Publishers use the channels tab to create, assign and delete unrestricted
channels; restricted targets require a maintainer. Only complete, active
versions are offered for assignment. The managed `latest` channel cannot be
edited. See
[Revocation, restore and channels](../using/revocation-channels.md) for the
semantics.

### Version

A version shows its builds, artifacts, findings and ancestry. Its operations
card can promote the version to a channel, revoke it immediately or on a
schedule, restore it, or delete it.

![Dufflebag version screen showing the version operations card](/screenshots/version-operations.png)

When a channel points at the version, the **Consume this version** card renders
a copyable Terraform `hcp_packer_version` + `hcp_packer_artifact` block. The
card provides the console's standing handoff to automation.

The card offers Terraform plus platform-appropriate commands for platforms the
version actually built. Terraform is always present and selected by default;
platforms without a confident command mapping fall back to Terraform.

![Dufflebag consumption card showing the Terraform and platform command toggle](/screenshots/version-consume.png)

### Build

A build shows its status history, artifacts and SBOMs.

### Principals

The **Principals** screen is available to maintainers. It creates principals,
issues secrets with an expiry choice, revokes secrets, and deletes principals.

![Dufflebag Principals screen showing principal credentials and actions](/screenshots/principals.png)

One-time secrets are shown exactly once. See
[Roles and principals](./roles-principals.md).

### Bag Drop

The **Bag Drop** screen shows mirror status to every reader. Maintainers enable,
verify, and disable the destination there. Enabling configures the destination
in one step. See the [Bag Drop guide](./bag-drop.md).

### Audit

The **Audit** screen is available to roots. It manages up to three audit targets
and shows each target's health.

::: warning
The console presents an explicit warning before removing the last audit target.
An audit-enabled instance fails closed without a target.
:::

### Encryption

The **Encryption** screen is available to roots. It shows the
encryption-at-rest posture and keyring state, surfaces key-service heartbeat
problems, and offers key rotation. On an instance without encryption at rest,
the screen states that encryption at rest is not enabled.

### Instance

The **Instance** screen generates the `HCP_*` client environment exports for
the selected tenancy. These exports point stock Packer and Terraform at the
instance. See [Getting started](../getting-started/first-use.md). The screen
also reports scanner and build information.

::: warning
The console warns if it is not served over HTTPS. Authentication requires an
HTTPS URL.
:::

## Failure states

Screens report failures directly. A load failure names what could not be
loaded. A refused action says it was refused. An empty screen explains why it
is empty. If the console disagrees with an API response, the API response is
authoritative. Report the discrepancy as an issue.

## Where to go next

- [Getting started](../getting-started/first-use.md): the client environment block in
  use.
- [Roles and principals](./roles-principals.md): the role ladder behind the
  capability gates.
