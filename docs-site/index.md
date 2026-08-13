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

Getting going:

- [Install an instance](./guides/installation.md) — run an instance with Helm,
  on OpenShift, or as a container.
- [Point Packer and Terraform at an instance](./guides/getting-started.md) —
  initialize an instance and connect the stock clients.
- [Roles, principals and credentials](./guides/roles-principals.md) — the role
  ladder, service principals, secrets and recovery.

Day to day:

- [The console](./guides/console.md) — the web console's screens, role gates
  and confirmations.
- [Manage the registry with Terraform](./guides/terraform.md) — manage buckets,
  channels and assignments declaratively.
- [Revocation, restore and channels](./guides/revocation-channels.md) —
  channels, promotion, and revoking and restoring versions.
- [SBOMs and vulnerability findings](./guides/sbom-findings.md) — upload SBOMs
  and read package and vulnerability findings.

Operating:

- [Mirror to another registry with Bag Drop](./guides/bag-drop.md) — mirror a
  project's registry data to another registry.
- [Send project events with webhooks](./guides/webhooks.md) — deliver signed
  project events to an HTTP endpoint.
- [Audit and encryption](./guides/audit-encryption.md) — audit targets and
  encryption at rest.
- [Use the platform API](./guides/platform-api.md) — authentication, tenancy
  and the platform endpoints.
