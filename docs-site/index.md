# Dufflebag

Dufflebag is a self-hosted registry serving the HCP Packer API. Stock Packer
can publish build metadata to it, and `terraform-provider-hcp` can read and
manage the supported registry resources.

It stores registry metadata, not machine images. The server also includes a
console for initialisation, tenancy and service-principal management, and
registry browsing.

Dufflebag is an independent community project. It is not maintained, supported
or endorsed by IBM or HashiCorp.

- [Install an instance](./guides/installation.md)
- [Point Packer and Terraform at an instance](./guides/getting-started.md)
- [Use the platform API](./guides/platform-api.md)
