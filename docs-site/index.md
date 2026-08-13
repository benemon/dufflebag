# Dufflebag

Dufflebag is a self-hosted registry serving the HCP Packer API. Stock Packer
can publish build metadata to it, and `terraform-provider-hcp` can read and
manage the supported registry resources.

It stores registry metadata, not machine images. The server also includes a
console for initialisation, tenancy and service-principal management, and
registry browsing.

Dufflebag is an independent community project. It is not maintained, supported
or endorsed by IBM or HashiCorp.

This documentation tracks the current development branch.

Getting started:

- [Install an instance](./getting-started/installation.md) — run an instance with Helm,
  on OpenShift, or as a container.
- [Point Packer and Terraform at an instance](./getting-started/first-use.md) —
  initialize an instance and connect the stock clients.

Administration:

- [Roles, principals and credentials](./administration/roles-principals.md) — the role
  ladder, service principals, secrets and recovery.
- [The console](./administration/console.md) — the web console's screens, role gates
  and confirmations.
- [Audit](./administration/audit.md) — audit targets.
- [Encryption](./administration/encryption.md) — encryption at rest.
- [Mirror to another registry with Bag Drop](./administration/bag-drop.md) — mirror a
  project's registry data to another registry.
- [Send project events with webhooks](./administration/webhooks.md) — deliver signed
  project events to an HTTP endpoint.

Using the registry:

- [Manage the registry with Terraform](./using/terraform.md) — manage buckets,
  channels and assignments declaratively.
- [Revocation, restore and channels](./using/revocation-channels.md) —
  channels, promotion, and revoking and restoring versions.
- [SBOMs and vulnerability findings](./using/sbom-findings.md) — upload SBOMs
  and read package and vulnerability findings.

Deployment:

- [Deploy an instance](./deployment/index.md) — configure PostgreSQL, TLS,
  migrations and first run.
- [Configure object storage](./deployment/object-storage.md) — store SBOMs and
  vulnerability-scanning transcripts in an S3-compatible service.
- [Configure encryption](./deployment/encryption-setup.md) — connect the key
  service and operate the encrypted posture.
- [Operate an instance](./deployment/operations.md) — configure scanning,
  webhooks and Bag Drop.

Reference:

- [Architecture](./reference/architecture.md) — the boundaries a change must
  respect.
- [HCP Packer API compatibility](./reference/compatibility.md) — the external
  client contract dufflebag serves.
- [Use the platform API](./reference/platform-api.md) — authentication, tenancy
  and the platform endpoints.
