# Upgrading

Upgrading a deployment means replacing the dufflebag image with a newer tag.
Run the newer image's `migrate` subcommand before that image starts serving.
The [deployment reference](../deployment/index.md#migrations) defines this
migrate-then-serve contract.

## Before 1.0

Before a 1.0 release, the database schema and API surface may change between
versions without a migration path. The
[GitHub release notes](https://github.com/benemon/dufflebag/releases) are the
record for each release. Do not assume upgrade compatibility that a release
note does not state.

::: warning
The bundled Helm chart runs one dufflebag replica and does not provide leader
election or other high-availability machinery. Do not use a multi-replica or
rolling upgrade with this chart. See the
[Helm section](../deployment/index.md#helm).
:::

## Upgrade an instance

Prerequisites: Access to the deployment configuration, the privileged
PostgreSQL migration role, and the unprivileged serving role described in the
[deployment reference](../deployment/index.md#postgresql-two-roles). Select the
target tag after reading its GitHub release notes.

1. Replace the dufflebag image tag in the deployment configuration with the
   target release tag. The [image reference](../deployment/index.md#the-image)
   describes the published tag forms.

2. Run the target image's `migrate` subcommand with the migration role. Wait
   for it to finish before starting the target image's serving process. See
   [Migrations](../deployment/index.md#migrations).

3. Start the target image with the serving role. See
   [Serving](../deployment/operations.md#serving) for the container layout.

4. Request `GET /sys/health` without credentials. Confirm that it returns 200.
   The [serving reference](../deployment/operations.md#serving) documents the
   readiness response and its non-200 states.
