# Dufflebag

Packer builds machine images, but that's where its responsibilities end. In
isolation, it is not designed to answer questions such as:

- Which image should production be using right now?
- Is the image that was deployed into staging the same one that production
  is about to get?
- When a CVE lands in a base image, which of your images carry it — and how
  do you stop the next deployment from using them?

Answering those questions takes a registry: one record of every build, with
named pointers that say which version each environment should consume —
instead of image IDs pasted into tfvars files, wikis and pipeline variables.
HashiCorp built that workflow into
[HCP Packer](https://developer.hashicorp.com/hcp/docs/packer), as a hosted
service on their cloud platform.

Dufflebag is an independent implementation of the
[HCP Packer APIs](https://developer.hashicorp.com/hcp/api-docs/packer) that
you host yourself. The stock `packer` binary publishes to it and
`terraform-provider-hcp` reads from it with nothing changed but environment
variables. Your build metadata stays on infrastructure you run.

::: info
Dufflebag is an independent community project. It is not maintained,
supported or endorsed by IBM or HashiCorp.
:::

What the workflow gives you:

- **One record of every build.** Buckets group image families; a version
  collects the builds of one `packer build` run, with the resulting artifact
  identifiers recorded per platform and region.
- **Promotion through channels.** Channels are named pointers into a
  bucket's version history: assign a version to staging, promote it to
  production when it passes. A release is a channel reassignment, not an
  edit to every consumer.
- **Image lookups from Terraform.** The `hcp_packer_version` and
  `hcp_packer_artifact` data sources resolve "whatever production points at"
  to a concrete image ID at plan time. No hard-coded image IDs.
- **Revocation.** Revoke a version that must no longer be consumed —
  immediately or on a schedule — and restore it if circumstances change.
- **SBOMs and findings.** Builds carry the SBOMs Packer reports, giving each
  image a recorded package inventory. With a scanner configured, that
  inventory is checked against a vulnerability service and findings surface
  in the console — the CVE question answered from recorded inventory rather
  than by re-scanning every image.

Dufflebag stores registry metadata, not the machine images themselves —
images stay wherever Packer put them. The server also includes a console for
initialisation, tenancy and service-principal management, and registry
browsing.

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
