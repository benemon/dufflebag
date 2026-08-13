# The console

The web console is an ordinary client of the same APIs everything else uses —
it holds no privileged paths and no console-only endpoints. Whatever it can
do, you can do with `curl`, Terraform, or the
[platform API](/platform-api.html), and the role rules are identical.

Sign in with a service principal's client ID and secret, then select an
organisation and project. On a fresh instance the console opens with the
first-run wizard instead — see
[Roles and principals](./roles-principals.md) for what initialization mints.

## What your role unlocks

The console shows capability honestly: navigation you cannot use is hidden,
and actions you can see but not perform are disabled with a
`Requires <role>` explanation rather than silently missing.

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
| Revoke / restore versions, delete versions, manage channels, delete buckets | `publisher` |
| Manage principals and secrets, configure Bag Drop | `maintainer` |
| Configure audit targets, manage encryption | `root` |

Maintainers can create projects from the project context picker; platform-scoped roots can
create organisations from the organisation context picker.

Until the console has resolved who you are, it shows the reader-tier
navigation as a safe default; a direct URL to a restricted screen still
answers honestly.

Every destructive action confirms in a modal that arms only after you type a confirmation:
deleting a bucket, version, channel, webhook, principal or Bag Drop configuration, revoking
a version, or stopping a mirror asks for the resource's name; removing an audit target asks
for the target path; revoking a secret, rotating encryption keys, or rewrapping the keyring
asks for the action word itself. Restoring a version is recovery, not destruction, and
confirms with a single click. For set actions, the modal lists every selected resource and
any exclusions, then asks for the fixed action word: `stop mirroring`, `revoke`, or `delete`;
single-resource flows still ask for the resource name.

## The screens

**Buckets** lists the project's registry. Builders can pin buckets they care
about, and un-pin them from the pinned card itself; publishers can delete a
bucket — with a warning when Bag Drop currently mirrors it, since the
deletion propagates to the destination.

**A bucket** opens onto its versions and channels. The versions table shows
completion and revocation state; the channels tab is where publishers create
channels, assign versions and delete channels — only complete, active
versions are offered for assignment, and the managed `latest` cannot be
edited. See [Revocation, restore and channels](./revocation-channels.md) for
the semantics.

**A version** shows its builds, artifacts, findings and ancestry, and carries
the operations card: promote to a channel, revoke (immediately or scheduled),
restore, delete. A **Consume this version** card renders a copyable Terraform
`hcp_packer_version` + `hcp_packer_artifact` block when a channel points at
the version — the console's standing handoff to automation.

**A build** shows its status history, artifacts and SBOMs.

**Principals** (maintainer) is the credential lifecycle: create principals,
issue secrets with an expiry choice, revoke, delete. One-time secrets are
shown exactly once — see [Roles and principals](./roles-principals.md).

**Bag Drop** shows mirror status to any reader; maintainers enable, verify
and disable the destination there — enabling configures it in one step. See
the [Bag Drop guide](./bag-drop.md).

**Audit** (root) manages audit targets — up to three — and shows each
target's health. Removing the last target gets an explicit warning, because
audit-enabled instances fail closed.

**Encryption** (root) shows the encryption-at-rest posture and keyring state,
surfaces key-service heartbeat problems, and offers key rotation. On an
instance without encryption at rest it says so plainly.

**Instance** generates the client environment block for the selected tenancy
— the `HCP_*` exports that point stock Packer and Terraform at this instance
(see [Getting started](./getting-started.md)) — and reports scanner and
build information. It warns if the console is not served over HTTPS, since
authentication requires an HTTPS URL.

## Honest states

Screens state their failures rather than masking them: a load failure names
what could not be loaded, a refused action says it was refused, and an empty
screen says why it is empty. If the console disagrees with what the API told
you, trust the API — and file an issue.

## Where to go next

- [Getting started](./getting-started.md) — the client environment block in
  use.
- [Roles and principals](./roles-principals.md) — the role ladder behind the
  capability gates.
