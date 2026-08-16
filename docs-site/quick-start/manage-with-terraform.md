# Manage dufflebag with Terraform

`terraform-provider-hcp` is the automation interface for a dufflebag
registry. It includes resources, not only data sources, so it can manage
buckets, channels, and channel assignments declaratively. Dufflebag supports
version 0.84.0 or newer; the end-to-end suite runs version 0.112.0.

This page starts with the consumption lookup — resolving a published image —
then covers declarative management.

## Configure the provider

Prerequisites: The `HCP_*` environment variables from
[Build an image with Packer](./build-with-packer.md) and
`terraform-provider-hcp` 0.84.0 or newer.

1. Set `skip_status_check = true` in the provider configuration. The HCP
   status page is outside dufflebag's surface; skipping the check prevents
   the run from calling the HCP service.

2. Run Terraform with the `HCP_*` environment variables set.

::: info
The provider fetches the selected project through the resource-manager API,
even when `HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` are set. Packer skips
this call. Dufflebag serves the provider's request without additional
configuration.
:::

## Look up an image

Resolve the managed `latest` channel to the current version, then its
artifact by platform and region:

```hcl
terraform {
  required_providers {
    hcp = {
      source  = "hashicorp/hcp"
      version = ">= 0.84.0"
    }
  }
}

provider "hcp" {
  skip_status_check = true
}

data "hcp_packer_version" "example" {
  bucket_name  = "dufflebag-example"
  channel_name = "latest"
}

data "hcp_packer_artifact" "example" {
  bucket_name         = "dufflebag-example"
  version_fingerprint = data.hcp_packer_version.example.fingerprint
  platform            = "docker"
  region              = "docker"
}
```

- `hcp_packer_version` resolves a channel to its current version — the
  consumption pattern, most often against `latest`. It refuses a revoked
  version, so consumers cannot build from a version that has been pulled.
- `hcp_packer_artifact` returns the `external_identifier` consumed by a
  downstream build.
- `hcp_packer_bucket_names` lists the project's buckets.

The console's consumption card on a version screen renders this block
ready-to-paste for any resolvable version — see
[Versions](../operations/versions.md).

## Manage the registry declaratively

- **`hcp_packer_bucket`** creates and manages a bucket. Destroying the
  resource tolerates an already-deleted bucket and fails on any other error.
- **`hcp_packer_channel`** manages user channels. It can also manage the
  managed `latest` channel: `CreateBucket` creates `latest`, so the provider
  receives an already-exists response and adopts the existing channel. Omit
  the `restricted` argument from a channel block that manages `latest` —
  setting it makes the provider try to update a managed channel, and
  dufflebag refuses the update.
- **`hcp_packer_channel_assignment`** records a promotion by assigning a
  version fingerprint to a channel. Destroying the resource clears the
  assignment.

::: warning
`hcp_packer_run_task`, an inbound-webhook feature of HCP Terraform, is not
supported. Bucket IAM bindings are also not supported.
:::

### A working example

The end-to-end suite applies this shape against a real dufflebag instance:

```hcl
resource "hcp_packer_bucket" "images" {
  name = "app-image"
}

resource "hcp_packer_channel" "production" {
  bucket_name = hcp_packer_bucket.images.name
  name        = "production"
}

# CreateBucket already made the managed latest channel; this block adopts it.
resource "hcp_packer_channel" "latest" {
  bucket_name = hcp_packer_bucket.images.name
  name        = "latest"
}

resource "hcp_packer_channel_assignment" "production" {
  bucket_name         = hcp_packer_bucket.images.name
  channel_name        = hcp_packer_channel.production.name
  version_fingerprint = var.release_fingerprint
}
```

### Promotion as code

Prerequisites: An `hcp_packer_channel_assignment` resource and the
fingerprint of a version that passed testing.

1. Change `release_fingerprint` to the selected fingerprint.
2. Apply the change.

The server accumulates the channel's assignment history. Revocation rollback
walks that history when a version is pulled; after a revocation rolls a
channel back, the assignment in Terraform state appears as drift on the next
plan. See [Versions — revocation](../operations/versions.md) and
[Channels](../operations/channels.md).

The console provides the same operations to publishers. Use Terraform to
keep pipeline operations declarative.

## Next

[Upgrading](./upgrading.md) — replacing the image and verifying readiness.
