# Roles and principals

Every client of a dufflebag instance — Packer, Terraform, the console, your
scripts — authenticates as a **service principal** with client credentials.
There are no user accounts. A principal is bound to one scope, holds one
role, and authenticates with up to two active secrets.

Roles are resolved from the database on every request, never baked into the
token, so revoking a secret or changing a binding takes effect immediately.

## The role ladder

Roles are strictly ordered — each adds to the one below it:

| Role | Adds |
|---|---|
| `reader` | Sees buckets, versions, builds, channels, SBOMs and findings. |
| `builder` | Creates buckets, versions and builds — the smallest role that completes a `packer build`, and what a CI build credential should hold. |
| `publisher` | Assigns channels and promotes, revokes and restores versions, deletes buckets, versions and builds. |
| `maintainer` | Manages principals and role bindings within its scope, and operational configuration such as Bag Drop. |
| `root` | Everything, including creating organisations and configuring authentication and audit. Platform scope only. |

The `builder` / `publisher` split is deliberate: declaring a channel is asking
to promote, and a CI credential that can promote straight to production is
fine until it is not. Give build pipelines `builder`; keep promotion behind a
separate credential.

## Scopes

A principal is bound to exactly one scope:

- **Platform** — only `root` lives here.
- **Organisation** — sees every project in its organisation.
- **Project** — sees exactly one project.

Both tenancy-scoped kinds exist because the Packer CLI treats them
differently: a project-scoped principal gets a 403 from project discovery
(which Packer turns into "try setting `HCP_PROJECT_ID`"), while an org-scoped
principal seeing several projects picks the oldest and warns. Set
`HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` explicitly and neither path
matters.

Authorization always checks tenancy before role: a caller outside the tenant
gets the same 404 as for something that does not exist (existence is a
disclosure); a caller inside the tenant with an insufficient role gets a 403.

## Principals and secrets

Creating a principal mints **no** credentials — it cannot authenticate until
a secret is explicitly issued. Issuing is always the same call, whether it is
the first secret or a rotation:

- A principal may hold **two active secrets** at once. That is what makes
  rotation gap-free: issue the second, roll the deployment onto it, revoke
  the first.
- Issuing a third is refused while two are *usable* — rotation is a
  deliberate sequence, not accumulation. An expired secret does not count
  against the cap.
- **Expiry is chosen at issue time** and lives on the secret, not the
  principal: never, 90 days, or a custom timestamp. The outgoing secret of a
  rotation can expire soon while its replacement expires later.
- The secret value is returned exactly once, at issue. Store it then.
- Expiry never deletes anything — an expired secret stops granting access but
  stays listed until revoked.
- Any principal except root may be left secretless (a maintainer issues it a
  replacement — so a leaked credential can be revoked *immediately*, before
  its successor exists). Revoking the last usable never-expiring secret of a
  **root** principal is refused: a secretless root would leave the instance
  administrable only by direct database access.

The console's **Principals** screen drives all of this — create, issue with
the expiry choice, revoke, delete — and shows the one-time secret exactly
once. The same operations are in the
[platform API reference](/platform-api.html) under `principals`.

Principal management requires `maintainer` on the scope in question; a
maintainer manages principals within its organisation or project, and `root`
manages everything including other platform concerns.

## The root principal and recovery

The first principal comes from initialization: one unauthenticated
`POST /sys/init`, callable exactly once, returns the root principal's
credentials **and the recovery shares** (with their threshold) exactly once.
The console's first-run wizard is an ordinary client of the same endpoint —
there is no privileged side door. Store the credentials in a secret manager
and the shares separately: on an unencrypted deployment, `POST /sys/recovery`
accepts a threshold of shares and mints a fresh root if the credentials are
ever lost. Every attempt, including refusals, is audited.

The full break-glass ceremony is in the
[deployment guide](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md).

## Where to go next

- [Getting started](./getting-started.md) — minting a builder principal and
  pointing Packer at the instance.
- [Platform API reference](/platform-api.html) — the `principals` and
  `organizations` endpoint families.
