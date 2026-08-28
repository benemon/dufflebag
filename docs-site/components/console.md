# The console

The web console uses the same APIs as every other client. It has no
privileged paths or console-only endpoints. Anything it can do is also
available through `curl`, Terraform, or the
[platform API](/platform-api.html). The same role rules apply to every
client.

The per-screen walkthroughs live in [Operations](../operations/buckets.md)
(the registry screens) and [Administration](../administration/principals.md)
(the administrative screens).

## Sign in

Prerequisites: A service principal's client ID and secret. On a fresh
instance, the console opens the first-run wizard instead - see
[Bootstrap](../quick-start/bootstrap.md).

1. Sign in with the service principal's client ID and secret.
2. Select an organisation and project.

The console follows the system theme. Its masthead toggle remembers your
choice.

::: warning
The console warns if it is not served over HTTPS. Authentication requires an
HTTPS URL.
:::

## What your role unlocks

The console hides navigation your role cannot use. Actions that require a
higher role are shown disabled, with the required role named. The disabled
action displays the requirement as `Requires <role>`.

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

Maintainers can create projects from the project context picker.
Platform-scoped roots can create organisations from the organisation context
picker.

Until the console resolves who you are, it shows the reader-tier navigation.
A direct URL to a restricted screen still reports that access is restricted.

## Confirmations

::: warning
Every destructive action opens a confirmation modal. The modal arms only
after you type a confirmation. Deleting a bucket, version, channel, webhook,
principal, or Bag Drop configuration, revoking a version, or stopping a
mirror requires the resource's name. Removing an audit target requires the
target path. Revoking a secret, rotating encryption keys, or rewrapping the
keyring requires the action word itself.
:::

![Dufflebag typed confirmation modal for revoking a version](/screenshots/typed-confirm.png)

Restoring a version uses a single-click recovery confirmation. For set
actions, the modal lists every selected resource and any exclusions. It then
requires the fixed action word `stop mirroring`, `revoke`, or `delete`.
Single-resource flows still require the resource name.

## Refresh

Console screens refresh automatically every few seconds while work is in
motion, such as running builds, incomplete versions, or Bag Drop
convergence, and every half-minute when the screen is stable. Refresh pauses
while the browser tab is hidden. The manual refresh button starts an
immediate background refresh without blanking the screen or replacing its
current data with a loading state.

## Failure states

Screens report failures directly. A load failure names what could not be
loaded. A refused action says it was refused. An empty screen explains why it
is empty. If the console disagrees with an API response, the API response is
authoritative. Report the discrepancy as an issue.
