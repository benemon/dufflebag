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

Quick Start:

- [Installation](./quick-start/installation.md) — a serving instance with
  Helm, on OpenShift, or as a container, and the deployment detail.
- [Bootstrap](./quick-start/bootstrap.md) — initialize the instance, create
  an organisation and project, and mint a builder credential.
- [Build an image with Packer](./quick-start/build-with-packer.md) — point
  the stock client at the instance and publish build metadata.
- [Manage dufflebag with Terraform](./quick-start/manage-with-terraform.md) —
  look up published images and manage the registry declaratively.
- [Upgrading](./quick-start/upgrading.md) — replace the image and verify
  readiness; migrations apply at startup.

Components:

- [Architecture](./components/architecture.md) — the boundaries a change must
  respect.
- [The console](./components/console.md) — sign-in, role gates,
  confirmations and refresh behaviour.
- [Database](./components/database.md) — the PostgreSQL role model and
  migrations.
- [Object storage](./components/object-storage.md) — SBOM payloads and scan
  transcripts in an S3-compatible service.
- [Encryption](./components/encryption.md) — encryption at rest, key
  rotation and recovery.
- [Vulnerability scanning](./components/vulnerability-scanning.md) — the OSV
  adapter and its operator contract.

Administration:

- [Principals](./administration/principals.md) — the role ladder, service
  principals, secrets and recovery.
- [Audit](./administration/audit.md) — audit targets and the audit trail.
- [Encryption](./administration/encryption.md) — the console's encryption
  screen: posture, health and rotation.
- [Bag Drop](./administration/bag-drop.md) — mirror a project's registry
  data to another registry.
- [Webhooks](./administration/webhooks.md) — deliver signed project events
  to an HTTP endpoint.
- [Instance](./administration/instance.md) — the client environment block
  and instance health.

Operations:

- [Buckets](./operations/buckets.md) — the registry listing and the bucket
  drill-down.
- [Versions](./operations/versions.md) — versions, consumption, revocation
  and restore.
- [Channels](./operations/channels.md) — channels, promotion and
  restriction.
- [Builds](./operations/builds.md) — builds, SBOMs, packages and findings.

Integrations:

- [MCP server](./integrations/mcp-server.md) — expose the registry to
  agentic clients as a set of typed tools.

Reference:

- [HCP Packer API compatibility](./reference/compatibility.md) — the external
  client contract dufflebag serves.
- [Use the platform API](./reference/platform-api.md) — authentication,
  tenancy and the platform endpoints.
