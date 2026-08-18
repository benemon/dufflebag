# Upgrading

Upgrading a deployment means replacing the dufflebag image with a newer tag.
The server applies pending schema migrations at startup, so a single-role
deployment upgrades by starting the new image — nothing else. On a hardened
two-role deployment, the automation that runs the `migrate` subcommand (your
init container or pre-deploy step) applies them instead.
[Database — migrations](../components/database.md#migrations) defines both
paths.

## Before 1.0

Before a 1.0 release, the database schema and API surface may change between
versions without a migration path. The
[GitHub release notes](https://github.com/benemon/dufflebag/releases) are the
record for each release. Do not assume upgrade compatibility that a release
note does not state.

Deployments that set `DFBG_VAULT_AUTH_METHOD=env` must set it to `token`
instead. The process refuses to start with the old value and names the rename.

Primary Vault connection configuration moved to `DFBG_VAULT_ADDR`,
`DFBG_VAULT_TOKEN`, `DFBG_VAULT_CACERT`, and
`DFBG_VAULT_TRANSIT_NAMESPACE`. The native variables still function, but the
documented surface is `DFBG_`.

Restricted-channel authorization is now enforced. Restricted user channels
are invisible below `builder`, while their creation, assignment, update and
deletion require `maintainer`. Reader principals also lose access to the
restricted managed `latest` channel; create an unrestricted channel for
reader consumption before upgrading. Automation principals that manage
restricted user channels must be raised to `maintainer` before upgrading.

::: warning
The bundled Helm chart runs one dufflebag replica and does not provide leader
election or other high-availability machinery. Do not use a multi-replica or
rolling upgrade with this chart. See the
[Helm section](./installation.md#kubernetes-helm).
:::

## Upgrade an instance

Prerequisites: Access to the deployment configuration. On a
[hardened two-role deployment](../components/database.md#hardened-two-roles),
also the automation that runs migrations with the privileged role. Select the
target tag after reading its GitHub release notes.

1. Back up the database before changing the deployment. Follow
   [Backup and restore](../components/backup-restore.md).

2. Replace the dufflebag image tag in the deployment configuration with the
   target release tag. The [image reference](./installation.md#the-image)
   describes the published tag forms.

3. On a two-role deployment only: let your automation run the target image's
   `migrate` subcommand with the migration role, and wait for it to finish
   before the target image serves. See
   [Migrations](../components/database.md#migrations). A single-role deployment
   skips this step; the server migrates at startup.

4. Start the target image. See
   [Serving](./installation.md#serving-and-readiness) for the container layout.

5. Request `GET /sys/health` without credentials. Confirm that it returns 200.
   The [serving reference](./installation.md#serving-and-readiness) documents the
   readiness response and its non-200 states.
