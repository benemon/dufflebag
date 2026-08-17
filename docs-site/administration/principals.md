# Principals

Every client of a dufflebag instance authenticates as a **service principal**
with client credentials. This includes Packer, Terraform, the console, and
scripts. There are no user accounts. A principal is bound to one scope, holds
one role, and authenticates with up to two active secrets.

Roles are resolved from the database on every request. They are never baked
into the token, so revoking a secret or changing a binding takes effect
immediately.

## The role ladder

Roles are strictly ordered. Each role adds to the one below it:

| Role | Adds |
|---|---|
| `reader` | Sees buckets, versions, builds, unrestricted channels, SBOMs and findings. |
| `builder` | Creates buckets, versions and builds, and sees and consumes restricted channels — the smallest role that completes a `packer build`, and what a CI build credential should hold. |
| `publisher` | Assigns and manages unrestricted channels, promotes, revokes and restores versions, and deletes buckets, versions and builds. |
| `maintainer` | Creates, assigns, updates and deletes restricted channels, and manages principals, role bindings and operational configuration such as Bag Drop within its scope. |
| `root` | Everything, including creating organisations and configuring authentication and audit. Platform scope only. |

The `builder` and `publisher` roles separate builds from promotion.
Declaring a channel requests promotion.

Restricted channels, including the managed `latest`, are invisible below
`builder`. Restricted user channels require `maintainer` to create or
manage; the existing service-managed mutation refusals continue to govern
`latest` for every role.

::: warning
A CI credential with promotion access can promote directly to production.
Give build pipelines the `builder` role. Keep promotion behind a separate
credential.
:::

## Scopes

A principal is bound to exactly one scope:

- **Platform:** only `root` lives here.
- **Organisation:** sees every project in its organisation.
- **Project:** sees exactly one project.
- **Bucket:** sees exactly one bucket inside its project. The narrowest
  binding — the right shape for a CI pipeline that publishes one image line.
  It can read and (with `builder`) publish into its bucket, and nothing else
  in the project exists for it: sibling buckets answer not-found, and bucket
  creation and deletion are refused. In the console it lands directly in its
  bucket rather than on the Buckets screen. The bucket must exist before the
  principal is created, and cannot be deleted while the principal lives.

Bucket-scoped principals are created through the platform API today by
passing `bucket_id` (the bucket's ULID) alongside the organisation and
project; the console's create form does not yet offer the scope.

The two tenancy-scoped kinds behave differently in the Packer CLI. A
project-scoped principal gets a 403 from project discovery, which Packer
turns into "try setting `HCP_PROJECT_ID`". An organisation-scoped principal
that sees several projects selects the oldest and warns.

::: tip
Set `HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` explicitly to avoid both
discovery paths.
:::

Authorization checks tenancy before role. A caller outside the tenant gets
the same 404 as a resource that does not exist. This response prevents
disclosure of the resource's existence. A caller inside the tenant with an
insufficient role gets a 403.

## The Principals screen

The **Principals** screen is available to maintainers. It creates
principals, issues secrets with an expiry choice, revokes secrets, and
deletes principals. One-time secrets are shown exactly once. The same
operations are in the [platform API reference](/platform-api.html) under
`principals`.

![Dufflebag Principals screen showing principal credentials and actions](/screenshots/principals.png)

### Create a principal and issue its first secret

Prerequisites: Permission to manage principals in the target scope — a
maintainer manages principals within its organisation or project; a root
manages every principal.

1. Create the principal. A new principal has no credentials and cannot
   authenticate.

2. Issue its first secret. Choose an expiry of never, 90 days, or a custom
   timestamp. Expiry belongs to the secret, not the principal.

3. Store the secret value when it is returned. The value is returned exactly
   once, at issue.

The same issue operation creates a first secret or a rotation secret. A
principal may hold two active secrets at once. Issuing a third secret is
refused while two are usable. An expired secret does not count against the
cap. Expiry does not delete a secret. An expired secret stops granting
access but stays listed until revoked.

### Rotate a secret

Prerequisites: A principal with one usable secret and permission to manage
it.

1. Issue a second secret.
2. Roll the deployment onto the second secret.
3. Revoke the first secret.

This sequence rotates credentials without an authentication gap. During a
rotation, the outgoing secret can expire soon while its replacement expires
later.

::: warning
Any principal except root may be left without a usable secret. This allows a
leaked credential to be revoked immediately, before its replacement exists.
Revoking the last usable, never-expiring secret of a root principal is
refused. A root without a secret would leave the instance administrable only
through direct database access.
:::

## The root principal and recovery

Initialization creates the first principal. One unauthenticated
`POST /sys/init` request can be made exactly once. It returns the root
principal's credentials, the recovery shares, and their threshold exactly
once. The console's first-run wizard uses the same endpoint. It has no
privileged side door. See [Bootstrap](../quick-start/bootstrap.md).

::: warning
Store the root credentials in a secret manager and store the recovery shares
separately.
:::

On an unencrypted deployment, `POST /sys/recovery` accepts a threshold of
shares and mints a fresh root if the credentials are ever lost. Every
recovery attempt is audited, including refusals. The recovery ceremony and
the break-glass floor below it are in
[Encryption — recovery](../components/encryption.md#recovery).
