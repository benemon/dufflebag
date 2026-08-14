# Upgrading

Upgrading a deployment means replacing the dufflebag image with a newer tag.
The server applies pending schema migrations at startup, so a single-role
deployment upgrades by starting the new image — nothing else. On a hardened
two-role deployment, the automation that runs the `migrate` subcommand (your
init container or pre-deploy step) applies them instead.
The [deployment reference](../deployment/index.md#migrations) defines both
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

::: warning
The bundled Helm chart runs one dufflebag replica and does not provide leader
election or other high-availability machinery. Do not use a multi-replica or
rolling upgrade with this chart. See the
[Helm section](../deployment/index.md#helm).
:::

## Upgrade an instance

Prerequisites: Access to the deployment configuration. On a
[hardened two-role deployment](../deployment/index.md#hardened-two-roles),
also the automation that runs migrations with the privileged role. Select the
target tag after reading its GitHub release notes.

1. Replace the dufflebag image tag in the deployment configuration with the
   target release tag. The [image reference](../deployment/index.md#the-image)
   describes the published tag forms.

2. On a two-role deployment only: let your automation run the target image's
   `migrate` subcommand with the migration role, and wait for it to finish
   before the target image serves. See
   [Migrations](../deployment/index.md#migrations). A single-role deployment
   skips this step; the server migrates at startup.

3. Start the target image. See
   [Serving](../deployment/operations.md#serving) for the container layout.

4. Request `GET /sys/health` without credentials. Confirm that it returns 200.
   The [serving reference](../deployment/operations.md#serving) documents the
   readiness response and its non-200 states.
