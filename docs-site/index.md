# Dufflebag

Dufflebag is a self-hosted registry serving the HCP Packer API. Stock Packer
can publish build metadata to it, and `terraform-provider-hcp` can read and
manage the supported registry resources.

It stores registry metadata, not machine images. The server also includes a
console for initialisation, tenancy and service-principal management, and
registry browsing.

Dufflebag is an independent community project. It is not maintained, supported
or endorsed by IBM or HashiCorp.

Getting going:

- [Install an instance](./guides/installation.md)
- [Point Packer and Terraform at an instance](./guides/getting-started.md)
- [Roles, principals and credentials](./guides/roles-principals.md)

Day to day:

- [The console](./guides/console.md)
- [Manage the registry with Terraform](./guides/terraform.md)
- [Revocation, restore and channels](./guides/revocation-channels.md)
- [SBOMs and vulnerability findings](./guides/sbom-findings.md)

Operating:

- [Mirror to another registry with Bag Drop](./guides/bag-drop.md)
- [Audit and encryption](./guides/audit-encryption.md)
- [Use the platform API](./guides/platform-api.md)
